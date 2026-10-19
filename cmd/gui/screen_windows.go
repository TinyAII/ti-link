// 窗口尺寸：按屏幕大小算，别再用写死的小尺寸。
package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procEnumWindows         = user32.NewProc("EnumWindows")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	// 单实例用：按标题找另一个实例的窗口（见 singleinstance_windows.go）
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
)

const (
	smCXScreen = 0 // SM_CXSCREEN
	smCYScreen = 1 // SM_CYSCREEN

	swHide = 0 // SW_HIDE
	swShow = 5 // SW_SHOW
)

// 窗口刚创建时用的是默认尺寸，SetSize 之后才变 —— 中间会闪一下小白框。
// 所以先藏起来，等尺寸和网页内容都就位了再显示。
func hideWindow(hwnd uintptr) { procShowWindow.Call(hwnd, swHide) }

// showWindow 把窗口亮出来并**带到最前面**。
//
// 🩸 光 ShowWindow(SW_SHOW) 是不够的：窗口只是"被别的窗口盖住"或"在后台"时，
// SW_SHOW 什么都不做 —— 用户点了托盘图标会以为"没反应"（页面其实切了，但他看不见）。
// 所以先 SW_RESTORE（最小化也能恢复），再抢一次前台。
func showWindow(hwnd uintptr) {
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
}

// hideOurTopWindows 把「本进程」所有可见的顶层窗口藏起来。
//
// 为什么需要它：webview 的 C++ 构造函数里是先 ShowWindow 再 embed（初始化 WebView2），
// 而 embed 要几百毫秒 —— 这段时间窗口就是一片白。等 New() 返回再藏已经晚了，
// 所以要在 New() 阻塞期间由另一个 goroutine 反复把它按住。
func hideOurTopWindows() {
	pid := windows.GetCurrentProcessId()
	cb := windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
		var wpid uint32
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&wpid)))
		if wpid != pid {
			return 1 // 不是我们的窗口，跳过
		}
		if vis, _, _ := procIsWindowVisible.Call(hwnd); vis != 0 {
			procShowWindow.Call(hwnd, swHide)
		}
		return 1
	})
	procEnumWindows.Call(cb, 0)
}

func screenSize() (int, int) {
	sw, _, _ := procGetSystemMetrics.Call(smCXScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if sw == 0 || sh == 0 {
		return 1280, 800 // 兜底
	}
	return int(sw), int(sh)
}

// windowSize 返回窗口该用多大：约屏幕的 60% 宽 / 88% 高，再夹到合理区间。
// 小屏幕上放得下，大屏幕上也不显得局促。
func windowSize() (int, int) {
	sw, sh := screenSize()

	w := sw * 60 / 100
	h := sh * 88 / 100

	if w < 900 {
		w = 900
	}
	if w > 1280 {
		w = 1280
	}
	if h < 660 {
		h = 660
	}
	if h > 960 {
		h = 960
	}

	// 无论如何别超出屏幕
	if w > sw-40 {
		w = sw - 40
	}
	if h > sh-80 {
		h = sh - 80
	}

	return w, h
}
