package tunnel

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"
	"time"
)

// isTimeout 判断这个错误是不是"到点了"而不是"坏了"。
// 两者处理方式完全相反：到点 = 接着等，坏了 = 收摊 ✓
func isTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return errors.Is(err, os.ErrDeadlineExceeded)
}

// 一条打洞通道要分给两种流量用：KCP 的可靠字节流 + 游戏的裸 UDP。
//
// 🔑 为什么必须分开：
//
//	KCP 是「可靠字节流」—— 重传、保序、有拥塞控制。
//	而游戏 UDP 要的恰恰**不是**这些：它要"快、丢了就丢、下一条更新更重要"。
//	把游戏 UDP 塞进 KCP，等于给它加一层不必要的重传和等待 ⇒
//	延迟变差，语义也被破坏 ✗
//
// 📌 怎么区分两种流量：每个出站包前面加 **1 字节类型**。
//
//	[0x01][KCP 的包]
//	[0x02][游戏的裸 UDP 包]
//
// 📌 为什么不靠 KCP 自己的 conv 头：游戏发的包可能碰巧撞上那个数字 ✗
// 📌 为什么不开两条隧道各打一次洞：打两次洞的成功率是**相乘**的，
//
//	会明显变低 ✗ 而分流只多一个字节。
//
// ⚠️ **本文件只是能力，还没接进任何流程**（见 docs/UDP支持计划.md 的阶段 1）——
// 现有 TCP 链路一个字节都没动 ✓
const (
	typKCP uint8 = 0x01
	typUDP uint8 = 0x02
	// typUDP2 是**第二路**游戏 UDP（目前只有《饥荒》用：主世界 10999 + 洞穴 10998）。
	//
	// 📌 为什么是"再加一个类型值"而不是"在 UDP 帧里塞管道号"：
	//    帧头本来就有一个类型字节，加个枚举值是最小改动，而且**老对端能兼容**
	//    （它只认 typKCP/typUDP，收到 typUDP2 会当成"不认识"丢掉 ✓ 不会乱）✓
	typUDP2 uint8 = 0x03
	// typPing 是心跳（2026-10-18）：用来量**延迟和丢包**，也顺带证明通道还活着。
	//
	// 🩸 兼容性陷阱：老客户端不认识 0x04，会把它当 KCP 包塞进 kcpCh ✗
	//    而 KCP 读到 **conv 不匹配**会静默丢弃 ✓
	//    ⇒ 我们在包头塞一个"永远不等于 Conv 的常量"，老端就完全无感、不会因此断线 ✓
	typPing uint8 = 0x04
)

// pingGuard 保证"误入 KCP"时一定被丢掉：Conv 是 1，这个值不可能是它 ✓
const pingGuard uint32 = 0xFFFFFFFF

const (
	pingKindReq uint8 = 0 // 我发的，等对方回
	pingKindRes uint8 = 1 // 对方回的
	// [4 字节 guard][1 字节 kind][4 字节 seq][8 字节 时间戳]
	pingSize = 4 + 1 + 4 + 8
)

// 队列长度。满了就丢包 —— 这是**故意的**：
//   - 游戏 UDP 本来就允许丢，丢了自己会处理 ✓
//   - KCP 那头丢了它自己会重传 ✓
//
// 在这儿阻塞反而会把收包 goroutine 卡死，把另一路也拖垮 ✗
const splitQueueLen = 256

// 收包缓冲。KCP 的 MTU 是 1200，游戏 UDP 一般也在 1200 以内 ✓
const splitReadBuf = 2048

type splitPacket struct {
	data []byte
	addr net.Addr
}

// SplitConn 把一条打洞通道分给两种流量。
//
// 它实现 net.PacketConn，所以可以**直接当 kcp.NewConn3 的底层连接**传进去 ——
// KCP 只会看见 typKCP 的包，游戏的裸 UDP 走 ReadUDP / WriteUDP ✓
type SplitConn struct {
	underlying net.PacketConn

	kcpCh  chan splitPacket
	udpCh  chan splitPacket
	udpCh2 chan splitPacket // 第二路游戏 UDP（见 typUDP2）

	closeOnce sync.Once
	closed    chan struct{}

	// 出站要往包里插类型字节，得先把整包拼好再写 —— 不然两路并发写会交错 ✗
	writeMu sync.Mutex

	// 读超时。KCP 会调 SetReadDeadline 做"多久没数据就当断了"，
	// 得如实支持，否则它的行为会变 ✗
	rdMu       sync.Mutex
	rdDeadline time.Time

	// 心跳（见 typPing）
	peerMu  sync.Mutex
	peer    net.Addr // 第一次收到包就记住对端，发心跳要用
	pingMu  sync.Mutex
	pingSeq uint32
	pingAt  map[uint32]time.Time // 还没回的：seq -> 发出时刻
	onPing  func(rtt time.Duration, lost bool)
}

