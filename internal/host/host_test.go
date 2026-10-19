// 房主侧的收尾测试。
//
// 这里钉的是那个「点了关闭房间，却弹出『穿透服务器连接断开』」的 bug：
// 主动关闭和真断线在 recv 那层长得一模一样，必须靠 h.stopped 区分开。
package host

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// startSilentServer 起一个"哑"服务器：接住连接、读掉注册消息，然后什么都不回。
// 这样房主那边就停在 Recv 上，正好用来测"这段连接最后是怎么结束的"。
//
// closeAfter > 0 表示过一会儿把连接关掉（模拟真断线）；
// closeAfter == 0 表示一直挂着（模拟连接正常，由房主自己叫停）。
func startSilentServer(t *testing.T, closeAfter time.Duration) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4096)
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = c.Read(buf) // 读掉 register，免得房主卡在发送上
		_ = c.SetReadDeadline(time.Time{})
		if closeAfter > 0 {
			time.Sleep(closeAfter)
			return // 关掉连接 = 真断线
		}
		select {} // 一直挂着，等对方自己关
	}()
	return ln
}

func newTestHost(addr string) *Host {
	return New(Config{
		SignalAddr:   addr,
		Room:         "T-HOST-1",
		ClientID:     "cid-test",
		MaxSize:      4,
		PunchTimeout: 2 * time.Second,
	})
}

// runAsync 跑 Run 并把返回塞进 channel。
func runAsync(h *Host) chan error {
	ch := make(chan error, 1)
	go func() { ch <- h.Run() }()
	return ch
}

// 「这位朋友到底连上过没有」决定他离开时说哪句话。
//
// 🩸 老板 2026-10-18 从自己的房主日志里看出来的：打洞失败之后紧跟着一条
// 「非正常退出」—— 那人从头到尾没进过游戏，说「非正常」是用错了词，
// 而且打洞失败时已经明说过「建立连接失败」了，重复一句只让人以为又出了个新问题 ✗
//
// 反证方法：把 leaveLine 开头的 `if !everOK` 那段删掉 ⇒ 第一条断言立刻红 ✓
func TestLeaveLine(t *testing.T) {
	// ① 打洞就没成过 ⇒ 界面**一个字都不说**
	human, detail := leaveLine("玩家 1", false, false)
	if human != "" {
		t.Errorf("从没连上过的人离开，界面不该再说什么，实得：%q", human)
	}
	if !strings.Contains(detail, "没连上") {
		t.Errorf("导出日志里要留真相（排查用），实得：%q", detail)
	}

	// ② 连着的时候主动退 ⇒「退出了房间」
	if h, _ := leaveLine("玩家 1", true, true); !strings.Contains(h, "退出了房间") {
		t.Errorf("主动退出说错了：%q", h)
	}

	// ③ 连着的时候掉线 ⇒「非正常退出」
	h3, _ := leaveLine("玩家 1", true, false)
	if !strings.Contains(h3, "非正常退出") {
		t.Errorf("掉线说错了：%q", h3)
	}
	// 📌 反过来也要钉住：**连上过的人不能被当成"没连上"吞掉**
	if h3 == "" {
		t.Error("连上过的人离开必须说一句，不能被当成从没连上的")
	}
}

// 房主主动关房间（点「关闭房间」/ 退出程序）⇒ Run 必须返回 ErrStopped。
// 返回别的错误，界面就会弹「连接断开了」—— 这正是要防的。
func TestStopIsNotAnError(t *testing.T) {
	h := newTestHost(startSilentServer(t, 0).Addr().String())
	ch := runAsync(h)

	// 等它连上、注册完、停在 Recv 上（否则 Stop 时 h.cl 还没赋上）
	time.Sleep(300 * time.Millisecond)
	h.Stop()

	select {
	case err := <-ch:
		if !errors.Is(err, ErrStopped) {
			t.Fatalf("主动关房间该返回 ErrStopped，实得：%v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Stop 之后 Run 没退出")
	}
}

// 真断线（对面把连接关了、我们没叫停）⇒ 要报错，
// 而且**不能把 Go 的原始错误甩到弹窗上**（"use of closed network connection" 那种）。
func TestRealDisconnectIsReported(t *testing.T) {
	h := newTestHost(startSilentServer(t, 400*time.Millisecond).Addr().String())
	ch := runAsync(h)

	select {
	case err := <-ch:
		if err == nil {
			t.Fatal("真断线该报错，却返回了 nil")
		}
		if errors.Is(err, ErrStopped) {
			t.Fatal("这不是主动关闭，不该返回 ErrStopped")
		}
		msg := err.Error()
		for _, raw := range []string{"use of closed network connection", "read tcp", "connection reset"} {
			if strings.Contains(msg, raw) {
				t.Errorf("弹窗文案里不该出现 Go 原始错误 %q，实得：%q", raw, msg)
			}
		}
	case <-time.After(6 * time.Second):
		t.Fatal("超时没返回")
	}
}
