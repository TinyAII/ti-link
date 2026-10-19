package tunnel

import (
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePC 是一条可控的假 UDP 通道：写出去的都记下来，读的时候从队列喂。
// 不依赖真实网络 ⇒ 换台机器跑结果一样 ✓
type fakePC struct {
	mu      sync.Mutex
	written [][]byte
	in      chan []byte
	closed  chan struct{}
	once    sync.Once
	local   net.Addr
}

func newFakePC() *fakePC {
	return &fakePC{
		in:     make(chan []byte, 16),
		closed: make(chan struct{}),
		local:  &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4242},
	}
}

// feed 模拟"底层收到一个包"。队列满就丢 —— 测试自己不该把测试卡死 ✓
func (f *fakePC) feed(p []byte) {
	select {
	case f.in <- p:
	default:
	}
}

func (f *fakePC) takeWritten() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.written
	f.written = nil
	return out
}

func (f *fakePC) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case p := <-f.in:
		return copy(b, p), f.local, nil
	case <-f.closed:
		return 0, nil, net.ErrClosed
	}
}

func (f *fakePC) WriteTo(b []byte, addr net.Addr) (int, error) {
	f.mu.Lock()
	f.written = append(f.written, append([]byte(nil), b...))
	f.mu.Unlock()
	return len(b), nil
}

func (f *fakePC) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *fakePC) LocalAddr() net.Addr              { return f.local }
func (f *fakePC) SetDeadline(time.Time) error      { return nil }
func (f *fakePC) SetReadDeadline(time.Time) error  { return nil }
func (f *fakePC) SetWriteDeadline(time.Time) error { return nil }

// 两种类型各走各的队列 —— 这是整个分流的地基。
func TestSplitRoutesByType(t *testing.T) {
	pc := newFakePC()
	m := NewSplitConn(pc)
	defer m.Close()

	pc.feed([]byte{typKCP, 'k', 'c', 'p'})
	pc.feed([]byte{typUDP, 'u', 'd', 'p'})

	var buf [64]byte

	n, _, err := m.ReadFrom(buf[:])
	if err != nil {
		t.Fatalf("ReadFrom 出错: %v", err)
	}
	if got := string(buf[:n]); got != "kcp" {
		t.Errorf("KCP 那侧拿到 %q，该是 kcp（类型字节要剥掉）", got)
	}

	n, _, err = m.ReadUDP(buf[:])
	if err != nil {
		t.Fatalf("ReadUDP 出错: %v", err)
	}
	if got := string(buf[:n]); got != "udp" {
		t.Errorf("裸 UDP 那侧拿到 %q，该是 udp", got)
	}
}

// 出站要自己加上类型字节 —— 对面就是靠它分流的 ✓
func TestSplitWriteAddsTypeByte(t *testing.T) {
	pc := newFakePC()
	m := NewSplitConn(pc)
	defer m.Close()

	m.WriteTo([]byte("abc"), pc.local)
	m.WriteUDP([]byte("xyz"), pc.local)

	w := pc.takeWritten()
	if len(w) != 2 {
		t.Fatalf("应该写出去 2 个包，实得 %d", len(w))
	}
	if string(w[0]) != string([]byte{typKCP, 'a', 'b', 'c'}) {
		t.Errorf("KCP 出站包不对: %v", w[0])
	}
	if string(w[1]) != string([]byte{typUDP, 'x', 'y', 'z'}) {
		t.Errorf("UDP 出站包不对: %v", w[1])
	}

	// 返回的 n 只算"上层给的字节"，不该把类型字节算进去 ✗
	if n, _ := m.WriteUDP([]byte("xy"), pc.local); n != 2 {
		t.Errorf("WriteUDP 该返回 2，实得 %d", n)
	}
}

