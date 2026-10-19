package main

import (
	"strings"
	"testing"
)

// judgeServer 决定"连不上服务器"时给什么建议。
//
// 🩸 这一档说错最伤：用户会以为软件坏了、去重装，而真正的原因是
//
//	他自己开着杀软/在校园网里 ✗
func TestJudgeServer(t *testing.T) {
	t.Run("一次都没连上", func(t *testing.T) {
		v, head, detail := judgeServer(nil)
		if v != "down" {
			t.Fatalf("应该判 down，实际 %s（%s）", v, head)
		}
		// 必须给"先换网络试试"这种可执行的动作
		if !strings.Contains(detail, "换") {
			t.Errorf("应该给出可执行的动作：%s", detail)
		}
		if !strings.Contains(detail, "防火墙") && !strings.Contains(detail, "杀毒") {
			t.Errorf("应该点出本机拦截这个常见原因：%s", detail)
		}
	})

	t.Run("正常延迟", func(t *testing.T) {
		v, head, _ := judgeServer([]int64{119, 40, 40})
		if v != "ok" {
			t.Fatalf("应该判 ok，实际 %s（%s）", v, head)
		}
		// 取的是**最小**值（119 那次是首次建连，慢一点很正常）
		if !strings.Contains(head, "40") {
			t.Errorf("应该报最快那次 40ms，实际标题：%s", head)
		}
	})

	t.Run("延迟偏高", func(t *testing.T) {
		v, head, detail := judgeServer([]int64{320, 280})
		if v != "slow" {
			t.Fatalf("应该判 slow，实际 %s", v)
		}
		if !strings.Contains(head, "280") {
			t.Errorf("应该报最快那次：%s", head)
		}
		// 别让人误以为"延迟高 = 游戏卡"
		if !strings.Contains(detail, "游戏") {
			t.Errorf("应该说明这跟游戏流畅度无关：%s", detail)
		}
	})

	t.Run("刚好压线 150 算偏高", func(t *testing.T) {
		if v, _, _ := judgeServer([]int64{150}); v != "slow" {
			t.Errorf("150ms 应该算 slow，实际 %s", v)
		}
	})
}

// judgeProxy 决定"要不要提醒用户关掉代理"。
//
// 🩸 最忌两种错：
//
//	· 明明有 VPN 接管流量却报"没问题" ⇒ 用户永远查不出来 ✗
//	· 只是装过没用却报"有 VPN" ⇒ 用户白折腾一场 ✗
func TestJudgeProxy(t *testing.T) {
	t.Run("干净", func(t *testing.T) {
		v, head, detail := judgeProxy("", "", "", nil)
		if v != "ok" {
			t.Fatalf("应该判 ok，实际 %s（%s）", v, head)
		}
		if !strings.Contains(detail, "不用管") {
			t.Errorf("干净时应该明确说「这页可以不管了」，别让人瞎猜：%s", detail)
		}
	})

	t.Run("有 VPN 网卡最优先", func(t *testing.T) {
		v, _, detail := judgeProxy("127.0.0.1:7890", "", "", []string{"TAP-Windows Adapter V9"})
		if v != "vpn" {
			t.Fatalf("有 VPN 网卡时应该判 vpn（比系统代理更严重），实际 %s", v)
		}
		if !strings.Contains(detail, "TAP-Windows") {
			t.Errorf("应该把具体网卡名写出来：%s", detail)
		}
		// 要教他"完全退出"，不是只关界面 —— 这类软件关窗口仍在后台代理
		if !strings.Contains(detail, "退出") {
			t.Errorf("应该说清要完全退出：%s", detail)
		}
	})

	t.Run("只有系统代理", func(t *testing.T) {
		v, _, detail := judgeProxy("127.0.0.1:7890", "", "", nil)
		if v != "proxy" {
			t.Fatalf("应该判 proxy，实际 %s", v)
		}
		if !strings.Contains(detail, "127.0.0.1:7890") {
			t.Errorf("应该把代理地址写出来：%s", detail)
		}
		// 别把话说死：代理不一定影响联机
		if !strings.Contains(detail, "不一定") {
			t.Errorf("不该一口咬定代理是元凶：%s", detail)
		}
	})

	t.Run("只有 PAC", func(t *testing.T) {
		if v, _, _ := judgeProxy("", "http://x/pac", "", nil); v != "proxy" {
			t.Errorf("配了 PAC 也应该判 proxy，实际 %s", v)
		}
	})

	t.Run("只有环境变量", func(t *testing.T) {
		if v, _, _ := judgeProxy("", "", "http://127.0.0.1:1080", nil); v != "proxy" {
			t.Errorf("环境变量代理也应该判 proxy，实际 %s", v)
		}
	})
}
