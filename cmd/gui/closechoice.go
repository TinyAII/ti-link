// 「关窗口该做什么」的执行部分。
//
// 行为**完全由设置里的 CloseAction 决定**（定义见 settings.go）：
//
//	"tray" → 缩到托盘接着跑
//	"quit" → 真退出
//	"ask"  → 弹窗问（弹窗里勾「不再提醒」会把答案写回设置 ⇒ 以后就不问了）
//
// 决定"这次关窗走哪条"的地方在 main.go 的 SetOnCloseIntercept —— 那儿直接看设置 ✓
//
// 🩸 早先还有一套"本次开机记住选了什么"（close-choice.json + 比对开机时刻），
// 老板拍板改成"设置说了算 + 多一个「每次询问」项"之后它就多余了 ✓ 已撤掉 ✓
// 那时它闯的祸值得记：**在设置里改成"退出主程序"，关窗还是缩托盘** ✗ ——
// 因为关窗那条路只读了"本次开机记住的"，压根没读设置 ✓
package main

// applyClose 执行「关窗口」的两种结果之一。
func (b *backend) applyClose(action string) {
	switch action {
	case "tray":
		// 🩸 托盘没建起来就**不能藏** —— 窗口一藏、托盘又没有，
		// 程序就成了谁都叫不回来的幽灵进程。这种情况退回去真退出。
		if !trayReadyFlag.Load() {
			b.quitNow()
			return
		}
		// 只是藏起来接着跑 —— 正在联机的连接一点不受影响
		hideWindow(b.hwnd)
	case "quit":
		b.quitNow()
	}
}

// quitNow 真的退出：停联机、收托盘、关窗口。
func (b *backend) quitNow() {
	b.Stop()
	trayExit()
	b.w.Dispatch(func() { b.w.Terminate() })
}

// rememberCloseChoice 把用户在弹窗里的选择**记进设置**。
//
// 单独抽出来是为了能测：它跟"执行关窗"是两件事 ——
// 执行那半会去动托盘和窗口，测试里没法跑 ✓ 这半是纯设置读写 ✓
func rememberCloseChoice(action string) error {
	return updateSettings(func(st *Settings) { st.CloseAction = action })
}

// CloseChoice 由页面调用：用户在「关窗口」弹窗里点了按钮。
//
//	action:   "quit"（退出主程序）| "tray"（隐藏到托盘）
//	remember: 勾了「不再提醒」—— 那就把答案写进设置，以后关窗直接照做、不再问 ✓
//
// 返回错误文案，空 = 成功（跟其它页面方法保持一致）。
func (b *backend) CloseChoice(action string, remember bool) string {
	if action != "tray" && action != "quit" {
		return "不认识的选项：" + action
	}
	if remember {
		// 「不再提醒」= 表过态了 ⇒ 写进设置 ⇒ 下次关窗直接照做 ✓
		if err := rememberCloseChoice(action); err != nil {
			return "没记住这个选择：" + err.Error()
		}
	}
	b.applyClose(action)
	return ""
}
