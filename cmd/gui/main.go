// 图形客户端入口：建窗口、挂页面方法、管托盘与关闭行为。
//
// 其它几块在哪儿：
//
//	assets.go          —— 界面资源（index.html / style.css / app.js / 图片）拼接
//	backend.go         —— 联机逻辑桥接（建房 / 加房 / 踢 / 拉黑 / 停止）
//	settings_api.go    —— 设置页桥接
//	settings.go        —— 设置存取（JSON）
//	settings_windows.go—— 注册表自启、提示音
//	tray.go            —— 系统托盘 + 托盘图标
//	screen_windows.go  —— 窗口尺寸
package main

import (
	"flag"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/webview/webview_go"
)

// windowTitle 窗口标题。
// 📌 单实例那道关是**按标题找窗口**的（singleinstance_windows.go），
// 所以标题必须只有这一处定义 —— 别在别处再写一遍字面量，改了一处忘了另一处就找不到窗口了。
const windowTitle = "TI 联机"

func main() {
	// 🩸 单实例检查放在**最前面**（在 buildHTML 之前）：
	// 第二个实例该尽快下班，别白拼 1MB 的页面、更别去建窗口/托盘 ——
	// 那样会多一个托盘图标，用户关掉一个以为关干净了，另一个还在后台 ✗
	// 已经有实例在跑的话，它会先把那个窗口叫到前面来，再返回 false。
	if !acquireSingleInstance() {
		return
	}

	debug := flag.Bool("debug", false, "打开开发者工具（调试用）")
	flag.Parse()

	// 先把网页拼好 —— 这一步要处理 1MB+ 的 base64 图，比较慢，
	// 放到建窗口之前，免得窗口已经开了还在那儿空着。
	html := buildHTML()

	// 无边框：系统标题栏不要了，由页面自己画（顶部可拖动、右上角三个按钮）。
	// 必须在 New() 之前设置 —— 窗口样式在建窗口时就定死了。
	webview.SetFrameless(true, captionHeight, buttonsWidth)

	// 注意：third_party/webview_go 里打过补丁，窗口创建后不会自动显示，
	// 所以这里不会闪白框 —— 显示时机完全由下面的 showWindow 控制。
	w := webview.New(*debug)
	defer w.Destroy()

	hwnd := uintptr(w.Window())

	// 图标必须**显式**挂上去 —— 见 setWindowIcon 的注释：
	// webview 建窗口时写死用系统的 IDI_APPLICATION，不读我们 exe 里嵌的那个资源。
	// 少了这一句，任务栏 / Alt+Tab 就是系统的默认空白图，跟 app.ico 没关系 ✗
	setWindowIcon(hwnd)

	w.SetTitle(windowTitle)
	ww, wh := windowSize()
	// HintFixed：min/max 尺寸设成同一个值，窗口不可拖边缘改大小
	w.SetSize(ww, wh, webview.HintFixed)
	// 再去掉"可最大化" —— 这样双击标题栏、任务栏右键都最大化不了
	lockWindowSize(hwnd)

	b := &backend{w: w, hwnd: hwnd}
	bindAll(w, b)

	// 让窗口本身（圆角、描边）跟界面同色
	setWindowTheme(hwnd, loadTheme() == "dark")

	// 点窗口右上角的 X 时：**看设置里的 CloseAction** ✓
	//   tray/quit ⇒ 直接照做（不打扰）
	//   ask（以及老配置里没这个字段的情况）⇒ 拦住关闭、把弹窗叫出来
	// 用户选完会调 CloseChoice ✓
	//
	// 🩸 这里原来只读"本次开机记住的"、压根不看设置 ⇒ 在设置里改成"退出主程序"
	// 关窗还是缩托盘 ✗（老板踩到的）。现在只有一个来源：设置 ✓
	webview.SetOnCloseIntercept(func() bool {
		if a := closeActionNow(); a != "ask" {
			b.applyClose(a)
		} else {
			b.w.Dispatch(func() { b.w.Eval("showCloseDialog()") })
		}
		return true
	})

	// 托盘常驻：启动就建出来（左键调窗口、右键有菜单）
	ensureTray()
	go watchTray(w, hwnd, b)

	// 页面加载完会调这个，用它当"可以显示了"的信号
	ready := make(chan struct{})
	var once sync.Once
	w.Bind("__ready", func() { once.Do(func() { close(ready) }) })

	loadPage(w, html)

	go func() {
		select {
		case <-ready:
			// 页面已就绪，再给一帧时间让 WebView2 画出来
			time.Sleep(120 * time.Millisecond)
		case <-time.After(4 * time.Second):
			// 兜底：万一 __ready 没回调，也别一直藏着
		}
		w.Dispatch(func() { showWindow(hwnd) })
	}()

	// 启动时静默查一次更新（设置里开着才查）。
	//
	// 📌 两个"不做"：不阻塞启动、查不到就什么都不说 ——
	// 网络慢、或者更新源挂了，都不是用户的问题，别拿它烦人 ✗
	// 真查到新版本就把信息存下来，然后**主动推给页面**让它弹窗。
	if currentSettings().CheckUpdateStart {
		go func() {
			info, err := fetchLatest()
			if err != nil || !newerThan(appVersion, info.Version) {
				return
			}
			setPendingUpdate(info)

			// 🩸 查完得**主动叫页面一声** —— 不能指望页面初始化时正好来取：
			// 这一趟是后台跑的（几百毫秒到几秒），而页面加载完那一刻它多半还没回来
			// ⇒ 那样弹窗就永远不出来 ✗（"时灵时不灵"比压根不弹还糟）
			// 所以等页面就绪之后再叫它回查一次（见 06-settings.js 的 onStartupUpdate）。
			// ready 已经关掉的话 select 立刻就过，不会白等 ✓
			select {
			case <-ready:
			case <-time.After(5 * time.Second):
			}
			b.w.Dispatch(func() { b.w.Eval("onStartupUpdate()") })
		}()
	}

	w.Run()
	b.Stop()
}

