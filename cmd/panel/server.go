// 管理面板：注册 / 登录 / 后台。
//
// 它是**唯一**写账号库的进程（单写者约定，见 internal/auth 的包注释）；
// 穿透服务器只读地看着那两个文件。
//
// 两块东西共用一个 HTTP 服务，但面向的对象完全不同：
//
//	/api/…        —— 客户端（webview GUI）用的，凭据走 Authorization: Bearer
//	/api/admin/…  —— 浏览器用的，凭据走 HttpOnly cookie（所以要防 CSRF）
//
// 两条路的凭据其实是同一种东西（auth 里的会话令牌），只是搬运方式不同 ✓
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"p2plink/internal/auth"
	"p2plink/internal/netutil"
)

//go:embed web
var embeddedWeb embed.FS

// 请求体上限。注册/登录的 JSON 撑死几百字节；给 8KB 已经很宽松了。
//
// 🩸 不设上限就是一条"一条请求撑爆内存"的路子 —— 而登录接口是**公开**的 ✗
const maxBodyBytes = 8 << 10

type ctxKey int

const (
	ctxAccount ctxKey = iota
	ctxToken
)

// Server 是面板的全部状态。
type Server struct {
	st         *auth.Store
	domains    []string
	csrfKey    []byte
	signalSock string
	hc         *http.Client

	// now 抽成字段是为了测试能控制时间（限流窗口、会话过期都靠它）。
	now func() time.Time

	rlLoginIP    *limiter // 按 IP 限登录
	rlLoginEmail *limiter // 按邮箱限登录
	rlSignupIP   *limiter // 按 IP 限注册
	rlAdminIP    *limiter // 按 IP 限后台登录（门槛更高：这是服务器主人的门）
	rlSendCodeIP *limiter // 按 IP 限"要验证码"（防有人拿它刷我们的发信额度）

	// mailCodes 是邮箱验证码（**只在内存里**，见 mailcode.go）
	mailCodes *mailCodeStore

	// mail 是发信配置。零值 = 没配 ⇒ "验证码登录"整个不可用，
	// /api/sendcode 会直接说"还没开放"（**不假装发出去**）✓
	mail smtpConf
}

func newServer(st *auth.Store, domains []string, csrfKey []byte, signalSock string) *Server {
	return &Server{
		st:         st,
		domains:    domains,
		csrfKey:    csrfKey,
		signalSock: signalSock,
		// 调信令侧那个本机端点用的客户端。
		//
		// 🩸 **必须有超时**：不然信令侧卡住会把面板的请求一起挂死 ✗
		// 🩸 **必须走那个 Unix socket**：信令侧的本机端点已经不是 TCP 了，
		//    默认 Transport 会去连 signalHost 里的主机名 ⇒ 连不上 ✗
		hc: &http.Client{
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", signalSock)
				},
			},
		},
		now:          time.Now,
		rlLoginIP:    newLimiter(20, 15*time.Minute),
		rlLoginEmail: newLimiter(10, 15*time.Minute),
		rlSignupIP:   newLimiter(10, time.Hour),
		rlAdminIP:    newLimiter(10, 15*time.Minute),
		rlSendCodeIP: newLimiter(10, 15*time.Minute),
		// 验证码用的 HMAC 密钥跟 CSRF 共用那一把 —— 反正都是"服务端密钥"，
		// 再养一把只会多一个要备份的东西 ✓
		mailCodes: newMailCodeStore(csrfKey),
	}
}

// route 是一条路由。
//
// 📌 把它写成**表**而不是一堆散落的 mux.HandleFunc，是为了有测试能遍历它：
// "每一条后台接口，未登录时都必须是 401" —— 新加了接口却忘了挂鉴权，
// 那条测试会当场发现 ✓（表就是唯一来源，不会出现"测试里那份和实际那份不一样"）
type route struct {
	method  string
	pattern string
	h       http.HandlerFunc
	// admin=true 的会被塞进 /api/admin/ 那棵子树，**先过鉴权**；
	// POST 还会再套一层 CSRF 校验。
	admin bool
}

