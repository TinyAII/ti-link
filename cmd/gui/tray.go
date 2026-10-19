// 系统托盘：启动就常驻。左键把窗口调出来，右键是菜单（联机 / 设置 / 退出）。
// 关窗口时是"藏起来接着跑"还是"真退出"，由 closechoice.go 那个弹窗决定。
package main

import (
	_ "embed"
	"sync"
	"sync/atomic"

	"fyne.io/systray"
)

// 托盘图标直接吃 exe 里那一份 —— 同一个 app.ico，不再单独画。
//
// 🩸 以前这里是在 Go 里手绘的（紫底圆 + 三个白点连线，约 100 行）。
// 结果 exe / 任务栏换成 TI 了，托盘还是那三个点，老板一眼就看出来"图标还是旧的" ✗
// 现在共用同一个源文件：换图标只改 app.ico，exe、任务栏、托盘三处一起跟着变 ✓
// （界面左侧的 logo 是 SVG，在 index.html 里，手画但同款）
//
//go:embed app.ico
var appIconICO []byte

var (
	trayMu      sync.Mutex
	trayStarted bool

	// trayReadyFlag 托盘是否真的建起来了。
	// 没建起来时不能拦窗口关闭 —— 否则窗口没了、托盘又没有，
	// 程序就变成谁都叫不回来的幽灵进程了。
	trayReadyFlag atomic.Bool

	trayShow     = make(chan struct{}, 1) // 左键：把窗口调出来
	trayOnline   = make(chan struct{}, 1) // 菜单「联机」
	traySettings = make(chan struct{}, 1) // 菜单「设置」
	trayQuit     = make(chan struct{}, 1) // 菜单「退出 TI 联机」
)

// ensureTray 按需创建托盘图标。
//
// 只会创建一次：systray 退出后不能再 Run，所以图标一旦建出来就常驻，
// 后面即使把设置关掉也不会再拆（拆不掉，留着也无害）。
func ensureTray() {
	trayMu.Lock()
	defer trayMu.Unlock()
	if trayStarted {
		return
	}
	trayStarted = true
	go systray.Run(trayReady, func() {})
}

func trayReady() {
	trayReadyFlag.Store(true)

	systray.SetIcon(appIconICO)
	systray.SetTitle("TI 联机")
	systray.SetTooltip("TI 联机 · 正在后台运行")

	// 左键 = 把窗口调出来。
	// 🩸 不设这个的话，左键会跟右键一样弹菜单 —— 库里是"谁没设回调谁弹菜单"。
	systray.SetOnTapped(func() { push(trayShow) })

	mOnline := systray.AddMenuItem("联机", "回到联机页")
	mSettings := systray.AddMenuItem("设置", "打开设置页")
	// 退出是"危险操作"，单独隔开一组，别跟日常操作挨着
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出 TI 联机", "完全退出程序")

	go func() {
		for {
			select {
			case <-mOnline.ClickedCh:
				push(trayOnline)
			case <-mSettings.ClickedCh:
				push(traySettings)
			case <-mQuit.ClickedCh:
				push(trayQuit)
			}
		}
	}()
}

// push 非阻塞地塞一个信号（重复点击只算一次）。
func push(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// trayExit 让托盘图标消失（程序真要退出时调用）。
func trayExit() { systray.Quit() }