// NewSplitConn 在一条**已经打通**的 UDP 通道上建分流层，并起一个收包 goroutine。
//
// ⚠️ 调用之后那条底层通道就**只能**通过返回值读写了 ——
// ReadFrom 被收包 goroutine 占着，再直接去读会把包抢走 ✗
func NewSplitConn(pc net.PacketConn) *SplitConn {
	m := &SplitConn{
		underlying: pc,
		kcpCh:      make(chan splitPacket, splitQueueLen),
		udpCh:      make(chan splitPacket, splitQueueLen),
		udpCh2:     make(chan splitPacket, splitQueueLen),
		closed:     make(chan struct{}),
	}
	go m.readLoop()
	return m
}

// readLoop 是唯一读底层的地方：读到包 → 看类型字节 → 丢进对应的队列。
func (m *SplitConn) readLoop() {
	buf := make([]byte, splitReadBuf)
	for {
		n, addr, err := m.underlying.ReadFrom(buf)
		if err != nil {
			// 🩸 **超时不算"通道坏了"** —— 别把整个分流层关掉 ✗
			//
			// （上面 SetReadDeadline 已经不给底层设 deadline 了，
			//   所以正常情况走不到这儿；但用户/别的代码可能自己设了一个，
			//   那时候也该是"这一次没读到"而不是"整条通道死了"）
			if isTimeout(err) {
				continue
			}
			m.Close()
			return
		}
		if n < 1 {
			continue // 连类型字节都没有，不是我们的包
		}

		// 必须抄一份：buf 下一轮就被覆盖了 ✗
		p := make([]byte, n-1)
		copy(p, buf[1:n])

		// 记住对端：发心跳要知道往哪儿发（收包这儿是唯一稳定的来源）✓
		m.peerMu.Lock()
		if m.peer == nil {
			m.peer = addr
		}
		m.peerMu.Unlock()

		// 心跳自己处理，**绝不往下游队列塞** —— KCP 和 UDP 那两路都不认识它 ✗
		if buf[0] == typPing {
			m.handlePing(buf[1:n], addr)
			continue
		}

		ch := m.kcpCh
		switch buf[0] {
		case typUDP:
			ch = m.udpCh
		case typUDP2:
			ch = m.udpCh2
		}
		select {
		case ch <- splitPacket{data: p, addr: addr}:
		default:
			// 队列满 ⇒ 丢。理由见文件头 ✓
		}
	}
}

/* ---------- 给 KCP 用的那半（net.PacketConn）---------- */

// ReadFrom 只会拿到 typKCP 的包 —— 游戏的裸 UDP 被分到 udpCh 去了 ✓
func (m *SplitConn) ReadFrom(b []byte) (int, net.Addr, error) {
	// 先把 deadline 拿好，避免白等 ✗
	m.rdMu.Lock()
	d := m.rdDeadline
	m.rdMu.Unlock()

	var timeout <-chan time.Time
	if !d.IsZero() {
		left := time.Until(d)
		if left <= 0 {
			return 0, nil, os.ErrDeadlineExceeded
		}
		t := time.NewTimer(left)
		defer t.Stop()
		timeout = t.C
	}

	select {
	case p := <-m.kcpCh:
		n := copy(b, p.data)
		return n, p.addr, nil
	case <-timeout:
		return 0, nil, os.ErrDeadlineExceeded
	case <-m.closed:
		return 0, nil, net.ErrClosed
	}
}

// WriteTo 是 KCP 的出口 —— 自动带上 typKCP ✓
func (m *SplitConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	return m.writeWithType(typKCP, b, addr)
}

// ReadUDP 读一个游戏的裸 UDP 包（返回的内容不含类型字节）。
// 队列空时会阻塞 —— 调用方一般在一个循环里跑 ✓
func (m *SplitConn) ReadUDP(b []byte) (int, net.Addr, error) {
	select {
	case p := <-m.udpCh:
		n := copy(b, p.data)
		return n, p.addr, nil
	case <-m.closed:
		return 0, nil, net.ErrClosed
	}
}

// WriteUDP 发一个游戏的裸 UDP 包 —— 自动带上 typUDP ✓
func (m *SplitConn) WriteUDP(b []byte, addr net.Addr) (int, error) {
	return m.writeWithType(typUDP, b, addr)
}

// ReadUDP2 / WriteUDP2 是**第二路**游戏 UDP（见 typUDP2）。
//
// 📌 单独两个方法而不是"给 ReadUDP 加个 id 参数"：
//
//	加参数要改所有现有调用点，而这一路只有饥荒用得上 ⇒ 少动就少错 ✓
func (m *SplitConn) ReadUDP2(b []byte) (int, net.Addr, error) {
	select {
	case p := <-m.udpCh2:
		n := copy(b, p.data)
		return n, p.addr, nil
	case <-m.closed:
		return 0, nil, net.ErrClosed
	}
}

// WriteUDP2 发第二路的裸 UDP 包 —— 自动带上 typUDP2 ✓
func (m *SplitConn) WriteUDP2(b []byte, addr net.Addr) (int, error) {
	return m.writeWithType(typUDP2, b, addr)
}

func (m *SplitConn) writeWithType(t uint8, b []byte, addr net.Addr) (int, error) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	buf := make([]byte, len(b)+1)
	buf[0] = t
	copy(buf[1:], b)

	n, err := m.underlying.WriteTo(buf, addr)
	if err != nil {
		return 0, err
	}
	// 对上层的口径是"我给你的 b 写进去几个字节" —— 那个类型字节是我们自己加的，不算 ✓
	if n > 0 {
		n--
	}
	return n, nil
}

