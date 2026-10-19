package tunnel

import (
	"net"
	"strings"
	"testing"
	"time"
)

// udpEcho 是个假游戏：收到什么原样发回去 ✓
func udpEcho(pc net.PacketConn) {
	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		pc.WriteTo(buf[:n], addr)
	}
}

// 这是阶段 2 真正的验收：一条打洞通道上**同时**跑 TCP（走 KCP）和 UDP（裸转发），
// 两边都得通 —— 只跑通一路不算，那证明不了分流是对的 ✓
func TestTunnelCarriesUDPAndKCPAtOnce(t *testing.T) {
	// 两端各一条真实 UDP socket，互相知道地址（现实中这步是打洞做的）
	a, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	key := []byte("0123456789abcdef")
	splitA, splitB := NewSplitConn(a), NewSplitConn(b)

	// ── 两端的 KCP 会话（就是现在搬 TCP 游戏的那条路）──
	sessA, err := NewSessionWithKey(splitA, b.LocalAddr(), key)
	if err != nil {
		t.Fatalf("A 建 KCP 失败: %v", err)
	}
	defer sessA.Close()
	sessB, err := NewSessionWithKey(splitB, a.LocalAddr(), key)
	if err != nil {
		t.Fatalf("B 建 KCP 失败: %v", err)
	}
	defer sessB.Close()

	// ── 假游戏：一个 UDP 回声服务 ──
	game, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer game.Close()
	go udpEcho(game)

	// ── 房主那侧：连本机游戏（fixedDest = 游戏地址）──
	hostLocal, err := net.DialUDP("udp4", nil, game.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	pipeHost, err := NewUDPPipe(splitA, key, hostLocal, b.LocalAddr(), game.LocalAddr(), false, nil)
	if err != nil {
		t.Fatalf("房主侧 UDP 管子建不起来: %v", err)
	}
	defer pipeHost.Close()

	// ── 玩家那侧：在本机端口等游戏连（fixedDest = nil）──
	playerLocal, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pipePlayer, err := NewUDPPipe(splitB, key, playerLocal, a.LocalAddr(), nil, false, nil)
	if err != nil {
		t.Fatalf("玩家侧 UDP 管子建不起来: %v", err)
	}
	defer pipePlayer.Close()

	// 模拟"玩家本机的游戏"：主动连玩家那侧
	app, err := net.DialUDP("udp4", nil, playerLocal.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// ══ 第一件事：UDP 要能穿过去，而且要原样回来 ══
	app.SetDeadline(time.Now().Add(5 * time.Second))
	msg := "hello udp 穿过隧道"
	if _, err := app.Write([]byte(msg)); err != nil {
		t.Fatalf("写不出去: %v", err)
	}
	buf := make([]byte, 2048)
	n, err := app.Read(buf)
	if err != nil {
		t.Fatalf("UDP 没回来: %v", err)
	}
	if got := string(buf[:n]); got != msg {
		t.Errorf("回来的是 %q，该是 %q", got, msg)
	}

	// ══ 第二件事：**同时**TCP 那路也得通 —— 这才证明两条流量没打架 ══
	go sessA.Write([]byte("这是一条走 KCP 的 TCP 数据"))

	sessB.SetReadDeadline(time.Now().Add(5 * time.Second))
	kb := make([]byte, 256)
	kn, err := sessB.Read(kb)
	if err != nil {
		t.Fatalf("KCP 那路被 UDP 影响了: %v", err)
	}
	if !strings.Contains(string(kb[:kn]), "KCP") {
		t.Errorf("KCP 收到 %q", kb[:kn])
	}

	// ══ 第三件事：UDP 再来一轮，证明它没被 KCP 那波流量弄坏 ══
	app.SetDeadline(time.Now().Add(5 * time.Second))
	msg2 := "第二轮"
	if _, err := app.Write([]byte(msg2)); err != nil {
		t.Fatal(err)
	}
	n, err = app.Read(buf)
	if err != nil {
		t.Fatalf("第二轮 UDP 没回来: %v", err)
	}
	if got := string(buf[:n]); got != msg2 {
		t.Errorf("第二轮回来的是 %q", got)
	}
}

// 超限的包要**丢掉**，不能硬塞 —— 硬塞会让对面的解密直接失败 ✗
// （这条只钉"超限的被丢"，完整链路在端到端那条里验 ✓）
func TestUDPPipeDropsOversizedPacket(t *testing.T) {
	tunnelSide, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer tunnelSide.Close()
	peerSide, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer peerSide.Close()

	split := NewSplitConn(tunnelSide)
	defer split.Close()

	playerLocal, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer playerLocal.Close()

	pipe, err := NewUDPPipe(split, []byte("0123456789abcdef"), playerLocal, peerSide.LocalAddr(), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()

	// 在"隧道对面"那头收，看实际发出来了几个包
	recv := make(chan int, 8)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, _, err := peerSide.ReadFrom(buf)
			if err != nil {
				return
			}
			recv <- n
		}
	}()

	app, _ := net.DialUDP("udp4", nil, playerLocal.LocalAddr().(*net.UDPAddr))
	defer app.Close()

	// 先发个正常大小的（顺带让转发层认识来源），再发一个明显超限的
	app.Write([]byte("ok"))
	app.Write(make([]byte, udpMaxPayload+500))

	var sizes []int
	deadline := time.After(1500 * time.Millisecond)
loop:
	for {
		select {
		case n := <-recv:
			sizes = append(sizes, n)
		case <-deadline:
			break loop
		}
	}

	if len(sizes) == 0 {
		t.Fatal("正常大小的包也该发出来，一个都没收到")
	}
	if len(sizes) > 1 {
		t.Errorf("超限的包也被发出来了（一共收到 %d 个），该丢掉才对", len(sizes))
	}
}

// 坏包/垃圾包不能把转发循环打断 —— UDP 上这太常见了 ✓
func TestUDPPipeSurvivesGarbage(t *testing.T) {
	a, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer a.Close()
	b, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer b.Close()

	splitA, splitB := NewSplitConn(a), NewSplitConn(b)
	defer splitA.Close()
	defer splitB.Close()

	key := []byte("0123456789abcdef")
	playerLocal, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer playerLocal.Close()
	pipe, err := NewUDPPipe(splitB, key, playerLocal, a.LocalAddr(), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()

	// 从 A 那侧直接往隧道里灌垃圾（模拟别人乱发包）✓
	for _, junk := range [][]byte{
		{}, {1, 2, 3}, make([]byte, 40), make([]byte, udpTunnelMTU),
	} {
		splitA.WriteUDP(junk, b.LocalAddr())
	}

	// 灌完之后，正常的包还得能过去（说明循环还活着）✓
	game, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer game.Close()
	go udpEcho(game)

	hostLocal, _ := net.DialUDP("udp4", nil, game.LocalAddr().(*net.UDPAddr))
	defer hostLocal.Close()
	pipeHost, err := NewUDPPipe(splitA, key, hostLocal, b.LocalAddr(), game.LocalAddr(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeHost.Close()

	app, _ := net.DialUDP("udp4", nil, playerLocal.LocalAddr().(*net.UDPAddr))
	defer app.Close()

	app.SetDeadline(time.Now().Add(5 * time.Second))
	app.Write([]byte("still alive"))
	var buf [64]byte
	n, err := app.Read(buf[:])
	if err != nil {
		t.Fatalf("垃圾包把转发搞坏了: %v", err)
	}
	if string(buf[:n]) != "still alive" {
		t.Errorf("拿到 %q", buf[:n])
	}
}

// ⭐⭐ 验收：同一个隧道上挂**两条** UDP 管子，两路不能串。
//
// 🩸 这是"饥荒洞穴"那个功能的底线：主世界 10999 和洞穴 10998 是两条独立的管子，
//
//	如果串了，玩家在地面上会收到洞穴的包、进洞穴又收到地面的包 —— 游戏直接乱 ✗
//
// 反证方法：把 split.go 里 `case typUDP2: ch = m.udpCh2` 改成 `ch = m.udpCh`
// （两路都进同一个队列）⇒ 立刻红 ✓
func TestUDPPipesDoNotMix(t *testing.T) {
	a, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	key := []byte("0123456789abcdef")
	splitA, splitB := NewSplitConn(a), NewSplitConn(b)

	// 两个"假游戏"：分别代表主世界和洞穴
	game1, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer game1.Close()
	go udpEcho(game1)
	game2, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer game2.Close()
	go udpEcho(game2)

	// 房主侧：两条管子分别连两个游戏端口
	h1, err := net.DialUDP("udp4", nil, game1.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	pipeH1, err := NewUDPPipe(splitA, key, h1, b.LocalAddr(), game1.LocalAddr(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeH1.Close()
	go pipeH1.KeepAlive()

	h2, err := net.DialUDP("udp4", nil, game2.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	pipeH2, err := NewUDPPipe(splitA, key, h2, b.LocalAddr(), game2.LocalAddr(), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeH2.Close()
	go pipeH2.KeepAlive()

	// 玩家侧：两条管子各听一个本地端口
	p1, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pipeP1, err := NewUDPPipe(splitB, key, p1, a.LocalAddr(), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeP1.Close()
	p2, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pipeP2, err := NewUDPPipe(splitB, key, p2, a.LocalAddr(), nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pipeP2.Close()

	send := func(pc net.PacketConn, s string) {
		pc.WriteTo([]byte(s), pc.LocalAddr())
	}
	send(p1, "GROUND")
	send(p2, "CAVE")

	read := func(pc net.PacketConn) string {
		buf := make([]byte, 256)
		pc.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return ""
		}
		return string(buf[:n])
	}

	// 两路各自回自己的话，而且**绝不能看到对方的内容** ✓
	if got := read(p1); got != "GROUND" {
		t.Fatalf("主世界那路收到 %q，期望 GROUND ✗（串到洞穴去了？）", got)
	}
	if got := read(p2); got != "CAVE" {
		t.Fatalf("洞穴那路收到 %q，期望 CAVE ✗（串到主世界去了？）", got)
	}
}
