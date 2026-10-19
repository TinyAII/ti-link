// 「断线自动重连」的自检。
//
// 🩸 端到端那条路（真的断一次网再连回来）本机测不了 —— 要造一整套假信令服务器，
// 而且"断网"本身在回环上根本模拟不出来。所以这里只钉**能机械判定**的那几件：
//
//  1. 退避节奏对不对（太快会刷日志，太慢像卡死）
//  2. 会话句柄的取消 / 只报一次语义（错了会 panic 或者重连后响两声）
//  3. 三个事件名在 Go 和 JS 两边都对得上（漏一个界面就是哑的）
//
// 真机验收方式见 docs/使用说明 的排障一节：拔网线 20 秒再插回来，房间应该自己回来。
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"p2plink/internal/node"
)

// 退避必须是 2/4/8/16/20（封顶），而且**严格递增到封顶为止**。
//
// 反证方法：把 retryDelay 里的 `if d > retryMaxWait` 改成 `return d`
// ⇒ 第 5 次会变成 32 秒，这个测试立刻红 ✓
func TestRetryDelay(t *testing.T) {
	want := []time.Duration{
		2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 20 * time.Second, // 封顶
		20 * time.Second, // 再往后都是封顶，不能一直翻倍
	}
	for i, w := range want {
		if got := retryDelay(i + 1); got != w {
			t.Errorf("第 %d 次重连该等 %v，实际 %v", i+1, w, got)
		}
	}

	// 次数是"理论上限 5 次"，但就算传进来个离谱的数也不能溢出成负数/零
	// （<< 对 int 是会绕回的，绕成负数之后 time.After 直接闹钟立即响 ⇒ 疯狂重试）
	for _, n := range []int{0, -1, 40, 200} {
		if d := retryDelay(n); d <= 0 || d > retryMaxWait {
			t.Errorf("retryDelay(%d) = %v，越界了（必须落在 (0, %v]）", n, d, retryMaxWait)
		}
	}

	// 总时限得比"5 次退避之和"宽，否则还没退避完就被 deadline 掐了
	if total := 2 + 4 + 8 + 16 + 20; retryWindow < time.Duration(total)*time.Second {
		t.Errorf("retryWindow=%v 比退避总和 %ds 还短 —— 最后一次永远跑不到", retryWindow, total)
	}
}

// 取消必须幂等：用户连点两下「停止重连」不能 panic（close 两次同一个 channel）。
func TestJoinSessionCancel(t *testing.T) {
	ses := newJoinSession(joinParams{code: "TI000000AA"}, "127.0.0.1:25565", "", false)
	if ses.canceled() {
		t.Fatal("刚建的会话不该是已取消状态")
	}
	ses.cancel()
	if !ses.canceled() {
		t.Fatal("cancel 之后 canceled 应该是 true")
	}
	ses.cancel() // 第二下不能 panic
	ses.cancel()

	// 取消之后 stop 通道必须是关的 —— joinLoop 靠它从退避里醒过来
	select {
	case <-ses.stop:
	default:
		t.Error("stop 通道没关，joinLoop 会一直等到退避结束才退出")
	}
}

// 「已进房」只报一次：重连成功了不能再响一声提示音、也不能把页面又跳一遍。
//
// 反证方法：把 hello() 里的 `s.saidHi = true` 删掉 ⇒ 这个测试红 ✓
func TestJoinSessionHelloOnce(t *testing.T) {
	ses := newJoinSession(joinParams{}, "127.0.0.1:25565", "", false)
	if !ses.hello() {
		t.Error("第一次 hello 该返回 true（要进房间、要响声）")
	}
	for i := 0; i < 3; i++ {
		if ses.hello() {
			t.Errorf("第 %d 次 hello 又返回 true —— 重连会重复跳页/响铃", i+2)
		}
	}

	// 「重新连接」按钮那条路：会话一起来就是"已经进过房"的状态
	again := newJoinSession(joinParams{}, "127.0.0.1:25565", "", true)
	if again.hello() {
		t.Error("saidHi=true 的会话不该再报一次已进房")
	}
}

// 断线原因的翻译：界面上要显示"和穿透服务器的连接断了"这种话，
// 不是 `read tcp ...: i/o timeout`。
func TestBreakReason(t *testing.T) {
	cases := map[string]string{
		"等待房主超时或穿透服务器断开: read tcp: i/o timeout": "和穿透服务器的连接断了",
		"和穿透服务器的连接断了":                           "和穿透服务器的连接断了",
		"和房主的连接断了":                              "和房主的连接断了",
		"建立多路复用失败: broken pipe":                 "连接断了",
	}
	for in, want := range cases {
		if got := breakReason(errString(in)); got != want {
			t.Errorf("breakReason(%q) = %q，想要 %q", in, got, want)
		}
	}
	if got := breakReason(nil); got == "" {
		t.Error("breakReason(nil) 不能返回空串 —— 界面上会显示半句话")
	}
}

// errString 是个最小化的 error 实现，省得为测试引 errors.New 的包装链。
type errString string

func (e errString) Error() string { return string(e) }

