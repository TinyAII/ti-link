package main

import (
	"strings"
	"testing"
)

// judgeIPv6 决定用户看到哪句话、以及该去动哪一层设备。
//
// 🩸 这四档的顺序不能乱：说错了用户会去修错的东西 ——
//
//	比如明明是"网卡协议被关"，却让他去打电话找运营商 ✗
func TestJudgeIPv6(t *testing.T) {
	t.Run("协议被关：连链路本地都没有", func(t *testing.T) {
		v, _, detail := judgeIPv6(nil, false, false)
		if v != "off" {
			t.Fatalf("应该判 off，实际 %s", v)
		}
		if !strings.Contains(detail, "TCP/IPv6") {
			t.Errorf("应该直接告诉他去勾哪里：%s", detail)
		}
		// 这一档不该把人推给运营商
		if strings.Contains(detail, "运营商") {
			t.Errorf("协议没开时说运营商，会把人引错方向：%s", detail)
		}
	})

	t.Run("协议开着但没地址", func(t *testing.T) {
		v, head, detail := judgeIPv6(nil, true, false)
		if v != "no-address" {
			t.Fatalf("应该判 no-address，实际 %s（%s）", v, head)
		}
		if !strings.Contains(detail, "路由器") {
			t.Errorf("这一档该指向路由器/光猫：%s", detail)
		}
	})

	t.Run("有地址但出不去", func(t *testing.T) {
		addrs := []string{"2409:8a50:c31:4870:269:2dff:fef6:1b14"}
		v, _, detail := judgeIPv6(addrs, true, false)
		if v != "no-route" {
			t.Fatalf("应该判 no-route，实际 %s", v)
		}
		if !strings.Contains(detail, addrs[0]) {
			t.Errorf("应该把探到的地址写出来，方便对照：%s", detail)
		}
	})

	t.Run("能用", func(t *testing.T) {
		v, head, detail := judgeIPv6([]string{"2409:8a50:c31:4870:269:2dff:fef6:1b14"}, true, true)
		if v != "ok" {
			t.Fatalf("应该判 ok，实际 %s", v)
		}
		if !strings.Contains(detail, "不需要打洞") {
			t.Errorf("应该点明 v6 的好处（不走打洞），否则用户不知道这有什么用：%s", detail)
		}
		if head == "" {
			t.Error("结论标题不能为空")
		}
	})

	// 有地址但走不通、且链路本地也没了 —— 仍应判 no-route（地址是更强的证据）
	t.Run("有地址时以地址为准", func(t *testing.T) {
		if v, _, _ := judgeIPv6([]string{"240e::1"}, false, false); v != "no-route" {
			t.Errorf("有全局地址就该判 no-route，实际 %s", v)
		}
	})
}

// 🩸「没有 IPv6」不是故障，文案必须自己说清楚。
//
// 老板 2026-10-18 的原话：「ipv6不通别人以为就不能用我们的软件了，得写清楚」——
// 这不是文案偏好，是**产品风险**：绝大多数人本来就没有 IPv6，
// 界面上标红会直接把人劝退 ✗
func TestIPv6CopySaysOptional(t *testing.T) {
	cases := []struct {
		name   string
		global []string
		link   bool
		out    bool
	}{
		{"协议被关", nil, false, false},
		{"只有链路本地", nil, true, false},
		{"有地址但出不去", []string{"240e::1"}, true, false},
	}
	for _, c := range cases {
		_, head, detail := judgeIPv6(c.global, c.link, c.out)
		if strings.Contains(head, "不影响") || strings.Contains(detail, "不影响") ||
			strings.Contains(detail, "照样能连") {
			continue
		}
		t.Errorf("%s：标题和正文都没说「不影响使用」，用户会以为软件用不了\n  标题：%s\n  正文：%s",
			c.name, head, detail)
	}
}