// loadPage 把拼好的页面交给 WebView 显示。
//
// 🩸 **别改回 w.SetHtml()。** 它底下是 WebView2 的 NavigateToString，
// 要把整个 HTML 当**一个字符串**推过 COM 边界，这条路是**有容量上限**的
// —— 微软文档写着 2MB，并专门提醒「字符串里内联了别的资源时很容易超」，
// 而我们把 CSS / JS / 头像 / 教程图**全部** base64 内联在这一份 HTML 里。
//
// 实测（tmp/pageprobe，量的是转成 UTF-16 之后的体积）：
//
//	加头像之前 2.75 MB —— 能用
//	加头像之后 3.14 MB —— 白屏
//
// 所以真实阈值比文档那个 2MB 高、就卡在这两者之间。具体是多少不重要，
// 重要的是：**这条路本来就该换掉**，页面只会越来越大。
//
// 换成"先写一份到配置目录、再导航过去"：没有长度限制，
// 而且浏览器能直接从磁盘流式读，比把 1.6MB 字符串推过 COM 边界更快。
// 另外 ui.html 会留在配置目录里 —— 以后再出白屏，可以拿它直接对着看。
//
// 写不出来时退回 SetHtml：页面小的时候它没问题，总比什么都不显示强。
func loadPage(w webview.WebView, html string) {
	fallback := func() { w.SetHtml(html) }

	dir := configDir()
	if dir == "" {
		fallback()
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fallback()
		return
	}
	p := filepath.Join(dir, "ui.html")
	if err := os.WriteFile(p, []byte(html), 0o644); err != nil {
		fallback()
		return
	}
	w.Navigate("file:///" + filepath.ToSlash(p))
}

// closeActionNow 返回这次关窗该怎么做："tray" / "quit" / "ask"（弹窗问）。
//
// 抽成函数是为了能测 —— 它就是出过事的地方：
// 设置里选了「退出主程序」，关窗却还是缩到托盘（因为当时这条路只读"本次开机记住的"）✓
//
// 空值 / 老配置里没这个字段 / 乱值 ⇒ 一律当 "ask" ✓ 宁可多问一句，也别擅自退出或乱藏窗口 ✓
func closeActionNow() string {
	switch a := currentSettings().CloseAction; a {
	case "tray", "quit":
		return a
	default:
		return "ask"
	}
}

