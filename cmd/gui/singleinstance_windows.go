// 单实例：同一个 Windows 用户只允许开一个。
//
// 为什么要有这道关（都是**用户看不懂**的那种毛病）：
//
//	· 两个实例 = **两个托盘图标 + 两个窗口**。用户关掉一个以为关干净了，
//	  另一个还在后台跑着 ✗
//	· 第二个实例加入房间时会撞端口（要监听 127.0.0.1:<同一个端口>），
//	  而报出来的是「本机端口用不了（多半已被游戏或其它程序占着）」——
//	  真实原因是**另一个自己**占了 ✗ 用户会跑去关游戏，白折腾一场。
//
// 🩸 关键：**绝不能静默退出**。第二个实例的正确行为是
// 「把已经开着的那个窗口叫到前面来，然后自己下班」——
// 静默退出会让用户以为程序没打开（点两下没反应），那比多开还难懂。
//
// 两道关配合：
//
//	① 命名互斥体 —— 真正的锁，挡住"双击两下、几乎同时启动"这种竞争
//	② 按标题找窗口 —— 找到就 showWindow（RESTORE + 置前，见 screen_windows.go）
package main

import (
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mutexName 带 Local\ 前缀 = **每个登录会话一个**。
// 换成 Global\ 的话，同一台机器上另一个 Windows 用户就开不了自己那份了 ✗
const mutexName = `Local\TI联机-single-instance`

// mutexHandle 拿到就**一直握着**（不 Close）—— 进程活着，锁就在。
var mutexHandle windows.Handle

// acquireSingleInstance 返回 true = 可以继续启动。
// 返回 false = 已经有一个在跑（那个窗口已经被叫到前面了），本进程应当直接退出。
func acquireSingleInstance() bool {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		return true // 建名字都失败（不该发生）：别拦着用户
	}
	h, err := windows.CreateMutex(nil, false, name)
	switch err {
	case nil:
		mutexHandle = h
		return true
	case windows.ERROR_ALREADY_EXISTS:
		focusExistingWindow()
		return false
	default:
		// 建锁本身出错：宁可让用户开（大不了多开），也别把人挡在外面 ✗
		return true
	}
}

// focusExistingWindow 把另一个实例的窗口叫到前面。
//
// 刚启动的那个可能**窗口还没建出来**，所以重试一会儿；
// 实在找不到也就算了 —— 总之本进程不再往下走（不然就是多开）。
func focusExistingWindow() {
	const tries = 25 // 约 2.5 秒
	for i := 0; i < tries; i++ {
		if hwnd := findWindowByTitle(windowTitle); hwnd != 0 {
			showWindow(hwnd)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// findWindowByTitle 找标题完全匹配的顶层窗口。
//
// 🩸 **不能只看"可见窗口"**：缩到托盘之后窗口是**隐藏**的（ShowWindow(SW_HIDE)），
// 一过滤掉就找不着 ⇒ 干等重试、然后静默退出 ⇒ 用户看到的是"双击了只转圈、没反应"。
// 标题匹配已经够精确了（就我们一家用「TI 联机」），不必再卡可见性。
//
// 这个函数只在"自己还没建窗口"的时候被调用（见 main.go 的顺序），
// 所以匹配到的一定是**另一个实例**的窗口，不会认错自己 ✓
func findWindowByTitle(title string) uintptr {
	var found uintptr
	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var buf [256]uint16
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 {
			return 1
		}
		if got := windows.UTF16ToString(buf[:n]); got == title {
			found = hwnd
			return 0 // 找到就停
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
	return found
}