// 「三种错误三样去处」—— joinLoop 全靠这条判据分派，非得钉死不可。
//
// 🩸 反证方法：把 classifyJoinEnd 里 `errors.Is(err, node.ErrStopped)` 那半句删掉
// ⇒ 「用户退出」那两条立刻变成 joinEndBreak（会去自动重连一个已经退掉的房间）✗
// 注意**别**拿"调换 Punch / Fatal 的判断顺序"当反证 —— 两者类型不同、
// errors.As 不会互相命中，换顺序结果一样，测不出东西来。
//
// 为什么值得专门测：这套分类错了的后果**不是报错，而是行为不对** ——
// 密码错的人被自动重试 5 遍（白等 50 秒）、或者被踢的人又被连回去（被踢两次）。
func TestClassifyJoinEnd(t *testing.T) {
	punch := &node.PunchError{Msg: "打洞没打通"}
	fatal := &node.FatalError{Msg: "房间密码不对"}

	cases := []struct {
		name string
		err  error
		want joinEnd
	}{
		{"正常结束", nil, joinEndStop},
		{"用户退出", node.ErrStopped, joinEndStop},
		{"用户退出（被包装过）", fmt.Errorf("收尾: %w", node.ErrStopped), joinEndStop},
		{"打洞不通", punch, joinEndPunch},
		{"打洞不通（被包装过）", fmt.Errorf("第 1 轮: %w", punch), joinEndPunch},
		{"密码不对", fatal, joinEndFatal},
		{"密码不对（被包装过）", fmt.Errorf("注册失败: %w", fatal), joinEndFatal},
		{"信令断了", errors.New("和穿透服务器的连接断了"), joinEndBreak},
		{"等房主超时", errors.New("等待房主超时或穿透服务器断开: i/o timeout"), joinEndBreak},
	}
	for _, c := range cases {
		if got := classifyJoinEnd(c.err); got != c.want {
			t.Errorf("%s：分成了 %v，想要 %v（%v）", c.name, got, c.want, c.err)
		}
	}

	// 打洞不通**不能**被当成"断了"：那样会自动重连，而重连九成还是打不通，
	// 用户白等五轮退避 ✗
	if classifyJoinEnd(punch) == joinEndBreak {
		t.Error("打洞不通被当成了「断了」—— 会白白自动重连 5 次")
	}
	// 反过来：密码错**不能**被当成"打洞不通"：那会把致命错误挂成"可以重试"的横幅，
	// 用户点了重连还是同样的错 ✗
	if classifyJoinEnd(fatal) == joinEndPunch {
		t.Error("密码不对被当成了「打洞不通」—— 横幅会让用户一直点重连")
	}
}

// 重连途中撞上"房间不存在"要改写文案 —— 那时候房间号多半是对的，
// 是房主那边也断了、房间散了。照原文案说"检查房间 ID"会让人白折腾 ✓
func TestFatalMsg(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		attempt int
		want    string
	}{
		{"第一次就房间不存在 → 原样（可能真打错了）", errString("房间不存在，检查一下房间 ID"), 0,
			"房间不存在，检查一下房间 ID"},
		{"重连途中房间不存在 → 改写成房主也断了", errString("房间不存在，检查一下房间 ID"), 2,
			"房间已经结束了 —— 多半是房主那边也断线了。让房主重新开一个房间再进。"},
		{"密码不对 → 原样", errString("房间密码不对"), 2, "房间密码不对"},
		{"被踢 → 原样", errString("已被中断: 房主将你踢出房间"), 1, "已被中断: 房主将你踢出房间"},
	}
	for _, c := range cases {
		if got := fatalMsg(c.err, c.attempt); got != c.want {
			t.Errorf("%s：fatalMsg = %q，想要 %q", c.name, got, c.want)
		}
	}
	if got := fatalMsg(nil, 3); got != "" {
		t.Errorf("fatalMsg(nil) = %q，该是空串", got)
	}
}

// 三个新事件必须**两边都真存在**。
//
// 🩸 这类"改动一边忘了另一边"的坑踩过（导出按钮：JS 按 'exportLog'+s 拼 id，
// HTML 早改成 exportLogRoom 了 ⇒ 按钮点了没反应，而且不报错 ✗）。
// 这里用最简单粗暴但有效的判据：Go 源码里 emit 过这个名字、拼好的页面里
// onEvent 也认这个名字 —— 少一边就红。
func TestReconnectEventWiring(t *testing.T) {
	be, err := os.ReadFile("backend.go")
	if err != nil {
		t.Fatalf("读不到 backend.go：%v", err)
	}
	page := buildHTML()

	events := []string{"reconnecting", "reconnected", "reconnectfail"}
	for _, ev := range events {
		if !strings.Contains(string(be), `b.emit("`+ev+`"`) {
			t.Errorf("backend.go 里没有 emit(%q) —— 前端等的那条事件永远不会来", ev)
		}
		// 前端 onEvent 里是按单引号比较的
		if !strings.Contains(page, `'`+ev+`'`) {
			t.Errorf("页面里没有处理 %q 的分支 —— 后端发了也没人接", ev)
		}
	}

	// 横幅的三个零件：只加 HTML 不接线 = 永远不出现；只写 JS 不摆元素 = 报错
	for _, id := range []string{"roomRetryBar", "roomRetryMsg", "btnCancelRetry", "roomFailTitle"} {
		if !strings.Contains(page, `id="`+id+`"`) {
			t.Errorf("页面里没有 #%s", id)
		}
		if !strings.Contains(page, `$('`+id+`')`) {
			t.Errorf("JS 里没引用 #%s", id)
		}
	}
}
