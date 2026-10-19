package tunnel

import (
	"net"
	"sync"
	"time"
)

// 隧道里一个 UDP 包的上限。跟 KCP 的 MTU 对齐（都走同一条通道）✓
const udpTunnelMTU = 1200

// 分流加 1 字节类型、加密加 8 字节序号 + 16 字节 tag ⇒ 一共多 25 字节。
// 所以单个**游戏**包里能带的上限是这么多 ——
// 超了的**直接丢**，不做分片（分片会引入更复杂的问题，而游戏 UDP 基本都在这个数以内）✗
const udpMaxPayload = udpTunnelMTU - 1 - 8 - 16

// 收包缓冲留宽一点：对面包可能比预期大，读的时候要能接住再说 ✓
const udpRecvBuf = 2048

// UDPPipe 把 SplitConn 的「裸 UDP 那半」接到一个**本机 UDP 端点**上。
//
//	隧道来的包  → 解密 → 发给本机
//	本机来的包  → 加密 → 发给隧道
//
// 两种用法（差别只在"本机那头是谁"）：
//
//	房主侧：DialUDP 连本机的游戏服务 ⇒ fixedDest 非空，包总是发给它
//	玩家侧：ListenUDP 等游戏连过来   ⇒ fixedDest 为 nil，回包发给"最近来的那个来源"
//
// 📌 为什么要记 lastLocal 而不是写死一个：游戏重连、或者换个端口再来时
// 来源会变 —— 记死的话它就再也收不到回包了 ✗
//
// 两个方向各一个 goroutine，任一方向出错就整体收摊 ✓
type UDPPipe struct {
	split      *SplitConn
	sealer     *udpSealer
	local      net.PacketConn
	tunnelPeer net.Addr // 打洞出来的对面地址 —— 往隧道发包时用它
	fixedDest  net.Addr // 非 nil = 房主模式（包固定发给本机游戏）
	logf       func(string, ...any)

	mu        sync.Mutex
	lastLocal net.Addr

	// second = 走**第二路**游戏 UDP（typUDP2）。
	//
	// 🩸 为什么要有它：《饥荒》一个游戏占两个端口（主世界 10999 / 洞穴 10998），
	//    两条 pipe 挂同一条隧道的话，包会**互相抢**（隧道出口没有归属信息）✗
	//    ⇒ 让第二条 pipe 走另一个类型值，隧道那边就能分开 ✓
	second bool

	closed chan struct{}
	once   sync.Once
}

// readPkt / writePkt：按 second 决定走哪一路。
//
// 📌 把"选路"收成两个小函数，就不用在一个文件里到处写 if second ✗
func (p *UDPPipe) readPkt(b []byte) (int, net.Addr, error) {
	if p.second {
		return p.split.ReadUDP2(b)
	}
	return p.split.ReadUDP(b)
}

func (p *UDPPipe) writePkt(b []byte, addr net.Addr) (int, error) {
	if p.second {
		return p.split.WriteUDP2(b, addr)
	}
	return p.split.WriteUDP(b, addr)
}

// NewUDPPipe 起一条 UDP 管子。
//
// key 和 KCP 那条用的是同一份（ECDH 协商出来的）—— 但那层加密是"流式"的，
// UDP 用不了，所以这边会**另开一个逐包加密器**（见 udpcrypt.go）✓
func NewUDPPipe(split *SplitConn, key []byte, local net.PacketConn,
	tunnelPeer net.Addr, fixedDest net.Addr, second bool,
	logf func(string, ...any)) (*UDPPipe, error) {

	sealer, err := newUDPSealer(key)
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	p := &UDPPipe{
		split:      split,
		sealer:     sealer,
		local:      local,
		tunnelPeer: tunnelPeer,
		fixedDest:  fixedDest,
		second:     second,
		logf:       logf,
		closed:     make(chan struct{}),
	}
	go p.fromTunnel()
	go p.fromLocal()
	return p, nil
}