// bindAll 把后端方法挂到页面的 window 上。
// 全列在这儿 —— 页面能调什么，一眼看得完。
func bindAll(w webview.WebView, b *backend) {
	// 联机
	w.Bind("CreateRoom", b.CreateRoom)
	w.Bind("JoinRoom", b.JoinRoom)
	w.Bind("Kick", b.Kick)
	w.Bind("Ban", b.Ban)
	w.Bind("Stop", b.Stop)
	w.Bind("CloseRoom", b.CloseRoom)
	w.Bind("LeaveRoom", b.LeaveRoom)
	// 房间管理（房主）
	w.Bind("SetRoomPass", b.SetRoomPass)
	w.Bind("GetBanList", b.GetBanList)
	w.Bind("Unban", b.Unban)
	// 设置
	w.Bind("SaveTheme", b.SaveTheme)
	w.Bind("GetSettings", b.GetSettings)
	w.Bind("SetSetting", b.SetSetting)
	w.Bind("CheckUpdate", b.CheckUpdate)
	w.Bind("UpdateInfo", b.UpdateInfo)
	w.Bind("TakeStartupUpdate", b.TakeStartupUpdate)
	w.Bind("OpenDownloadPage", b.OpenDownloadPage)
	w.Bind("RememberGamePort", b.RememberGamePort)
	w.Bind("OpenConfigDir", b.OpenConfigDir)
	w.Bind("ChooseDataDir", b.ChooseDataDir)
	w.Bind("ResetDataDir", b.ResetDataDir)
	w.Bind("ResetSettings", b.ResetSettings)

	// 账号（登录 / 注册 / 退出）—— 见 auth_api.go
	w.Bind("AccountState", b.AccountState)
	w.Bind("VerifyAccount", b.VerifyAccount)
	w.Bind("Login", b.Login)
	w.Bind("SignUp", b.SignUp)
	w.Bind("LoginByCode", b.LoginByCode)
	w.Bind("SendCode", b.SendCode)
	w.Bind("SignupMode", b.SignupMode)
	w.Bind("OpenLink", b.OpenLink)
	w.Bind("ResetPassword", b.ResetPassword)
	w.Bind("Logout", b.Logout)
	w.Bind("SetLoginServer", b.SetLoginServer)
	// 关窗口弹窗（两个选择 + 「不再提醒」）
	w.Bind("CloseChoice", b.CloseChoice)
	// 窗口按钮（无边框模式下由页面自己画那三个按钮）
	w.Bind("WinMinimize", b.WinMinimize)
	w.Bind("WinMaxToggle", b.WinMaxToggle)
	w.Bind("WinClose", b.WinClose)
	w.Bind("WinIsMaximized", b.WinIsMaximized)
	w.Bind("SetDragExclusion", b.SetDragExclusion)
	w.Bind("DragMove", b.DragMove)
	// 日志导出
	w.Bind("ExportLog", b.ExportLog)
	w.Bind("NatCheck", b.NatCheck)       // 本机网络自检（能不能打洞）
	w.Bind("IPv6Check", b.IPv6Check)     // IPv6 直连自检（卡在哪一层）
	w.Bind("ServerCheck", b.ServerCheck) // 能不能连上穿透服务器
	w.Bind("ProxyCheck", b.ProxyCheck)   // 有没有代理/VPN 在干扰
	w.Bind("RetryJoin", b.RetryJoin)     // 打洞失败后「重新连接」（不清日志）
}

// watchTray 处理托盘：左键把窗口调出来；菜单里「联机 / 设置」顺带切到对应页。
func watchTray(w webview.WebView, hwnd uintptr, b *backend) {
	for {
		select {
		case <-trayShow:
			w.Dispatch(func() { showWindow(hwnd) })
		case <-trayOnline:
			w.Dispatch(func() {
				showWindow(hwnd)
				w.Eval("gotoLink()")
			})
		case <-traySettings:
			w.Dispatch(func() {
				showWindow(hwnd)
				w.Eval("gotoSettings()")
			})
		case <-trayQuit:
			b.Stop()
			trayExit()
			w.Dispatch(func() { w.Terminate() })
			return
		}
	}
}
