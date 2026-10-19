// 用系统默认浏览器打开一个链接。
//
// 为什么不让页面自己 window.open：webview 里那个不一定被允许，而且真弹出来
// 也可能是 webview 自己的窗口（没有地址栏、没有后退，用户不知道该点哪）✗
package main

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procShellExecuteW = shell32.NewProc("ShellExecuteW")

// swShowNormal 是 ShellExecuteW 的最后一个参数：按正常大小显示。
const swShowNormal = 1

// openURL 打开浏览器。
//
// 🩸 只放行 https —— 这个地址是从网上拉回来的，明文 http 有可能被中间人
// 换成钓鱼页。卡在这儿，别把判断权交给上游 ✗
func openURL(u string) error {
	if !strings.HasPrefix(u, "https://") {
		return errors.New("只允许打开 https 链接")
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	// ShellExecuteW 返回 <= 32 就是失败（微软文档里的约定，它的返回值不是负数）
	r, _, _ := procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(target)),
		0, 0, swShowNormal)
	if r <= 32 {
		return errors.New("打不开浏览器")
	}
	return nil
}
