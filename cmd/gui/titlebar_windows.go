// 系统标题栏跟随主题。
//
// Windows 的标题栏默认是浅色的，压在深色界面上像贴了一条白胶布。
// 用 DWM 的属性把它染成和界面同色 —— 视觉上就等于"没有标题栏"，
// 但最小化 / 最大化 / 关闭、拖动、贴边这些系统行为全都还在。
package main

import (
	"unsafe"

	"github.com/webview/webview_go"
	"golang.org/x/sys/windows"
)

var (
	dwmapi                    = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
)

const (
	// DWMWA_USE_IMMERSIVE_DARK_MODE：窗口走深色（影响标题栏、右键菜单等）
	dwmwaUseImmersiveDarkMode = 20
	// DWMWA_WINDOW_CORNER_PREFERENCE：窗口圆角（Win11）
	dwmwaWindowCornerPreference = 33
	// DWMWA_BORDER_COLOR：窗口那圈 1px 描边 —— 深色界面下不改会显出一道白线
	dwmwaBorderColor = 34
	// DWMWA_CAPTION_COLOR / DWMWA_TEXT_COLOR：自定义标题栏底色与文字色（Win11）
	dwmwaCaptionColor = 35
	dwmwaTextColor    = 36

	// DWMWCP_ROUND
	dwmcpRound = 2

	// 恢复系统默认
	dwmColorDefault = 0xFFFFFFFF
)

// 和 style.css 里深色主题的 --appBg / --fg / --line 保持一致，
// 否则窗口边缘和界面之间会有色差，反而更显眼。
const (
	darkCaption = 0x171b30
	darkText    = 0xeceffa
	darkBorder  = 0x2f3556
)

// setWindowTheme 让窗口本身（圆角、描边、标题栏）跟着界面主题走。
// 属性不被支持时调用会失败，但只是"保持系统默认"，不影响功能。
func setWindowTheme(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	dwmSet(hwnd, dwmwaWindowCornerPreference, dwmcpRound)

	if !dark {
		dwmSet(hwnd, dwmwaUseImmersiveDarkMode, 0)
		dwmSet(hwnd, dwmwaBorderColor, dwmColorDefault)
		dwmSet(hwnd, dwmwaCaptionColor, dwmColorDefault)
		dwmSet(hwnd, dwmwaTextColor, dwmColorDefault)
		return
	}
	dwmSet(hwnd, dwmwaUseImmersiveDarkMode, 1)
	dwmSet(hwnd, dwmwaBorderColor, colorRef(darkBorder))
	dwmSet(hwnd, dwmwaCaptionColor, colorRef(darkCaption))
	dwmSet(hwnd, dwmwaTextColor, colorRef(darkText))
}

func dwmSet(hwnd uintptr, attr, value uint32) {
	v := value
	procDwmSetWindowAttribute.Call(hwnd, uintptr(attr),
		uintptr(unsafe.Pointer(&v)), unsafe.Sizeof(v))
}

// colorRef 拼 COLORREF —— 它是 0x00BBGGRR，**红在最低字节**，别写反。
func colorRef(rgb uint32) uint32 {
	r := (rgb >> 16) & 0xFF
	g := (rgb >> 8) & 0xFF
	b := rgb & 0xFF
	return r | g<<8 | b<<16
}

/* ==================== 无边框窗口 ==================== */

// 顶部标题栏的高度、右上角按钮区的宽度（像素）。
//
// 这两个数**页面和 WndProc 都要用**：页面按它们布局，
// WndProc 按它们判断"这一点算标题栏（可拖动）还是按钮（不能拖）"。
// ⚠️ 改 style.css 里的 .topbar 高度 / .winbtn 宽度时，这里要一起改。
const (
	captionHeight = 48
	buttonsWidth  = 92 // 2 个按钮 × 46（最小化 + 关闭，没有最大化）
)

