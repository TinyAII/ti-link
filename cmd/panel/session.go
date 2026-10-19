// 浏览器的会话载体：cookie + CSRF。
//
// 为什么后台用 cookie 而不是"前端拿 token 自己存"：
// cookie 可以设 **HttpOnly** ⇒ 脚本读不到它 ⇒ 就算哪天页面被注入了脚本，
// 也偷不走凭据 ✓ 代价是"浏览器会自动带上 cookie"，于是必须防 CSRF（见下）。
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"

	"p2plink/internal/auth"
)

// cookieSession 是登录凭据。
//
// 🩸 `__Host-` 前缀不是装饰：浏览器**强制**要求这种 cookie 必须是
// Secure + Path=/ + 不带 Domain。少了任何一条它直接不收。
// 这条前缀挡住的是"子域名往父域写 cookie"这类投毒手法 ✓
//
// ⚠️ 也正因为如此：**没有 HTTPS 的时候浏览器根本不会存它** ⇒
// 本地想看界面得用 `-dev-tls`（临时自签证书），明文起服务是登不进去的。
const cookieSession = "__Host-p2p_sess"

// headerCSRF 是前端把 CSRF 令牌放回来的地方。
const headerCSRF = "X-CSRF-Token"

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieSession,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(auth.SessionTTL / time.Second),
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieSession,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// csrfFor 由会话令牌算出一个 CSRF 令牌。
//
// 📌 为什么是"算出来"而不是"再存一份"：存一份就要动会话表（多一个字段、
// 多一处写盘），而 HMAC 的结果本身就绑定了那把会话令牌 ——
// 换个会话、或者伪造者不知道服务端密钥，都算不出同一个值 ✓
//
// 前提：**服务端密钥要稳定**（存盘，见 main.go 的 `-data`）。密钥变了，
// 所有已经打开的页面都要重新登录一次 —— 不影响安全，只是体验。
func (s *Server) csrfFor(token string) string {
	mac := hmac.New(sha256.New, s.csrfKey)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// csrfOK 校验前端回传的 CSRF 令牌。
//
// ⚠️ 用常数时间比较：逐个字节比会在"第一个不同的字节"提前返回，
// 理论上能一位一位试出正确值。隔着网络很难利用，但改起来就一行 —— 没理由留着。
func (s *Server) csrfOK(r *http.Request, token string) bool {
	got := r.Header.Get(headerCSRF)
	if got == "" {
		return false
	}
	return hmac.Equal([]byte(got), []byte(s.csrfFor(token)))
}

// clientIP 取来访者的地址，**限流和封禁就靠它**。
//
// 🩸🩸 分两种情况，判据是「这条连接是谁建的」——**不是**看请求头写了什么：
//
//	① 直连（RemoteAddr 不是回环）
//	   ⇒ 只信 RemoteAddr，**一个头都不看**。
//	   X-Real-IP / X-Forwarded-For 是客户端随便写的，信它等于把限流的钥匙
//	   直接递给攻击者：他每换一个假 IP 就多一整份额度 ✗
//
//	② 经过**本机的**反代（RemoteAddr 是回环）
//	   ⇒ 信 X-Real-IP；没有就退到 XFF 的**最后一跳**。
//	   为什么是最后一跳：nginx 的 `$proxy_add_x_forwarded_for` 会把
//	   「客户端自己塞的那串」原样保留、然后在**末尾追加**它看到的真实地址。
//	   所以前面的全可以伪造，**最后那个才是真的** ✓
//
// 📌 2026-10-16 部署时补的：面板要放到 panel.tinyaii.cn 的 nginx 后面。
// 不补这一步的话所有人看到的 RemoteAddr 都是 127.0.0.1 ⇒
// 一个IP的限流额度变成**全局共用**，随便一个人就能把所有人锁在门外 ✗
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isTrustedProxy(host) {
		return host
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		if ip := net.ParseIP(v); ip != nil {
			return ip.String()
		}
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		parts := strings.Split(v, ",")
		last := strings.TrimSpace(parts[len(parts)-1])
		if ip := net.ParseIP(last); ip != nil {
			return ip.String()
		}
	}
	// 头是空的 / 乱写的 ⇒ 退回本机。**别拿它当"没法限流就放行"** ——
	// 那样反倒成了绕过口子 ✓ 回环也算一个"键"，限流照常工作 ✓
	return host
}

// isTrustedProxy 判断"这条连接是不是本机反代建的"。
//
// 📌 只认回环：面板本身也只监听 127.0.0.1，所以 nginx 一定是从本机连过来的 ✓
// 要是哪天把面板直接开到公网，这条就自动变成 false（谁都不信）—— 正合期望 ✓
func isTrustedProxy(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// isHTTPS 判断"用户那头是不是走的 https"。
//
// 🩸 不能只看 r.TLS：放在 nginx 后面时，面板这头收到的是**明文**（TLS 在 nginx 就终结了）
// ⇒ r.TLS 永远是 nil ⇒ HSTS 那道头就再也发不出去了 ✗
// 所以再加一条：**本机反代**说它是 https（X-Forwarded-Proto）也算 ✓
// （同样只在受信反代的前提下才信这个头 ✓）
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !isTrustedProxy(hostOf(r.RemoteAddr)) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func hostOf(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
