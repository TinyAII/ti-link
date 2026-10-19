package main

import (
	"strings"
	"testing"
)

// judgeNat 是「网络自检」的判据。判错了会把人引到错误的方向：
// 明明是对方的问题，却让用户去折腾自己的网络 ✗
func TestJudgeNat(t *testing.T) {
	t.Run("端口一致=锥型", func(t *testing.T) {
		v, head, detail := judgeNat([]string{
			"120.227.55.90:12189",
			"120.227.55.90:12189",
			"120.227.55.90:12189",
		})
		if v != "cone" {
			t.Fatalf("应该判成 cone，实际 %s（%s）", v, head)
		}
		if !strings.Contains(detail, "12189") {
			t.Errorf("应该说清是哪个端口：%s", detail)
		}
		if !strings.Contains(detail, "对方") {
			t.Errorf("锥型时应该提示「再去查对方」，否则用户只会在自己这边瞎折腾：%s", detail)
		}
	})

	t.Run("端口不同=对称型", func(t *testing.T) {
		v, _, detail := judgeNat([]string{
			"120.227.55.90:12189",
			"120.227.55.90:12200", // 换个目标，映射端口就变了
			"120.227.55.90:12190",
		})
		if v != "symmetric" {
			t.Fatalf("应该判成 symmetric，实际 %s", v)
		}
		if !strings.Contains(detail, "换网络") {
			t.Errorf("对称型要给出可执行的动作：%s", detail)
		}
	})

	t.Run("一个都没通=被挡", func(t *testing.T) {
		v, _, _ := judgeNat(nil)
		if v != "blocked" {
			t.Errorf("应该判成 blocked，实际 %s", v)
		}
	})

	t.Run("只通一个=判不出来", func(t *testing.T) {
		v, _, _ := judgeNat([]string{"120.227.55.90:12189"})
		if v != "unknown" {
			t.Errorf("只通一个时不该硬判，实际 %s", v)
		}
	})

	// 🩸 关键一条：STUN 回的地址本身不是公网地址 ⇒ 运营商大内网。
	// 这时就算端口一致也不能说"你能打通" —— 那个地址别人根本访问不到 ✗
	t.Run("映射到100.64=运营商大内网", func(t *testing.T) {
		v, head, detail := judgeNat([]string{
			"100.64.12.7:40000",
			"100.64.12.7:40000",
		})
		if v != "cgnat" {
			t.Fatalf("100.64/10 应该判成 cgnat，实际 %s（%s）", v, head)
		}
		if !strings.Contains(detail, "100.64.12.7") {
			t.Errorf("应该把探到的地址写出来，方便用户求证：%s", detail)
		}
	})

	// 私网地址同理（有些网络 STUN 会回内网地址）
	t.Run("映射到私网地址=没有公网地址", func(t *testing.T) {
		v, _, _ := judgeNat([]string{"192.168.1.9:40000", "192.168.1.9:40000"})
		if v != "cgnat" {
			t.Errorf("私网映射不该判成 cone，实际 %s", v)
		}
	})
}

// 拆 host:port 要稳：不带端口、IPv6 形式都不能崩。
func TestNatHostPort(t *testing.T) {
	cases := []struct{ in, host, port string }{
		{"1.2.3.4:5678", "1.2.3.4", "5678"},
		{"1.2.3.4", "1.2.3.4", ""},
		{"[::1]:80", "::1", "80"},
	}
	for _, c := range cases {
		h, p := natHostPort(c.in)
		if h != c.host || p != c.port {
			t.Errorf("natHostPort(%q) = (%q,%q)，期望 (%q,%q)", c.in, h, p, c.host, c.port)
		}
	}
}