func (s *Server) routes() []route {
	return []route{
		// ---- 客户端（Bearer 令牌） ----
		{"POST", "/api/signup", s.apiSignup, false},
		{"POST", "/api/sendcode", s.apiSendCode, false},
		{"POST", "/api/login", s.apiLogin, false},
		{"POST", "/api/logout", s.apiLogout, false},
		{"GET", "/api/me", s.apiMe, false},
		{"POST", "/api/password", s.apiPassword, false},
		// 找回密码：不需要登录（就是登不上去才用的），靠邮箱验证码证明身份 ✓
		{"POST", "/api/reset", s.apiReset, false},
		// 注册模式：只回一个"要不要邀请码"，客户端拿它决定界面怎么写 ✓
		{"GET", "/api/signupmode", s.apiSignupMode, false},

		// ---- 后台（cookie） ----
		// 登录本身当然不能要求"已经登录了"，所以它是唯一一条非 admin 的后台路由。
		{"POST", "/api/admin/login", s.apiAdminLogin, false},
		{"POST", "/api/admin/logout", s.apiAdminLogout, true},
		{"GET", "/api/admin/session", s.apiAdminSession, true},
		{"GET", "/api/admin/codes", s.apiAdminCodes, true},
		{"POST", "/api/admin/codes", s.apiAdminGenCodes, true},
		{"GET", "/api/admin/codes.csv", s.apiAdminCodesCSV, true},
		{"POST", "/api/admin/codes/note", s.apiAdminSetNote, true},
		{"GET", "/api/admin/accounts", s.apiAdminAccounts, true},
		{"POST", "/api/admin/accounts/status", s.apiAdminSetStatus, true},
		{"POST", "/api/admin/accounts/revoke", s.apiAdminRevoke, true},
		{"GET", "/api/admin/rooms", s.apiAdminRooms, true},
		{"GET", "/api/admin/metrics", s.apiAdminMetrics, true},
		{"POST", "/api/admin/rooms/close", s.apiAdminRoomsClose, true},
		{"GET", "/api/admin/rooms/history", s.apiAdminRoomHistory, true},
		{"GET", "/api/admin/rooms/detail", s.apiAdminRoomDetail, true},
		{"POST", "/api/admin/rooms/kick", s.apiAdminRoomKick, true},
		{"GET", "/api/admin/stats", s.apiAdminStats, true},

		// 注册设置：GET 读、POST 写（POST 会自动套 CSRF ✓）—— 2026-10-17 加
		{"GET", "/api/admin/settings", s.apiAdminSettings, true},
		{"POST", "/api/admin/settings", s.apiAdminSetSettings, true},
	}
}

// Handler 组装出完整的 HTTP 处理链。
func (s *Server) Handler() http.Handler {
	web, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		log.Fatalf("面板静态资源打不开: %v", err)
	}

	mux := http.NewServeMux()
	// 后台页面本身**不鉴权**：它是一个空壳，一条数据都没有（数据全靠 /api/admin/* 取）。
	// 让空壳可见的好处是"没登录就显示登录框"这件事能由页面自己决定，服务端不用管。
	mux.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.Handle("/admin/", http.StripPrefix("/admin/", http.FileServerFS(web)))

	var admin []route
	for _, rt := range s.routes() {
		if rt.admin {
			admin = append(admin, rt)
			continue
		}
		mux.HandleFunc(rt.method+" "+rt.pattern, rt.h)
	}

	am := http.NewServeMux()
	for _, rt := range admin {
		h := rt.h
		if rt.method == "POST" {
			h = s.requireCSRF(h)
		}
		am.HandleFunc(rt.method+" "+rt.pattern, h)
	}
	// ⭐ 整棵 /api/admin/ 默认走鉴权。这样"以后新加一条接口忘了挂鉴权"
	// 这个最常见的失手，在这里**不可能发生** ✓
	// （上面的 /api/admin/login 更具体，ServeMux 会优先挑它 ✓）
	mux.Handle("/api/admin/", s.requireAdmin(am))

	return s.securityHeaders(s.recoverer(mux))
}

