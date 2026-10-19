// tunnel 的测试：在**真 UDP** 上开一对 KCP 会话，看数据能不能过去。
//
// 为什么要这么测：这层是"游戏数据到底能不能穿过去"的最后一道 ✗
// 只断言"函数不报错"是没用的 —— 密钥两端不一致时，会话建得起来、
// 数据却过不去，那种毛病只有真跑一次数据才看得出来 ✓
package tunnel

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// key16 是 AES-128 的密钥长度（两端必须一致）。
var key16 = []byte("0123456789abcdef")

func udpPair(t *testing.T) (*net.UDPConn, *net.UDPConn) {
	t.Helper()
	mk := func() *net.UDPConn {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	return mk(), mk()
}

// 两端同一把密钥 ⇒ 数据能过去。
func TestSessionCarriesData(t *testing.T) {
	a, b := udpPair(t)

	sa, err := NewSessionWithKey(a, b.LocalAddr(), key16)
	if err != nil {
		t.Fatalf("A 建会话失败: %v", err)
	}
	defer sa.Close()
	sb, err := NewSessionWithKey(b, a.LocalAddr(), key16)
	if err != nil {
		t.Fatalf("B 建会话失败: %v", err)
	}
	defer sb.Close()

	want := []byte("hello-p2plink")
	if _, err := sa.Write(want); err != nil {
		t.Fatalf("写失败: %v", err)
	}

	_ = sb.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 128)
	n, err := sb.Read(buf)
	if err != nil {
		t.Fatalf("对面没读到数据: %v —— 隧道不通", err)
	}
	if !bytes.Equal(buf[:n], want) {
		t.Errorf("读到的内容不对：%q", buf[:n])
	}
}

// 🩸 两端密钥**不一致** ⇒ 必须读不到东西。
// 这条是上面那条的反面：如果密钥根本没参与加密，这条会红 ✓
// （也正是那个经典症状："打洞成功、握手静默失败" —— 用户只看到卡住 ✗）
func TestWrongKeyGetsNothing(t *testing.T) {
	a, b := udpPair(t)

	sa, err := NewSessionWithKey(a, b.LocalAddr(), key16)
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	// 对面用另一把密钥
	sb, err := NewSessionWithKey(b, a.LocalAddr(), []byte("fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	_, _ = sa.Write([]byte("应该过不去"))

	_ = sb.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 128)
	if n, err := sb.Read(buf); err == nil {
		t.Fatalf("密钥不同却读到了 %d 字节（%q）—— 加密没起作用", n, buf[:n])
	}
}

// 多路复用：一条会话上开两条流，互不串。
// 这是"MC 一次连接开两条 TCP"那个需求的底层保证 ✓
func TestMuxCarriesTwoStreams(t *testing.T) {
	a, b := udpPair(t)
	sa, err := NewSessionWithKey(a, b.LocalAddr(), key16)
	if err != nil {
		t.Fatal(err)
	}
	defer sa.Close()
	sb, err := NewSessionWithKey(b, a.LocalAddr(), key16)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()

	cli, err := MuxClient(sa)
	if err != nil {
		t.Fatalf("客户端多路复用失败: %v", err)
	}
	defer cli.Close()
	srv, err := MuxServer(sb)
	if err != nil {
		t.Fatalf("服务端多路复用失败: %v", err)
	}
	defer srv.Close()

	// 服务端接受两条流
	got := make(chan string, 2)
	go func() {
		for i := 0; i < 2; i++ {
			s, err := srv.AcceptStream()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, _ := s.Read(buf)
			got <- string(buf[:n])
		}
	}()

	for _, msg := range []string{"first", "second"} {
		st, err := cli.OpenStream()
		if err != nil {
			t.Fatalf("开流失败: %v", err)
		}
		if _, err := st.Write([]byte(msg)); err != nil {
			t.Fatalf("写流失败: %v", err)
		}
	}

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case s := <-got:
			seen[s] = true
		case <-time.After(6 * time.Second):
			t.Fatalf("第二条流没到（已收到 %v）", seen)
		}
	}
	if !seen["first"] || !seen["second"] {
		t.Errorf("两条流该都到，实得 %v", seen)
	}
}