/* ---------- 其余 PacketConn 接口：如实转发 ---------- */

func (m *SplitConn) Close() error {
	m.closeOnce.Do(func() { close(m.closed) })
	return m.underlying.Close()
}

func (m *SplitConn) LocalAddr() net.Addr { return m.underlying.LocalAddr() }

func (m *SplitConn) SetDeadline(t time.Time) error {
	m.SetReadDeadline(t)
	return m.underlying.SetWriteDeadline(t)
}

func (m *SplitConn) SetReadDeadline(t time.Time) error {
	m.rdMu.Lock()
	m.rdDeadline = t
	m.rdMu.Unlock()
	// 🩸 **不要**转发给底层！
	//
	// 底层那条 socket 是 readLoop **独占在读**的。而 KCP 会周期性地设
	// read deadline（它靠这个判断"多久没数据就当断了"）——
	// 一旦 deadline 到点，readLoop 的 ReadFrom 就收到超时错误，
	// 然后它会把整个分流层关掉 ⇒ **两边一起断** ✗
	//
	// 给 KCP 的超时由 ReadFrom 那个 select 自己实现就够了 ✓
	// （2026-10-15 写这段时转发给了底层，端到端测试直接卡死 5 分钟 ✗）
	return nil
}

func (m *SplitConn) SetWriteDeadline(t time.Time) error {
	// 写这侧转发给底层没问题 —— WriteTo 是立刻返回的，不会有人卡在它上面 ✓
	return m.underlying.SetWriteDeadline(t)
}

// TODO(安全加固，暂不做)：类型字节在密文外，可被中途篡改
//
// 危害上限是"投错管道 ⇒ 游戏收到垃圾包"，不能冒充也不能解密 ✓
// 要根治：把类型字节作为 **AEAD 附加数据**参与校验（改了就解密失败、直接丢）。
// 那需要动 udpcrypt 的 Seal/Open 签名，牵一发动全身，等有实际需要再说 ✓

/* ---------- 心跳：量延迟与丢包 ---------- */

// SetPingHandler 设置回调。rtt>0 表示收到回声；lost=true 表示这一发没回来。
//
// 📌 只回调"结论"，采样和超时判定都在这里做 —— 上层不用管细节 ✓
func (m *SplitConn) SetPingHandler(f func(rtt time.Duration, lost bool)) {
	m.pingMu.Lock()
	m.onPing = f
	m.pingMu.Unlock()
}

// Ping 发一个心跳请求。没回的会在这里判成"丢"，然后回调 lost ✓
func (m *SplitConn) Ping() {
	m.peerMu.Lock()
	peer := m.peer
	m.peerMu.Unlock()
	if peer == nil {
		return // 还没收到过任何包，不知道对端是谁
	}

	m.pingMu.Lock()
	m.pingSeq++
	seq := m.pingSeq
	if m.pingAt == nil {
		m.pingAt = make(map[uint32]time.Time, 8)
	}
	// 先把"等太久没回"的判丢：通道真断了的话，这些永远回不来 ✗
	var lostCall func(rtt time.Duration, lost bool)
	for s, t := range m.pingAt {
		if time.Since(t) > pingLostAfter {
			delete(m.pingAt, s)
			lostCall = m.onPing
		}
	}
	m.pingAt[seq] = time.Now()
	cb := m.onPing
	m.pingMu.Unlock()

	if lostCall != nil {
		lostCall(0, true)
	}
	_ = cb

	buf := make([]byte, pingSize)
	binary.BigEndian.PutUint32(buf[0:], pingGuard)
	buf[4] = pingKindReq
	binary.BigEndian.PutUint32(buf[5:], seq)
	binary.BigEndian.PutUint64(buf[9:], uint64(time.Now().UnixNano()))
	m.writeWithType(typPing, buf, peer)
}

// pingLostAfter 多久没回算丢。2 秒发一次、6 秒算丢 —— 中间丢两三包也还扛得住 ✓
const pingLostAfter = 6 * time.Second

// handlePing 收到心跳：请求就回声，回声就算 RTT。
func (m *SplitConn) handlePing(p []byte, addr net.Addr) {
	if len(p) < pingSize {
		return
	}
	if binary.BigEndian.Uint32(p[0:]) != pingGuard {
		return // 不是我们的心跳（或者是坏包）
	}
	seq := binary.BigEndian.Uint32(p[5:])

	if p[4] == pingKindReq {
		// 原样回声，只把 kind 改成"响应" ✓
		res := make([]byte, pingSize)
		copy(res, p[:pingSize])
		res[4] = pingKindRes
		m.writeWithType(typPing, res, addr)
		return
	}

	m.pingMu.Lock()
	t0, ok := m.pingAt[seq]
	if ok {
		delete(m.pingAt, seq)
	}
	cb := m.onPing
	m.pingMu.Unlock()
	if ok && cb != nil {
		cb(time.Since(t0), false)
	}
}