// fromTunnel：隧道来的包 → 解密 → 发给本机。
func (p *UDPPipe) fromTunnel() {
	buf := make([]byte, udpRecvBuf)
	for {
		n, _, err := p.readPkt(buf)
		if err != nil {
			p.Close()
			return
		}
		plain, ok := p.sealer.Open(buf[:n])
		if !ok {
			// 坏的 / 重放的 / 太旧的 —— **丢掉就完事了**，
			// 为它打断整个循环才是真的错（UDP 上丢包是常态）✓
			continue
		}
		if len(plain) == 0 {
			// 0 长度 = 对面的**保活包**（KeepAlive 发的）——
			// 它只是为了让 NAT 映射别冷掉，**不能真转给游戏** ✗
			continue
		}
		dest := p.dest()
		if dest == nil {
			// 玩家侧还没收到过游戏发的包 ⇒ 不知道该回给谁，先丢 ✓
			// （正常顺序是游戏先发包、我们才认识它）
			continue
		}
		if err := p.writeLocal(plain, dest); err != nil {
			p.logf("UDP：转发到本机失败 %v", err)
		}
	}
}

// fromLocal：本机来的包 → 加密 → 走隧道。
func (p *UDPPipe) fromLocal() {
	buf := make([]byte, udpRecvBuf)
	for {
		n, src, err := p.local.ReadFrom(buf)
		if err != nil {
			p.Close()
			return
		}
		if n == 0 {
			continue
		}

		// 记住来源：玩家侧的回包要发给它 ✓（房主侧也会记，但没用上）
		p.mu.Lock()
		p.lastLocal = src
		p.mu.Unlock()

		if n > udpMaxPayload {
			// 塞不下就别硬塞 —— 硬塞会让对面的解密直接失败 ✗
			p.logf("UDP：包太大（%d > %d），丢弃", n, udpMaxPayload)
			continue
		}
		pkt := p.sealer.Seal(buf[:n])
		if _, err := p.writePkt(pkt, p.tunnelPeer); err != nil {
			p.logf("UDP：转发到隧道失败 %v", err)
			continue
		}
	}
}

// writeLocal 往本机那头发一个包。
//
// 🩸 分两种情况，不能一刀切：
//
//	房主侧的本机 socket 是 DialUDP 出来的 ⇒ **已经连上游戏了** ⇒ 必须用 Write。
//	  对它调 WriteTo 会直接报错（ErrWriteToConnected）✗
//	玩家侧是 ListenUDP 出来的 ⇒ 没连任何人 ⇒ 只能 WriteTo，目标靠记的来 ✓
//
// （2026-10-15 一开始两种都用了 WriteTo，端到端测试里 UDP 就是回不来 ✗）
func (p *UDPPipe) writeLocal(b []byte, dest net.Addr) error {
	if p.fixedDest != nil {
		if w, ok := p.local.(interface{ Write([]byte) (int, error) }); ok {
			_, err := w.Write(b)
			return err
		}
	}
	_, err := p.local.WriteTo(b, dest)
	return err
}

func (p *UDPPipe) dest() net.Addr {
	if p.fixedDest != nil {
		return p.fixedDest
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastLocal
}

// Close 收摊。两个 goroutine 会因为读端出错自己退出来 ✓
func (p *UDPPipe) Close() error {
	p.once.Do(func() { close(p.closed) })
	// 把本机那侧也关掉，好让阻塞在 ReadFrom 的 goroutine 醒过来 ✓
	return p.local.Close()
}

// 保活间隔：NAT 对 UDP 映射的超时比 TCP 短得多 ✗
// 长时间没流量的话映射会被回收，之后再要发就发不出去了 ⇒ 定期戳一下 ✓
const udpKeepAlive = 20 * time.Second

// KeepAlive 定期往隧道里发一个**空包**，把 NAT 映射焐着。
//
// 📌 两边都要调 —— NAT 映射是各自出站时建立的，只顾一头的话
// 另一头照样会冷掉 ✗
//
// 空包对面解出来是 0 长度，转发层会直接丢掉，不会漏给游戏 ✓
func (p *UDPPipe) KeepAlive() {
	t := time.NewTicker(udpKeepAlive)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			// 空 payload —— 目的只是"让这条路径别冷掉" ✓
			if _, err := p.writePkt(p.sealer.Seal(nil), p.tunnelPeer); err != nil {
				return
			}
		case <-p.closed:
			return
		}
	}
}
