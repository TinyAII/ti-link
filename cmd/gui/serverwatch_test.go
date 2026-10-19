// watchLocalServer 的测试：它盯着本机游戏服务端在不在，把结果推给「房间信息」页那个灯。
//
// 为什么要测：这是"轮询 + 状态翻转才推"的逻辑，光靠截图看不出来
// （截图只能证明灯的样子，证明不了它会不会在服务端开关时跟着变）。
//
// 用假 WebView 把 emit 出去的 JS 收下来 —— 无头环境起不了真 WebView，
// 而这个函数只用到 Dispatch / Eval 两个方法，内嵌接口 + 覆盖它俩就够了。
package main

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webview/webview_go"
)

type fakeWV struct {
	webview.WebView // 内嵌（nil）：只覆盖用到的两个方法，别的一概不碰
	mu              sync.Mutex
	events          []string
}

func (f *fakeWV) Dispatch(fn func()) { fn() }
func (f *fakeWV) Eval(js string) {
	f.mu.Lock()
	f.events = append(f.events, js)
	f.mu.Unlock()
}

func (f *fakeWV) snapshot() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.events, " | ")
}

// waitEvent 等一条同时含 wants 里所有片段的 emit 出现。
func waitEvent(f *fakeWV, wants []string, timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		f.mu.Lock()
		got := strings.Join(f.events, " ")
		f.mu.Unlock()
		ok := true
		for _, w := range wants {
			if !strings.Contains(got, w) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// 🩸 纯 UDP 的游戏（星露谷那种）**探不了** —— 探测是连 TCP，它不听 TCP ✗
//
// 要是照常跑探测循环，界面会一直显示"连不上"，
// 用户以为自己没开好、在那儿白折腾（2026-10-15 老板碰到的就是这个）。
// ⇒ 这种情况该发一次 up=null，让界面如实说"测不了" ✓
//
// 而且要确认它**之后就歇了**：不能还在那儿每 3 秒探一轮（白耗 + 万一某轮探到
// 别的程序占了那个端口，反而会把界面从"测不了"翻成"已连上"）✗
func TestWatchLocalServerUDPOnlySkipsProbe(t *testing.T) {
	// 故意起一个**占着那个端口**的 TCP 监听：
	// 要是还去探，就会探到它、推 up=true —— 那这条测试就会红 ✓
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	f := &fakeWV{}
	b := &backend{w: f}
	stop := make(chan struct{})
	defer close(stop)

	// 空 Modes ⇒ 该走"不探"那条路（发完 up=null 就返回，不起轮询）
	b.watchLocalServer(probeSpec{Addr: addr}, stop)

	if !waitEvent(f, []string{`"server"`, `"up":null`}, 2*time.Second) {
		t.Fatalf("该推一次 up=null（测不了），实得：%s", f.snapshot())
	}
	// 关键：**不能**看到 up=true —— 那说明它还是去探了 ✗
	if waitEvent(f, []string{`"up":true`}, 500*time.Millisecond) {
		t.Fatalf("不该去探那个端口（探到别的程序占着就会假报已连上）✗ 实得：%s", f.snapshot())
	}
}

func TestWatchLocalServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	addr := ln.Addr().String()

	f := &fakeWV{}
	b := &backend{w: f}
	stop := make(chan struct{})
	defer close(stop)
	go b.watchLocalServer(probeSpec{Modes: []string{"tcp"}, Addr: addr}, stop)

	// 端口开着 → 第一轮就该推 up=true
	if !waitEvent(f, []string{`"server"`, `"up":true`}, 3*time.Second) {
		t.Fatalf("端口开着时该推 up=true，实得：%s", f.snapshot())
	}

	// 把端口关掉 → 下一轮（3 秒后）该推 up=false，并往运行日志写一条提醒
	ln.Close()
	if !waitEvent(f, []string{`"server"`, `"up":false`}, 8*time.Second) {
		t.Fatalf("端口关掉后该推 up=false，实得：%s", f.snapshot())
	}
	if !waitEvent(f, []string{"没检测到游戏服务端"}, time.Second) {
		t.Errorf("没检测到时该往运行日志写一条提醒，实得：%s", f.snapshot())
	}
}