const (
	swMinimize = 6 // SW_MINIMIZE
	swMaximize = 3 // SW_MAXIMIZE
	swRestore  = 9 // SW_RESTORE

	wmClose = 0x0010

	// GWL_STYLE = -16。注意不能直接 uintptr(gwlStyle) —— 无类型常量 -16 转 uintptr
	// 会在编译期报溢出，得先落成一个变量再转（运行时按补码走）。
	gwlStyle = -16

	wsMaximizeBox = 0x00010000

	swpNoMove       = 0x0002
	swpFrameChanged = 0x0020
)

var (
	procIsZoomed      = user32.NewProc("IsZoomed")
	procPostMessage   = user32.NewProc("PostMessageW")
	procGetWindowRect = user32.NewProc("GetWindowRect")
	procSetWindowPos  = user32.NewProc("SetWindowPos")

	procGetWindowLongPtr = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr = user32.NewProc("SetWindowLongPtrW")
)

// lockWindowSize 把窗口钉在固定大小上。
//
// 做法是去掉 WS_MAXIMIZEBOX：系统的"最大化"（双击标题栏、任务栏右键、
// 拖到屏幕顶端触发贴边）都会跟着失效。配合 SetSize 的 HintFixed
// （它把 min/max 尺寸设成同一个值），窗口就再也改不了大小了。
func lockWindowSize(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	idx := gwlStyle // 先落成变量，再转 uintptr（常量直接转会溢出）
	style, _, _ := procGetWindowLongPtr.Call(hwnd, uintptr(idx))
	procSetWindowLongPtr.Call(hwnd, uintptr(idx), style&^wsMaximizeBox)
	// 改完样式得让它重新算一次窗口框架，否则要等下次尺寸变化才生效
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNoMove|swpNoSize|swpNoZOrder|swpFrameChanged)
}

/* ---- 拖动窗口 ---- */

type rect struct{ L, T, R, B int32 }

