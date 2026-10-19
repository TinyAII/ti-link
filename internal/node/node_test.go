// 玩家侧"游戏连入 / 断开"的状态流转测试。
//
// 为什么值得测：这条状态直接决定界面上那盏「已连接房主服务器」的灯。
// 原来连接结束时压根不切回去 —— 游戏都关了灯还绿着 ✗（老板实测撞到的）。
//
// 这里不用真网络：serveStream 的那点判断全在 streamOpened / streamClosed 里，
// 塞一个记录用的 OnState 进去就能把**状态序列**抄下来。
package node

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"p2plink/internal/signal"
)

// 记下每次状态变化，方便断言"该报几条、报的是什么"。
type stateRec struct {
	states []State
	msgs   []string
}

func newRecRunner() (*Runner, *stateRec) {
	rec := &stateRec{}
	r := &Runner{
		OnState: func(s State, msg string) {
			rec.states = append(rec.states, s)
			rec.msgs = append(rec.msgs, msg)
		},
	}
	return r, rec
}

// 一条连接：连上报 bridging，断开必须**切回 tunnel**（这就是老板那个 bug）。
func TestStreamCloseRevertsState(t *testing.T) {
	r, rec := newRecRunner()

	r.streamOpened()
	if len(rec.states) != 1 || rec.states[0] != StateBridging {
		t.Fatalf("第一条连上该报 bridging，实得 %v", rec.states)
	}

	r.streamClosed()
	if len(rec.states) != 2 {
		t.Fatalf("断开后该再报一条状态，实得 %v", rec.states)
	}
	if rec.states[1] != StateTunnel {
		t.Fatalf("断开后该切回 tunnel（不然界面的灯一直绿着），实得 %v", rec.states[1])
	}
}

// 🩸 MC 一下会开**两条** TCP，而且它们不是同时结束的：
//
//	· 第二条进来时不该重复报 bridging（不然灯闪）
//	· 只关掉一条时不该切回 tunnel（不然灯闪回去又亮）
//
// 只有**计数归零**才算真的断开。
func TestStreamCountNoFlicker(t *testing.T) {
	r, rec := newRecRunner()

	r.streamOpened() // MC 的第一条（查询）
	r.streamOpened() // MC 的第二条（真进游戏）—— 不该再报一次
	if len(rec.states) != 1 {
		t.Fatalf("第二条进来不该重复报状态，实得 %v", rec.states)
	}

	r.streamClosed() // 关掉查询那条
	if len(rec.states) != 1 {
		t.Fatalf("还留着一条在转发，不该切回 tunnel，实得 %v", rec.states)
	}

	r.streamClosed() // 都走了
	if len(rec.states) != 2 || rec.states[1] != StateTunnel {
		t.Fatalf("都断开后该切回 tunnel，实得 %v", rec.states)
	}
}

// 断开之后再进一次游戏，要能重新亮起来（别一次之后就废了）。
func TestStreamReopen(t *testing.T) {
	r, rec := newRecRunner()

	r.streamOpened()
	r.streamClosed()
	r.streamOpened() // 再进游戏

	want := []State{StateBridging, StateTunnel, StateBridging}
	if len(rec.states) != len(want) {
		t.Fatalf("状态序列该是 %v，实得 %v", want, rec.states)
	}
	for i := range want {
		if rec.states[i] != want[i] {
			t.Fatalf("状态序列该是 %v，实得 %v", want, rec.states)
		}
	}
}

// 信令连接断了 ⇒ 会话必须结束，而且要说清原因。
//
// 🩸 这条是补 2026-10-17 的事故：`watchSignal` 里 Recv 出错**直接 return**，
// 于是隧道和本地 25565 监听都还活着 —— 界面一直显示"已连入、数据转发中"、
// 游戏也能连上本地端口，可房主那边早就没了 ⇒ **僵尸房间** ✗
// （老板主机上真实发生过：管理后台一个房间都没有，他这边还"在房间里"）
func TestSignalLostStopsSession(t *testing.T) {
	r := New(Config{Local: "127.0.0.1:0"})
	var notices []string
	r.OnNotice = func(s string) { notices = append(notices, s) }

	r.onSignalLost(errors.New("use of closed network connection"))

	if !r.stopped() {
		t.Fatal("信令断了，会话却没停 —— 会变成僵尸房间")
	}
	if len(notices) != 1 {
		t.Errorf("应该给用户一条提示，实际 %d 条", len(notices))
	}
	// 原因要能传到上层（backend 靠它决定弹不弹错误框）
	if err := r.abortReason(); errors.Is(err, ErrStopped) {
		t.Error("信令断了却当成「用户主动停」，上层只会静默收尾")
	} else if !strings.Contains(err.Error(), "连接断了") {
		t.Errorf("原因文案不对：%v", err)
	}
}

// 用户自己点的「退出房间」⇒ 即使之后 Recv 报错，也不能被记成"出事了"，
// 否则每次正常退出都弹一个错误框 ✗
func TestSignalLostAfterUserStopIsNormal(t *testing.T) {
	r := New(Config{Local: "127.0.0.1:0"})
	r.Stop() // 模拟用户点了退出

	r.onSignalLost(errors.New("use of closed network connection")) // 收尾时常见的报错

	if err := r.abortReason(); !errors.Is(err, ErrStopped) {
		t.Errorf("主动退出被记成了异常：%v", err)
	}
}

// Stop() 在还没 Start（各连接都是 nil）时也不能崩 ——
// 🩸 原来把它塞进 []interface{} 统一判空：`*signal.Client` 的 nil 指针
// 装进 interface 后 `c != nil` 是 true ⇒ 调 Close() 空指针解引用 ✗
func TestStopWithoutStartDoesNotPanic(t *testing.T) {
	r := New(Config{Local: "127.0.0.1:0"})
	r.Stop()
	r.Stop() // 重复调用也不能崩（once 保证）
}

// watchSignal 这条**接线**也要测：判断逻辑对了，但没人调用它照样是僵尸房间。
// （上面那几个测试直接调 onSignalLost，覆盖不到"调用点有没有接上"✗）
//
// 做法：起一个本地监听假装信令服务器，用真的 signal.Dial 连上去
// （IP 走明文，不用证书），然后关掉连接 ⇒ watchSignal 应该退出并且停掉会话。
func TestWatchSignalStopsRunnerWhenConnDrops(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起本地监听失败：%v", err)
	}
	defer ln.Close()

	// 接受连接后**什么都不做**，就一直挂着 —— 我们只要"连接存在"这件事
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	cl, err := signal.Dial(ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("连不上假服务器：%v", err)
	}

	r := New(Config{Local: "127.0.0.1:0"})
	done := make(chan struct{})
	go func() { defer close(done); r.watchSignal(cl) }()

	time.Sleep(50 * time.Millisecond) // 让它先阻塞在 Recv 上
	cl.Close()                        // 模拟"信令断了"

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("信令断了，watchSignal 还没退出")
	}
	if !r.stopped() {
		t.Error("watchSignal 退出了，但会话没停 —— 就是那个僵尸房间 ✗")
	}
}
