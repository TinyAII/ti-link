package main

import "testing"

// linkAllowed 是"页面能不能打开任意网站"的唯一闸门（见 settings_api.go 那段注释）。
// 这里把**该放行的**和**该拒的**都钉住 —— 尤其那几个看起来像但其实不是同一站的写法。
func TestLinkAllowed(t *testing.T) {
	allow := []string{
		"https://ti.tinyaii.cn/",
		"https://ti.tinyaii.cn/download",
		"https://qm.qq.com/q/RrUjoLACYi",
		"https://b23.tv/teKPE1I",
		"https://space.bilibili.com/123456",
		"https://www.bilibili.com/video/BV1xx",
	}
	for _, u := range allow {
		if !linkAllowed(u) {
			t.Errorf("该放行却被拒：%s", u)
		}
	}

	deny := []string{
		"", // 空
		// 冒充官网：前缀必须带着结尾的 /，光有域名不算
		"https://ti.tinyaii.cn.evil.com/",
		"https://ti.tinyaii.cnevil.com/",
		// 明文：能被中间人换成任意页面
		"http://ti.tinyaii.cn/",
		// 把白名单地址塞进路径里糊弄前缀匹配
		"https://evil.com/?r=https://ti.tinyaii.cn/",
		"https://evil.com/https://ti.tinyaii.cn/",
		// 别的协议
		"javascript:alert(1)",
		"file:///C:/Windows/System32/calc.exe",
		// 同域名但不同站（没说通配就不许）
		"https://panel.tinyaii.cn/",
		"https://mc.tinyaii.cn:39000/",
		// 大小写/空白绕过
		"HTTPS://ti.tinyaii.cn/",
		" https://ti.tinyaii.cn/ ",
	}
	for _, u := range deny {
		if linkAllowed(u) {
			t.Errorf("该拒却放行：%q", u)
		}
	}
}
