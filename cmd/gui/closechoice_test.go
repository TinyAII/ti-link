// 「关窗口做什么」的测试。
//
// 🩸 这条是踩坑之后才有的，所以它盯得很死：
// 老板在设置里选了「退出主程序」，关窗却还是缩到托盘 ✗ ——
// 根因是关窗那条路（main.go 的 SetOnCloseIntercept）当时只读"本次开机记住的"，
// **压根没读设置** ✓ 现在只有一个来源 = 设置，这里就钉它 ✓
package main

import (
	"testing"

	"p2plink/internal/appdir"
)

// 隔离数据目录 —— 别写到用户真实的那份去 ✗
func isolateData(t *testing.T) {
	t.Helper()
	t.Setenv(appdir.EnvDir, t.TempDir())
}

// 改设置并把内存缓存清掉，让下次读真的走一遍盘 ✓
func setCloseAction(t *testing.T, v string) {
	t.Helper()
	if err := updateSettings(func(s *Settings) { s.CloseAction = v }); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	settingsMu.Lock()
	settingsC = nil
	settingsMu.Unlock()
}

// 核心回归：设置里选什么，关窗就做什么。
func TestCloseActionFollowsSetting(t *testing.T) {
	isolateData(t)

	for _, want := range []string{"quit", "tray", "ask", "quit"} {
		setCloseAction(t, want)
		if got := closeActionNow(); got != want {
			t.Errorf("设置成 %q，关窗该是 %q，实得 %q", want, want, got)
		}
	}
}

// 老配置里没这个字段（空串）、或者写了个乱值 ⇒ 一律当"问一下" ✓
// 宁可多问一句，也别擅自退出或者把窗口藏起来 ✗
func TestCloseActionUnknownFallsBackToAsk(t *testing.T) {
	isolateData(t)
	for _, v := range []string{"", "  ", "TRAY", "托盘", "whatever"} {
		setCloseAction(t, v)
		if got := closeActionNow(); got != "ask" {
			t.Errorf("值是 %q 时该退回 ask，实得 %q", v, got)
		}
	}
}

// 弹窗勾了「不再提醒」⇒ 答案要写进设置 ⇒ 以后关窗直接照做、不再弹 ✓
func TestRememberChoiceWritesSetting(t *testing.T) {
	isolateData(t)
	setCloseAction(t, "ask")

	if err := rememberCloseChoice("quit"); err != nil {
		t.Fatal(err)
	}
	settingsMu.Lock()
	settingsC = nil
	settingsMu.Unlock()

	if got := closeActionNow(); got != "quit" {
		t.Errorf("勾过「不再提醒」之后该按 quit 来，实得 %q", got)
	}
}