const (
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

// DragMove 由页面调用：按住顶栏拖动时，按增量把窗口挪过去（单位：设备像素）。
//
// 🩸 为什么不走系统那套（SendMessage WM_NCLBUTTONDOWN + HTCAPTION，Electron 就这么干）：
// 它要求窗口**正处于前台**、鼠标处于**真实的按下上下文**，
// 在"无边框 + WebView2"这个组合下实测反复失败（消息直接返回，拖动循环根本不启动）。
// 而且鼠标事件全被 WebView2 子窗口吃掉，主窗口连 WM_NCHITTEST 都收不到。
//
// 自己按增量挪窗口：逻辑看得见、行为可预期，不挑前台、不依赖消息循环。
// 代价是没有系统的贴边（Aero Snap）—— 换来的是"一定能拖得动"。
func (b *backend) DragMove(dx, dy int) {
	if (dx == 0 && dy == 0) || b.hwnd == 0 {
		return
	}
	var r rect
	procGetWindowRect.Call(b.hwnd, uintptr(unsafe.Pointer(&r)))
	procSetWindowPos.Call(b.hwnd, 0,
		uintptr(int(r.L)+dx), uintptr(int(r.T)+dy),
		0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
}

func windowMinimize(hwnd uintptr) { procShowWindow.Call(hwnd, swMinimize) }

func windowMaximizeToggle(hwnd uintptr) {
	if windowIsMaximized(hwnd) {
		procShowWindow.Call(hwnd, swRestore)
		return
	}
	procShowWindow.Call(hwnd, swMaximize)
}

// windowClose 故意走 WM_CLOSE，而不是直接销毁窗口 ——
// 这样"关闭时最小化到托盘"那套拦截逻辑照样生效。
func windowClose(hwnd uintptr) { procPostMessage.Call(hwnd, wmClose, 0, 0) }

func windowIsMaximized(hwnd uintptr) bool {
	r, _, _ := procIsZoomed.Call(hwnd)
	return r != 0
}

/* ---- 暴露给页面 ---- */

// WinMinimize 由页面调用：最小化窗口。
func (b *backend) WinMinimize() { windowMinimize(b.hwnd) }

// WinMaxToggle 由页面调用：最大化 / 还原。返回切换后的状态。
func (b *backend) WinMaxToggle() bool {
	windowMaximizeToggle(b.hwnd)
	return windowIsMaximized(b.hwnd)
}

// WinClose 由页面调用：关窗口（跟点系统关闭按钮一个效果）。
func (b *backend) WinClose() { windowClose(b.hwnd) }

// WinIsMaximized 由页面调用：页面靠它决定右上角那个按钮画"最大化"还是"还原"。
func (b *backend) WinIsMaximized() bool { return windowIsMaximized(b.hwnd) }

// SetDragExclusion 由页面调用：告诉窗口"右上角这一块不能当拖动区"。
//
// 页面量完自己右侧那排按钮的实际宽度再报过来 —— 比两边各写死一个数字可靠
// （改主题按钮的文案、加删按钮，都不用回头同步 Go）。
func (b *backend) SetDragExclusion(width, height int) {
	if width < 0 {
		width = 0
	}
	if height <= 0 {
		height = captionHeight
	}
	webview.SetFrameless(true, height, width)
}

/* ==================== 窗口 / 任务栏图标 ==================== */

const (
	wmSetIcon = 0x0080

	iconSmallRes = 0 // ICON_SMALL：标题栏、任务栏小图
	iconBigRes   = 1 // ICON_BIG：Alt+Tab、任务栏大图、缩略图

	imageIcon = 1 // IMAGE_ICON
	lrShared  = 0x00008000

	// GetSystemMetrics 的索引
	smCXIcon   = 11
	smCYIcon   = 12
	smCXSmIcon = 49
	smCYSmIcon = 50

	// app.rc 里写的是 `1 ICON "app.ico"` —— 资源号就是 1，改 rc 的话这里要跟着改
	resAppIcon = 1
)

var (
	procLoadImage   = user32.NewProc("LoadImageW")
	procSendMessage = user32.NewProc("SendMessageW")
	// GetModuleHandle 在 x/sys/windows 这个版本里没有，自己挂 kernel32
	procGetModuleHandle = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

// setWindowIcon 把 exe 里嵌的图标挂到窗口上（任务栏、Alt+Tab、悬停缩略图都吃这个）。
//
// 🩸 为什么非要单独做这一步 —— **exe 里有图标 ≠ 窗口上有图标** ✗
// 2026-10-17 老板拿新编的 exe 一跑："图标怎么没变？还是那个旧的"。
// 查下来资源表是对的（7 层、256px 抽出来就是那张 TI），问题在建窗口那一行：
// third_party/webview_go/libs/webview/include/webview.h:3039
//
//	HICON icon = (HICON)LoadImage(hInstance, IDI_APPLICATION, IMAGE_ICON, ...);
//	wc.hIcon = icon;
//
// 它写死的是 IDI_APPLICATION（系统通用 ID，32512），**不是我们的资源号**。
// 我们 exe 里没这个 ID ⇒ LoadImage 返回 NULL ⇒ wc.hIcon = NULL
// ⇒ 窗口"没设图标" ⇒ 任务是栏画系统的默认空白图，资源表里躺着的 TI 没人看 ✓
//
// 这里改成按**我们自己的资源号 1** 加载，再用 WM_SETICON 交给窗口。
// 尺寸问系统要 SM_CXICON / SM_CXSMICON，让 Windows 自己在 7 层里挑合适那层 ✓
func setWindowIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	hInst, _, _ := procGetModuleHandle.Call(0)
	if hInst == 0 {
		return
	}
	set := func(idx uintptr, cx, cy uintptr) {
		h, _, _ := procLoadImage.Call(uintptr(hInst), resAppIcon, imageIcon,
			cx, cy, lrShared)
		if h != 0 {
			procSendMessage.Call(hwnd, wmSetIcon, idx, h)
		}
	}
	cx, _, _ := procGetSystemMetrics.Call(smCXIcon)
	cy, _, _ := procGetSystemMetrics.Call(smCYIcon)
	set(iconBigRes, cx, cy)
	cx, _, _ = procGetSystemMetrics.Call(smCXSmIcon)
	cy, _, _ = procGetSystemMetrics.Call(smCYSmIcon)
	set(iconSmallRes, cx, cy)
}
