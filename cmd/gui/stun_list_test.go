package main

import (
	"strings"
	"testing"
)

// checkStunList 守的是"别让用户把探测变慢"。
//
// 每个不通的 STUN 要白等 2.4 秒（2 次 × 1.2 秒）——
// 随手粘个网址进来，等于每次建房都多等几秒，比不改还糟 ✗
func TestCheckStunList(t *testing.T) {
	ok := []string{
		"stun.miwifi.com:3478",
		"stun.miwifi.com:3478,stun.hitv.com:3478",
		" 1.2.3.4:3478 , 5.6.7.8:3478 ", // 空格要能吃
		"[2409:8a50::1]:3478",           // v6 字面量
	}
	for _, v := range ok {
		if msg := checkStunList(v); msg != "" {
			t.Errorf("%q 该通过，却被拒：%s", v, msg)
		}
	}

	bad := []struct{ v, why string }{
		{"http://stun.example.com", "没有端口"},
		{"stun.miwifi.com", "没有端口"},
		{"stun.miwifi.com:abc", "端口不是数字"},
		{"stun.miwifi.com:99999", "端口超范围"},
		{":3478", "没有主机名"},
	}
	for _, c := range bad {
		if msg := checkStunList(c.v); msg == "" {
			t.Errorf("%q 该被拒（%s），却放行了", c.v, c.why)
		}
	}

	// 条数上限：太多等于每次都要白等好几轮
	tooMany := "a.com:3478,b.com:3478,c.com:3478,d.com:3478,e.com:3478,f.com:3478,g.com:3478"
	if msg := checkStunList(tooMany); !strings.Contains(msg, "最多") {
		t.Errorf("超过上限应该提示条数，实际：%q", msg)
	}
}
