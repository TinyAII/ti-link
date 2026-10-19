// 取来访地址 / 判断 https —— 这两件事都是"放到反代后面会不会**悄悄**失效"的重灾区。
//
// 🩸 背景（2026-10-16）：面板要放到 `panel.tinyaii.cn` 的 nginx 后面。
// 那一刻，面板这头收到的每一条连接都来自 127.0.0.1 ⇒
//
//	· 限流：所有人共用一个键 ⇒ **一个人就能把所有人锁在门外** ✗
//	· HSTS：r.TLS 永远是 nil ⇒ 那道头再也发不出去 ✗
//
// 两条都不会报错，只会"安安静静地不管用了"，所以各钉一条测试 ✓
package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withHeaders 造一条"来自 remote、带着这些头"的请求。
func withHeaders(remote string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// hostOnly 去掉端口（拿它跟 clientIP 的结果比）。
func hostOnly(remote string) string {
	h, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return h
}

// ⭐⭐ 最要紧的一条：**直连时一个头都不许信**。
//
// 反证方法：把 clientIP 里那个 `if !isTrustedProxy(host) { return host }` 删掉，
// 这条立刻红 ✓
// 它红了意味着"攻击者换个假 IP 头就多一整份额度"，限流等于没有 ✗
func TestClientIPIgnoresHeadersWhenDirect(t *testing.T) {
	fake := map[string]string{
		"X-Real-IP":         "9.9.9.9",
		"X-Forwarded-For":   "9.9.9.9, 8.8.8.8",
		"X-Forwarded-Proto": "https",
	}
	for _, remote := range []string{"1.2.3.4:5678", "[2001:db8::1]:5678", "10.0.0.9:1234"} {
		if got := clientIP(withHeaders(remote, fake)); got != hostOnly(remote) {
			t.Errorf("直连（来自 %s）时该只认 RemoteAddr，实得 %q ✗ —— 伪造头能绕限流",
				remote, got)
		}
	}
}

// 经过本机反代：X-Real-IP 优先（nginx 用 proxy_set_header 覆盖它，客户端塞不进来）
func TestClientIPUsesRealIPBehindProxy(t *testing.T) {
	got := clientIP(withHeaders("127.0.0.1:5000", map[string]string{
		"X-Real-IP":       "203.0.113.7",
		"X-Forwarded-For": "1.1.1.1, 203.0.113.7",
	}))
	if got != "203.0.113.7" {
		t.Fatalf("该用 X-Real-IP，实得 %q", got)
	}
}

// ⭐ 没有 X-Real-IP 时退到 XFF 的**最后一跳** ——
// 前面那些是客户端能随便写的，只有末尾那个是 nginx 追加的真地址 ✓
func TestClientIPUsesLastForwardedHop(t *testing.T) {
	got := clientIP(withHeaders("127.0.0.1:5000", map[string]string{
		// 客户端伪造了前两跳；nginx 把自己看到的地址追加在最后
		"X-Forwarded-For": "6.6.6.6, 5.5.5.5, 203.0.113.9",
	}))
	if got != "203.0.113.9" {
		t.Fatalf("该取最后一跳，实得 %q ✗（取第一个就等于让客户端自己报 IP）", got)
	}
}

// 反代那边什么都没给 / 给的是垃圾 ⇒ 退回本机，**不是放行** ✓
func TestClientIPFallsBackToLoopback(t *testing.T) {
	for i, hdr := range []map[string]string{
		{},
		{"X-Real-IP": "不是IP"},
		{"X-Forwarded-For": "abc, def"},
		{"X-Real-IP": "", "X-Forwarded-For": ""},
	} {
		got := clientIP(withHeaders("127.0.0.1:5000", hdr))
		if got != "127.0.0.1" {
			t.Errorf("第 %d 种：头不对时该退回 127.0.0.1（当个普通键继续限流），实得 %q ✗", i, got)
		}
	}
}

func TestIsTrustedProxy(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "::1", "[::1]", "127.0.0.2"} {
		if !isTrustedProxy(h) {
			t.Errorf("%q 该算本机反代", h)
		}
	}
	for _, h := range []string{"1.2.3.4", "10.0.0.1", "192.168.1.1", "", "localhost", "127.0.0.1.evil.com"} {
		if isTrustedProxy(h) {
			t.Errorf("%q **不该**算受信代理（它不是本机！）✗", h)
		}
	}
}

// 端口会不会被漏进去（RemoteAddr 带端口，头里不带 —— 两边必须归一）
func TestClientIPShape(t *testing.T) {
	if got := clientIP(withHeaders("1.2.3.4:5678", nil)); strings.Contains(got, ":") {
		t.Errorf("直连的结果不该带端口：%q", got)
	}
	if got := clientIP(withHeaders("127.0.0.1:5000", map[string]string{"X-Real-IP": "1.2.3.4"})); got != "1.2.3.4" {
		t.Errorf("反代的结果该是头里的地址：%q", got)
	}
}

// ---- https 的判断 ----

func TestIsHTTPS(t *testing.T) {
	// ① 直连 + TLS ⇒ 是（本地 -dev-tls 就是这种）
	r1 := httptest.NewRequest("GET", "/admin/", nil)
	r1.TLS = &tls.ConnectionState{}
	if !isHTTPS(r1) {
		t.Error("直连带 TLS 该判成 https")
	}

	// ② 🩸 走反代时面板这头是**明文**（TLS 在 nginx 就终结了）⇒
	//    只看 r.TLS 的话 HSTS 永远发不出去 ✗
	r2 := httptest.NewRequest("GET", "/admin/", nil)
	r2.RemoteAddr = "127.0.0.1:5000"
	r2.Header.Set("X-Forwarded-Proto", "https")
	if !isHTTPS(r2) {
		t.Error("反代说是 https 就该判成 https（不然 HSTS 发不出去）✗")
	}

	// ③ 但**直连**时这个头是客户端写的，不许信 ✗
	r3 := httptest.NewRequest("GET", "/admin/", nil)
	r3.RemoteAddr = "1.2.3.4:5000"
	r3.Header.Set("X-Forwarded-Proto", "https")
	if isHTTPS(r3) {
		t.Error("直连时不许信 X-Forwarded-Proto ✗")
	}

	// ④ 反代说是 http / 没说 ⇒ 不是 https
	for i, hdr := range []string{"", "http"} {
		r := httptest.NewRequest("GET", "/admin/", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		if hdr != "" {
			r.Header.Set("X-Forwarded-Proto", hdr)
		}
		if isHTTPS(r) {
			t.Errorf("第 %d 种：不该判成 https", i)
		}
	}
}
