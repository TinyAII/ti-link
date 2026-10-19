// 信令连接的读取上限测试。
//
// 这条测试防的是"对面发一行几个 G 的假 JSON，把进程顶爆"。
// 用 net.Pipe 造一条真连接，真发一行超长内容，看 Recv 是报错还是把内存吃光。
package signal

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	"p2plink/internal/proto"
)

// msgResult 只是把 Recv 的两个返回值打包过 channel 用。
type msgResult struct {
	msg proto.Msg
	err error
}

func TestRecvRejectsOversizedMessage(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()

	cl := New(serverSide)

	go func() {
		// 发一行超过上限的内容（不是合法 JSON，但**长度**才是这条测试要验的）
		blob := bytes.Repeat([]byte("x"), maxMsgBytes+4096)
		_, _ = clientSide.Write(blob)
		_, _ = clientSide.Write([]byte("\n"))
		// 让 Scanner 能读到上限并报错；写完就等对方关连接
		time.Sleep(200 * time.Millisecond)
		_ = clientSide.Close()
	}()

	_, err := cl.Recv(3 * time.Second)
	if err == nil {
		t.Fatal("超长消息该被拒绝，却读成功了 —— 这条路上能把内存吃光")
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Logf("返回的是 %v（不是 ErrTooLong，但至少报错了、没把内存吃光）", err)
	}
	_ = serverSide.Close()
}

// 正常消息当然要能读出来 —— 别把上限设得连正常消息都过不去。
func TestRecvNormalMessage(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	got := make(chan msgResult, 1)
	go func() {
		cl := New(serverSide)
		m, err := cl.Recv(2 * time.Second)
		got <- msgResult{m, err}
	}()

	if _, err := clientSide.Write([]byte(`{"type":"register","room":"TI123456DY"}` + "\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("正常的单行 JSON 读失败：%v", r.err)
		}
		if r.msg.Room != "TI123456DY" {
			t.Errorf("房间名读错了：%q", r.msg.Room)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("读超时")
	}
}

// 启用规则：域名走 TLS、IP 走明文。
// 这条规则错了会很危险 —— 把生产配成明文、或者让本地联调莫名其妙要证书。
func TestUseTLSFor(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"mc.tinyaii.cn:39000", true}, // 生产
		{"localhost:39000", true},     // 域名（规则要一致，不搞特例）
		{"127.0.0.1:39111", false},    // 本地 e2e
		{"192.168.1.5:25565", false},  // 内网 IP
		{"[::1]:39000", false},        // IPv6 回环也是 IP
		{"没有冒号的串", false},             // 解析不了就按明文处理（连都连不上）
	}
	for _, c := range cases {
		if got := useTLSFor(c.addr); got != c.want {
			t.Errorf("useTLSFor(%q) = %v，期望 %v", c.addr, got, c.want)
		}
	}
}