// 队列满的时候必须丢包、不能把收包那头卡死 ——
// 那会把另一路也一起拖垮 ✗
func TestSplitDropsInsteadOfBlocking(t *testing.T) {
	pc := newFakePC()
	m := NewSplitConn(pc)
	defer m.Close()

	done := make(chan struct{})
	go func() {
		// 一口气灌进去远超队列容量的包，中间一个都不读
		for i := 0; i < splitQueueLen*3; i++ {
			pc.feed([]byte{typUDP, byte(i)})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("喂包卡住了 —— 说明队列满的时候在阻塞，该丢包才对")
	}

	// 而且收包 goroutine 还活着：还能读到东西 ✓
	var buf [64]byte
	if _, _, err := m.ReadUDP(buf[:]); err != nil {
		t.Errorf("队列满之后还该读得到: %v", err)
	}
}

// 没有类型字节的野包直接忽略，不能当成 KCP 的包塞进去
func TestSplitDropsEmptyPacket(t *testing.T) {
	pc := newFakePC()
	m := NewSplitConn(pc)
	defer m.Close()

	pc.feed([]byte{}) // 0 字节
	pc.feed([]byte{typKCP, 'o', 'k'})

	var buf [64]byte
	n, _, err := m.ReadFrom(buf[:])
	if err != nil {
		t.Fatalf("出错: %v", err)
	}
	if got := string(buf[:n]); got != "ok" {
		t.Errorf("拿到 %q，该是 ok（那个空包该被丢掉）", got)
	}
}

// Close 之后两边都该立刻醒过来，而不是一直等 ✗
func TestSplitCloseUnblocksReaders(t *testing.T) {
	pc := newFakePC()
	m := NewSplitConn(pc)

	errc := make(chan error, 2)
	go func() { var b [8]byte; _, _, err := m.ReadFrom(b[:]); errc <- err }()
	go func() { var b [8]byte; _, _, err := m.ReadUDP(b[:]); errc <- err }()

	time.Sleep(30 * time.Millisecond)
	m.Close()

	for i := 0; i < 2; i++ {
		select {
		case err := <-errc:
			if err == nil {
				t.Error("Close 之后读该返回错误")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Close 之后还有读卡着 —— 该立刻返回才对")
		}
	}
}

// 心跳：发出去要能收到回声，RTT 要像回事，而且**不能漏进 KCP/UDP 队列**。
//
// 🩸 最后那条是兼容性底线：老客户端不认识 0x04，会把它当 KCP 包 ✗
//
//	我们靠包头里的 pingGuard（≠ Conv）让老端的 KCP 静默丢弃 ✓
//	所以本地这边也必须保证它不进 kcpCh —— 否则就是自己先犯同样的错 ✗
func TestSplitPingEcho(t *testing.T) {
	a, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("开 socket 失败: %v", err)
	}
	defer a.Close()
	b, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("开 socket 失败: %v", err)
	}
	defer b.Close()

	sa := NewSplitConn(a)
	defer sa.Close()
	sb := NewSplitConn(b)
	defer sb.Close()

	// 先互发一个普通包：两边都得知道对方地址才能发心跳（peer 是从收包记下来的）
	sa.WriteTo([]byte{typKCP, 1, 2, 3}, b.LocalAddr())
	sb.WriteTo([]byte{typKCP, 4, 5, 6}, a.LocalAddr())
	time.Sleep(150 * time.Millisecond)

	got := make(chan time.Duration, 4)
	sa.SetPingHandler(func(rtt time.Duration, lost bool) {
		if !lost {
			got <- rtt
		}
	})
	sa.Ping()

	select {
	case rtt := <-got:
		// 📌 本机回环可能量出 0（Windows 的计时器粒度就这么粗），
		//    所以只卡"不能是负数、不能离谱" ✓ 真实网络里是毫秒级
		if rtt < 0 || rtt > time.Second {
			t.Errorf("RTT 不合理：%v（本机回环应该是一瞬间）", rtt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("没收到心跳回声 —— 对端没处理 typPing？")
	}

	// 心跳不能出现在 KCP 队列里
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case p := <-sa.kcpCh:
			if len(p.data) >= 4 && binary.BigEndian.Uint32(p.data) == pingGuard {
				t.Fatal("心跳漏进了 KCP 队列 —— 老客户端会因此收到坏包")
			}
		case <-deadline:
			return
		}
	}
}

// StartProbe 要能真的报出"直连 v4 · N ms"。
//
// 🩸 只测心跳回声不够 —— 用户看到的是**这句拼好的话**，
//
//	窗口/丢包统计那层要是算错，界面就会显示成一堆没意义的数字 ✗
func TestStartProbeReports(t *testing.T) {
	a, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("开 socket 失败: %v", err)
	}
	defer a.Close()
	b, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("开 socket 失败: %v", err)
	}
	defer b.Close()

	sa := NewSplitConn(a)
	defer sa.Close()
	sb := NewSplitConn(b)
	defer sb.Close()

	// 先让两边都记住对端
	sa.WriteTo([]byte{typKCP, 1}, b.LocalAddr())
	sb.WriteTo([]byte{typKCP, 2}, a.LocalAddr())
	time.Sleep(120 * time.Millisecond)

	got := make(chan LiveStat, 16)
	stop := make(chan struct{})
	stopFn := StartProbe(sa, b.LocalAddr(), 100*time.Millisecond, func(s LiveStat) {
		select {
		case got <- s:
		default:
		}
	}, stop)
	defer stopFn()

	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-got:
			if !s.OK {
				continue // 第一次是"测速中"
			}
			if s.Family != "v4" {
				t.Errorf("本机走的是 v4，报成了 %q", s.Family)
			}
			text := s.String()
			if !strings.Contains(text, "直连 v4") || !strings.Contains(text, "ms") {
				t.Errorf("状态文案不对：%q", text)
			}
			return
		case <-deadline:
			t.Fatal("3 秒都没报出实时状态")
		}
	}
}