// ---- 中间件 ----

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// 🩸 CSP 里**没有** 'unsafe-inline'：页面不许有内联脚本，也就没有
		// "用户可控字符串被当代码执行"这条路。代价是 JS/CSS 必须放外部文件 ✓
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; "+
				"img-src 'self' data:; connect-src 'self'; form-action 'self'; "+
				"frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
		// HSTS 只在"用户那头确实是 https"时才发 —— 走反代时面板这头是明文，
		// 光看 r.TLS 会永远不发（见 isHTTPS 的注释）✓
		if isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		// 接口一律别缓存：这些都是"跟谁在登录"有关的响应，
		// 留在浏览器/中间层缓存里只会添乱 ✓
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// recoverer 兜住 panic：一个请求炸了不该把整个面板带走，也不该把堆栈漏给外面。
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("面板：处理 %s %s 时 panic: %v", r.Method, r.URL.Path, v)
				writeErr(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requireAdmin 是后台鉴权的**唯一**入口。
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := cookieToken(r)
		a, ok := s.st.VerifyToken(tok, s.now())
		if !ok || a.Role != auth.RoleAdmin {
			// 🩸 不区分"没登录"和"登录了但不是管理员" ——
			// 分开说等于告诉对方"这个号是存在的、只是权限不够" ✗
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		ctx := context.WithValue(r.Context(), ctxAccount, a)
		ctx = context.WithValue(ctx, ctxToken, tok)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireCSRF 只对 cookie 那套生效（Bearer 不带 cookie，天然没有 CSRF 问题）。
func (s *Server) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := cookieToken(r)
		if !s.csrfOK(r, tok) {
			writeErr(w, http.StatusForbidden, "请求校验失败，请刷新页面重试")
			return
		}
		next(w, r)
	}
}

// ---- 小工具 ----

func cookieToken(r *http.Request) string {
	c, err := r.Cookie(cookieSession)
	if err != nil {
		return ""
	}
	return c.Value
}

// bearerToken 从 Authorization: Bearer xxx 里取令牌（客户端走这条）。
func bearerToken(r *http.Request) string {
	v := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(v) <= len(p) || !strings.EqualFold(v[:len(p)], p) {
		return ""
	}
	return strings.TrimSpace(v[len(p):])
}

func accountFrom(r *http.Request) *auth.Account {
	a, _ := r.Context().Value(ctxAccount).(*auth.Account)
	return a
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error string `json:"error"`
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errBody{Error: msg})
}

// writeOK 是成功但没别的东西可回时的响应体。
// 故意回一个对象而不是空 body：JSON 解析器那边少一种特殊情况要处理 ✓
func writeOK(w http.ResponseWriter) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) }

// decodeJSON 读并解析请求体，**同时卡住大小**。
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	// 📌 **故意不开 DisallowUnknownFields**：多一个字段就报错的话，
	// 新客户端往老服务器发请求会直接失败 —— 而本项目的协议铁律是
	// "加字段一律 omitempty，老端只是忽略、不会报错"（见 internal/proto）✓
	// 这条规矩在 HTTP 这边也得守，不然同一件事两套脾气 ✓
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// 体里还有第二个 JSON 值 ⇒ 不收（同一条请求只该有一个意思）
	if dec.More() {
		return errors.New("请求体里有多余内容")
	}
	return nil
}

// logLine 打一行访问日志。
//
// 🩸 地址一律打码（跟 cmd/signal 一个规矩），路径**不带 query**
// （万一以后有人在 query 里塞令牌，这条规矩能兜住）。
func logLine(r *http.Request, code int, note string) {
	where := netutil.MaskAddr(r.RemoteAddr)
	if note == "" {
		log.Printf("面板 %s %s %s → %d", where, r.Method, r.URL.Path, code)
		return
	}
	log.Printf("面板 %s %s %s → %d（%s）", where, r.Method, r.URL.Path, code, note)
}

// drain 把剩下的请求体吃掉一点，好让连接能被复用。
// 顺手挡掉"发一半就不发了"的连接。
func drain(r *http.Request) {
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, maxBodyBytes))
}
