package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"p2plink/internal/auth"
)

// newTest 起一个内存里的面板。
//
// 🩸 必须用 **NewTLSServer**（不是 NewServer）：会话 cookie 带 `__Host-` 前缀，
// 浏览器/Go 的 cookie 罐子都要求它必须是 Secure ⇒ 明文 http 下**根本不会被存**，
// 于是所有"登录之后"的测试都会莫名其妙地 401 ✗（2026-10-15 本地踩到过这个坑。）
func newTest(t *testing.T) (*httptest.Server, *Server, *auth.Store) {
	t.Helper()
	st, err := auth.Open(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(st, auth.DefaultEmailDomains, []byte("test-csrf-key-0123456789abcdef"), "")
	ts := httptest.NewTLSServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv, st
}

// newClient 造一个带 cookie 罐子的客户端。
//
// 🩸 `ts.Client()` 返回的是**同一个缓存对象**（httptest 内部只建一次）。
// 直接在上面赋 Jar 的话，几个"不同客户端"其实是同一个 —— 后建的那个
// 会把先建的那个的 cookie 一起换掉，症状是"登录过却 401"，极难查 ✗
// （2026-10-15 我自己踩到过。）所以复制一份再改 ✓
func newClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := *ts.Client()
	c.Jar = jar
	return &c
}

func do(t *testing.T, c *http.Client, method, url string, body any, hdr map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp.StatusCode, out, resp.Header
}

// signUp 注册一个普通用户，返回 token。
//
// 🩸 注册现在要**邮箱验证码**（证明这个邮箱是他的）。测试里不连 SMTP，
// 而是直接调**真的那个 store** 拿一个码 —— 走的是产品代码，只跳过"发信"那一跳 ✓
// （发信协议本身另有测试，见 smtp_test.go 的假 SMTP 服务器）
func signUp(t *testing.T, ts *httptest.Server, srv *Server, st *auth.Store, email string) string {
	t.Helper()
	codes, err := st.GenCodes(1, "t", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ne := auth.NormalizeEmail(email)
	mc, why := srv.mailCodes.beginSend(ne, "127.0.0.1", srv.now())
	if why != "" {
		t.Fatalf("测试里拿验证码失败: %s", why)
	}
	body, _ := json.Marshal(map[string]string{
		"invite": codes[0], "email": email, "password": "goodpassword", "mailCode": mc,
	})
	resp, err := ts.Client().Post(ts.URL+"/api/signup", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("注册失败 %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.Token == "" {
		t.Fatal("注册没拿到 token")
	}
	return out.Token
}

// adminLogin 用管理员身份登录，返回带 cookie 的客户端和 CSRF 令牌。
func adminLogin(t *testing.T, ts *httptest.Server, st *auth.Store) (*http.Client, string) {
	t.Helper()
	if _, err := st.AddAdmin("boss@qq.com", "bossstrongpass", auth.DefaultEmailDomains, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, ts)
	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/login",
		map[string]string{"email": "boss@qq.com", "password": "bossstrongpass"}, nil)
	if code != http.StatusOK {
		t.Fatalf("后台登录失败 %d: %v", code, out)
	}
	csrf, _ := out["csrf"].(string)
	if csrf == "" {
		t.Fatal("登录响应里没有 csrf")
	}
	return c, csrf
}

// ⭐⭐ 这张表是本文件里最要紧的一条测试：**每一条后台接口，没登录都必须 401**。
//
// 它遍历的是**生产代码里的那张路由表**（srv.routes()），不是测试里另抄一份 ——
// 所以以后新加一条后台接口，"忘了挂鉴权"这件事在这里一定会被撞出来 ✓
//
// 🩸 而且它还顺手挡了两个假通过：路由表空了 / 模式写错了（404）。
func TestAllAdminRoutesRequireAuth(t *testing.T) {
	ts, srv, _ := newTest(t)
	c := newClient(t, ts)

	nAdmin := 0
	for _, rt := range srv.routes() {
		// 先确认这条路由是**真的挂上去了**：不管鉴权怎么样，都不该是 404
		code, _, _ := do(t, c, rt.method, ts.URL+rt.pattern, nil, nil)
		if code == http.StatusNotFound {
			t.Errorf("%s %s 是 404 —— 路由没挂上去（模式写错了？）", rt.method, rt.pattern)
			continue
		}
		if !rt.admin {
			continue
		}
		nAdmin++
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s 没登录却回了 %d —— 该是 401 ✗", rt.method, rt.pattern, code)
		}
	}
	if nAdmin < 5 {
		t.Fatalf("只找到 %d 条后台路由，太少了 —— 路由表是不是没建起来？", nAdmin)
	}
}

// 登录了、但**不是管理员**，后台一样进不去。
func TestAdminRejectsNormalUser(t *testing.T) {
	ts, srv, st := newTest(t)
	tok := signUp(t, ts, srv, st, "user@qq.com")

	c := newClient(t, ts)
	// 手动把普通用户的令牌塞进 cookie 里，冒充"已登录的浏览器"
	req, _ := http.NewRequest("GET", ts.URL+"/api/admin/codes", nil)
	req.AddCookie(&http.Cookie{Name: cookieSession, Value: tok})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("普通用户访问后台该 401，实得 %d ✗", resp.StatusCode)
	}
}

// 拿普通用户去后台登录接口，也必须失败 —— 而且会话不能留着。
func TestAdminLoginRejectsNormalUser(t *testing.T) {
	ts, srv, st := newTest(t)
	signUp(t, ts, srv, st, "user@qq.com")

	c := newClient(t, ts)
	code, _, hdr := do(t, c, "POST", ts.URL+"/api/admin/login",
		map[string]string{"email": "user@qq.com", "password": "goodpassword"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("普通用户登后台该 401，实得 %d ✗", code)
	}
	// 也不能给他种 cookie
	if len(hdr.Values("Set-Cookie")) > 0 {
		t.Errorf("失败的登录不该下发 cookie：%v", hdr.Values("Set-Cookie"))
	}
}

func TestSignupAndMe(t *testing.T) {
	ts, srv, st := newTest(t)
	tok := signUp(t, ts, srv, st, "a@qq.com")

	c := newClient(t, ts)
	code, out, _ := do(t, c, "GET", ts.URL+"/api/me", nil,
		map[string]string{"Authorization": "Bearer " + tok})
	if code != http.StatusOK {
		t.Fatalf("/api/me 该 200，实得 %d", code)
	}
	if out["email"] != "a@qq.com" {
		t.Errorf("邮箱对不上：%v", out["email"])
	}
	if out["role"] != auth.RoleUser {
		t.Errorf("角色该是普通用户：%v", out["role"])
	}
	// 令牌不对就该 401
	for _, bad := range []string{"", "garbage", tok + "x"} {
		code, _, _ := do(t, c, "GET", ts.URL+"/api/me", nil,
			map[string]string{"Authorization": "Bearer " + bad})
		if code != http.StatusUnauthorized {
			t.Errorf("令牌 %q 该 401，实得 %d ✗", bad, code)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	ts, srv, st := newTest(t)
	signUp(t, ts, srv, st, "a@qq.com")

	c := newClient(t, ts)
	code, out, _ := do(t, c, "POST", ts.URL+"/api/login",
		map[string]string{"email": "A@QQ.com", "password": "goodpassword"}, nil)
	if code != http.StatusOK {
		t.Fatalf("登录该 200，实得 %d: %v", code, out)
	}
	tok, _ := out["token"].(string)
	if tok == "" {
		t.Fatal("没发 token")
	}
	// 密码错
	code, out, _ = do(t, c, "POST", ts.URL+"/api/login",
		map[string]string{"email": "a@qq.com", "password": "wrongpassword"}, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("密码错该 401，实得 %d: %v", code, out)
	}
	// 注销之后令牌作废
	code, _, _ = do(t, c, "POST", ts.URL+"/api/logout", nil,
		map[string]string{"Authorization": "Bearer " + tok})
	if code != http.StatusOK {
		t.Fatalf("注销该 200，实得 %d", code)
	}
	code, _, _ = do(t, c, "GET", ts.URL+"/api/me", nil,
		map[string]string{"Authorization": "Bearer " + tok})
	if code != http.StatusUnauthorized {
		t.Fatalf("注销后该 401，实得 %d ✗", code)
	}
}

// ⭐ cookie 的属性一条都不能少 —— 少了任何一条都是一种具体的攻击面。
func TestSessionCookieAttributes(t *testing.T) {
	ts, _, st := newTest(t)
	if _, err := st.AddAdmin("boss@qq.com", "bossstrongpass", auth.DefaultEmailDomains, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, ts)
	_, _, hdr := do(t, c, "POST", ts.URL+"/api/admin/login",
		map[string]string{"email": "boss@qq.com", "password": "bossstrongpass"}, nil)

	sc := strings.Join(hdr.Values("Set-Cookie"), "\n")
	for _, want := range []string{
		cookieSession + "=", // __Host- 前缀（防子域名投毒）
		"HttpOnly",          // 脚本读不到（防 XSS 偷凭据）
		"Secure",            // 只在 https 上走（防明文降级）
		"SameSite=Strict",   // 跨站不带（防 CSRF 的第一道）
		"Path=/",            // __Host- 强制要求
	} {
		if !strings.Contains(sc, want) {
			t.Errorf("会话 cookie 少了 %q：\n%s", want, sc)
		}
	}
	if strings.Contains(sc, "Domain=") {
		t.Errorf("__Host- cookie 不能带 Domain：\n%s", sc)
	}
}

// CSP 里**不许**有 unsafe-inline：有它的话，"用户可控字符串被当代码跑"这条路就通了。
func TestSecurityHeaders(t *testing.T) {
	ts, _, _ := newTest(t)
	code, _, hdr := do(t, ts.Client(), "GET", ts.URL+"/admin/", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("/admin/ 该 200，实得 %d", code)
	}
	csp := hdr.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("没有 CSP")
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*"} {
		if strings.Contains(csp, bad) {
			t.Errorf("CSP 里不该出现 %q：%s", bad, csp)
		}
	}
	for _, want := range []string{"frame-ancestors 'none'", "base-uri 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP 少了 %q：%s", want, csp)
		}
	}
	if got := hdr.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("少了 nosniff：%q", got)
	}
	if got := hdr.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options 该 DENY：%q", got)
	}
}

// ⭐ CSRF：后台的**写**操作，没有正确的令牌一律 403。
func TestAdminWritesNeedCSRF(t *testing.T) {
	ts, _, st := newTest(t)
	c, csrf := adminLogin(t, ts, st)

	body := map[string]any{"n": 1, "batch": "", "note": ""}

	// ① 完全不带头
	code, _, _ := do(t, c, "POST", ts.URL+"/api/admin/codes", body, nil)
	if code != http.StatusForbidden {
		t.Fatalf("没有 CSRF 头该 403，实得 %d ✗", code)
	}
	// ② 带个错的
	code, _, _ = do(t, c, "POST", ts.URL+"/api/admin/codes", body,
		map[string]string{headerCSRF: "deadbeef"})
	if code != http.StatusForbidden {
		t.Fatalf("CSRF 头不对该 403，实得 %d ✗", code)
	}
	// ③ 带对的
	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/codes", body,
		map[string]string{headerCSRF: csrf})
	if code != http.StatusOK {
		t.Fatalf("CSRF 头对却没过：%d %v ✗", code, out)
	}

	// 只读的 GET 不需要 CSRF（它不改任何东西）
	code, _, _ = do(t, c, "GET", ts.URL+"/api/admin/codes", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("GET 不该要 CSRF：%d", code)
	}
}

// 别人的会话令牌算出来的 CSRF 值，不能拿来用（这就是"绑定"的意思）。
func TestCSRFIsBoundToSession(t *testing.T) {
	ts, srv, st := newTest(t)
	c, csrf := adminLogin(t, ts, st)

	other := srv.csrfFor("some-other-token")
	if other == csrf {
		t.Fatal("两个不同会话算出了同一个 CSRF 值 ✗")
	}
	code, _, _ := do(t, c, "POST", ts.URL+"/api/admin/codes",
		map[string]any{"n": 1}, map[string]string{headerCSRF: other})
	if code != http.StatusForbidden {
		t.Fatalf("拿别的会话的 CSRF 该 403，实得 %d ✗", code)
	}
}

// ⭐ 按 IP 限流：拿脚本猛试密码，试到一定次数就得被挡下来。
//
// ⚠️ 必须先把**按邮箱**那道关掉。不关的话，同一个邮箱试到第 10 次就会被
// 邮箱那道拦下，测试看到一个 429 就满意地绿了 —— 而它其实**根本没测到按 IP 这道**
// （2026-10-15 反证时发现：把按 IP 那道完全关掉，这条测试照样绿 ✗）
func TestLoginRateLimited(t *testing.T) {
	ts, srv, st := newTest(t)
	signUp(t, ts, srv, st, "a@qq.com")
	// ⚠️ 必须先把**按邮箱**那道关掉。不关的话，同一个邮箱试到第 10 次就会被
	// 邮箱那道拦下，测试看到一个 429 就满意地绿了 —— 而它其实**根本没测到按 IP 这道**
	// （2026-10-15 反证时发现：把按 IP 那道完全关掉，这条测试照样绿 ✗）
	srv.rlLoginEmail = newLimiter(1_000_000, 15*time.Minute)
	// 🩸 按 IP 那道也换成**不依赖真实时间**的参数（小限额 + 长窗口）。
	// 原来用的是生产参数（20 次 / 15 分钟）：判据里就掺进了"这 23 次请求有没有
	// 在窗口内跑完"这种跟机器忙不忙有关的东西 ⇒ **偶发** ✗
	// （2026-10-16 全量跑时红过一次，单跑三次都过 —— 它测的是"机制"，不该看时钟）
	srv.rlLoginIP = newLimiter(5, time.Hour)

	c := newClient(t, ts)
	limit := srv.rlLoginIP.limit
	blockedAt := -1
	var codes []int
	for i := 0; i < limit+3; i++ {
		code, _, _ := do(t, c, "POST", ts.URL+"/api/login",
			map[string]string{"email": "a@qq.com", "password": "wrongpassword"}, nil)
		codes = append(codes, code)
		if code == http.StatusTooManyRequests {
			blockedAt = i
			break
		}
	}
	if blockedAt < 0 {
		// 🩸 **把实得的码报出来** —— 只说"没被限流"定位不了任何东西：
		// 可能是请求没走到限流那一步、可能返回了别的错、也可能真没限住。
		// （2026-10-16 就是吃了这个亏：偶发红了一次，手上只有"23 次都没被限流"✗）
		t.Fatalf("连着试 %d 次都没被限流 ✗；每次实得：%v（期望：试错是 401，被挡是 429）",
			limit+3, codes)
	}
	if blockedAt > limit {
		t.Errorf("第 %d 次就被挡了，比设定的 %d 还早 —— 参数不对", blockedAt+1, limit)
	}
}

// 限流按**邮箱**也要有一道：换个 IP（宿舍/机房出口一大把）也躲不过。
func TestLoginRateLimitedPerEmail(t *testing.T) {
	ts, srv, st := newTest(t)
	signUp(t, ts, srv, st, "a@qq.com")

	// 把按 IP 的那道调大，逼出按邮箱的那道
	srv.rlLoginIP = newLimiter(1000, 15*time.Minute)
	c := newClient(t, ts)
	limit := srv.rlLoginEmail.limit
	got := false
	for i := 0; i < limit+3; i++ {
		code, _, _ := do(t, c, "POST", ts.URL+"/api/login",
			map[string]string{"email": "a@qq.com", "password": "wrongpassword"}, nil)
		if code == http.StatusTooManyRequests {
			got = true
			break
		}
	}
	if !got {
		t.Fatal("按邮箱的限流没生效 ✗")
	}
}

// 登录成功要把失败次数销掉，否则正常用户会被自己的历史拖累。
func TestLoginSuccessClearsRateLimit(t *testing.T) {
	ts, srv, st := newTest(t)
	signUp(t, ts, srv, st, "a@qq.com")
	c := newClient(t, ts)

	// 先错几次（但别到上限）
	for i := 0; i < srv.rlLoginEmail.limit-2; i++ {
		do(t, c, "POST", ts.URL+"/api/login",
			map[string]string{"email": "a@qq.com", "password": "wrongpassword"}, nil)
	}
	code, _, _ := do(t, c, "POST", ts.URL+"/api/login",
		map[string]string{"email": "a@qq.com", "password": "goodpassword"}, nil)
	if code != http.StatusOK {
		t.Fatalf("前面错了 %d 次之后正确的密码该能登，实得 %d", srv.rlLoginEmail.limit-2, code)
	}
	// 再错几次，应该重新有额度
	code, _, _ = do(t, c, "POST", ts.URL+"/api/login",
		map[string]string{"email": "a@qq.com", "password": "wrongpassword"}, nil)
	if code == http.StatusTooManyRequests {
		t.Fatal("登录成功没把失败计数清掉 ✗")
	}
}

func TestSignupRateLimited(t *testing.T) {
	ts, srv, _ := newTest(t)
	c := newClient(t, ts)
	got := false
	for i := 0; i < srv.rlSignupIP.limit+3; i++ {
		code, _, _ := do(t, c, "POST", ts.URL+"/api/signup",
			map[string]string{"code": "AAAAAAAAAA", "email": "a@qq.com", "password": "goodpassword"}, nil)
		if code == http.StatusTooManyRequests {
			got = true
			break
		}
	}
	if !got {
		t.Fatal("注册接口没限流 ✗")
	}
}

// 请求体超过上限：该被干净地拒掉（400），而不是把服务打爆或回 500。
func TestBodyTooLarge(t *testing.T) {
	ts, _, _ := newTest(t)
	c := newClient(t, ts)
	big := map[string]string{
		"email":    "a@qq.com",
		"password": strings.Repeat("x", maxBodyBytes*2),
	}
	code, _, _ := do(t, c, "POST", ts.URL+"/api/login", big, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("超大请求体该 400，实得 %d", code)
	}
}

// 方法不对 ⇒ 405（这靠的是 ServeMux 的方法模式，别哪天把它去掉了）。
func TestMethodNotAllowed(t *testing.T) {
	ts, _, _ := newTest(t)
	c := ts.Client()
	code, _, _ := do(t, c, "GET", ts.URL+"/api/login", nil, nil)
	if code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/login 该 405，实得 %d", code)
	}
}

// ── 邀请码 / 导出 ────────────────────────────────────────

func TestGenCodesAndCSV(t *testing.T) {
	ts, _, st := newTest(t)
	c, csrf := adminLogin(t, ts, st)

	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/codes",
		map[string]any{"n": 3, "batch": "第一批", "note": "=1+1 公式注入试试\n换行也试试"},
		map[string]string{headerCSRF: csrf})
	if code != http.StatusOK {
		t.Fatalf("发码失败：%d %v", code, out)
	}
	list, _ := out["codes"].([]any)
	if len(list) != 3 {
		t.Fatalf("该回 3 个码，实得 %d", len(list))
	}

	// 列表里应该能看到（而且码是带连字符给人看的形式）
	code, out, _ = do(t, c, "GET", ts.URL+"/api/admin/codes", nil, nil)
	if code != http.StatusOK {
		t.Fatal("取列表失败")
	}
	rows, _ := out["codes"].([]any)
	if len(rows) != 3 {
		t.Fatalf("列表该有 3 条，实得 %d", len(rows))
	}
	first, _ := rows[0].(map[string]any)
	disp, _ := first["display"].(string)
	if !strings.Contains(disp, "-") {
		t.Errorf("给人看的码该带连字符：%q", disp)
	}

	// CSV
	req, _ := http.NewRequest("GET", ts.URL+"/api/admin/codes.csv", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出该 200，实得 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type 不对：%q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("该是下载：%q", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	// 🩸 BOM 不能少：不加的话 Excel 打开中文全是乱码
	if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("CSV 少了 UTF-8 BOM（Excel 里中文会乱码）✗")
	}
	txt := string(body)
	// 🩸 公式注入：备注里那句 =1+1 必须被加一撇挡住
	if !strings.Contains(txt, "'=1+1") {
		t.Errorf("CSV 里没挡住公式注入：\n%s", txt)
	}
	// 🩸 换行/制表符不该出现在单元格里（会撑出额外的行）
	if strings.Contains(txt, "换行也试试\n") && !strings.Contains(txt, `"`) {
		t.Error("备注里的换行没被清掉")
	}
}

// 备注里的控制字符要在**存进去之前**就被清掉 —— 它会进 JSON、进 CSV、进页面。
func TestNoteSanitized(t *testing.T) {
	ts, _, st := newTest(t)
	c, csrf := adminLogin(t, ts, st)

	code, _, _ := do(t, c, "POST", ts.URL+"/api/admin/codes",
		map[string]any{"n": 1, "batch": "b\tc", "note": "a\nb\x00c"},
		map[string]string{headerCSRF: csrf})
	if code != http.StatusOK {
		t.Fatalf("发码失败 %d", code)
	}
	_, out, _ := do(t, c, "GET", ts.URL+"/api/admin/codes", nil, nil)
	rows, _ := out["codes"].([]any)
	first, _ := rows[0].(map[string]any)
	if n, _ := first["note"].(string); n != "abc" {
		t.Errorf("备注里的控制字符该被清掉，实得 %q", n)
	}
	if b, _ := first["batch"].(string); b != "bc" {
		t.Errorf("批号里的制表符该被清掉，实得 %q", b)
	}
}

// ── 账号管理 ─────────────────────────────────────────────

// 🩸 拦住"把自己停掉"这条自锁死路 —— 一旦停了，界面上就再也没人能把它开回来
// （只能 SSH 上去改文件）。
//
// ⚠️ 这条测试**必须先建两个管理员**。只有一个的时候"停自己"会被另一条更宽的
// 规则挡住，测出来的就不是这条规则了 —— 2026-10-15 反证时发现：一个管理员时
// 它照样是绿的 ✗ 而那种"因为别的原因通过"的测试，比没有更坏（它让人以为有保障）
func TestCannotDisableSelf(t *testing.T) {
	ts, _, st := newTest(t)
	c, csrf := adminLogin(t, ts, st) // 建 boss@qq.com，ID 一定是 1
	if _, err := st.AddAdmin("boss2@qq.com", "bossstrongpass2", auth.DefaultEmailDomains, time.Now()); err != nil {
		t.Fatal(err)
	}

	// ① 停自己 ⇒ 拒（这时候停掉它并不会让管理员归零，所以挡它的**只能**是这条规则）
	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/accounts/status",
		map[string]any{"id": 1, "status": auth.StatusDisabled},
		map[string]string{headerCSRF: csrf})
	if code != http.StatusBadRequest {
		t.Fatalf("停用自己该被拒，实得 %d %v ✗", code, out)
	}
	// ② 停别人 ⇒ 准（说明拒的是"自己"这个对象，不是"停用"这个动作）
	code, out, _ = do(t, c, "POST", ts.URL+"/api/admin/accounts/status",
		map[string]any{"id": 2, "status": auth.StatusDisabled},
		map[string]string{headerCSRF: csrf})
	if code != http.StatusOK {
		t.Fatalf("停掉另一个管理员该成功，实得 %d %v ✗", code, out)
	}
	// ③ 停完之后自己还进得来（"永远至少剩一个能用的管理员"这条不变量）
	if code, _, _ := do(t, c, "GET", ts.URL+"/api/admin/session", nil, nil); code != http.StatusOK {
		t.Fatalf("停掉别人之后自己也该还能进后台，实得 %d ✗", code)
	}
	// ④ 而且那个不变量在数据上也是真的
	active := 0
	for _, a := range st.Accounts() {
		if a.Role == auth.RoleAdmin && a.Status == auth.StatusActive {
			active++
		}
	}
	if active == 0 {
		t.Fatal("一个能用的管理员都不剩了 ✗")
	}
}

func TestRevokeSessions(t *testing.T) {
	ts, srv, st := newTest(t)
	c, csrf := adminLogin(t, ts, st)
	tok := signUp(t, ts, srv, st, "a@qq.com")

	// 找到那个普通用户的账号号（管理员是 1，用户不是）
	var uid int64
	for _, a := range st.Accounts() {
		if a.Email == "a@qq.com" {
			uid = a.ID
		}
	}
	if uid == 0 {
		t.Fatal("找不到刚注册的账号")
	}

	cc := newClient(t, ts)
	if code, _, _ := do(t, cc, "GET", ts.URL+"/api/me", nil,
		map[string]string{"Authorization": "Bearer " + tok}); code != http.StatusOK {
		t.Fatal("刚注册该能用")
	}
	code, out, _ := do(t, c, "POST", ts.URL+"/api/admin/accounts/revoke",
		map[string]any{"id": uid}, map[string]string{headerCSRF: csrf})
	if code != http.StatusOK {
		t.Fatalf("强制下线失败 %d %v", code, out)
	}
	if code, _, _ := do(t, cc, "GET", ts.URL+"/api/me", nil,
		map[string]string{"Authorization": "Bearer " + tok}); code != http.StatusUnauthorized {
		t.Fatalf("强制下线之后该 401，实得 %d ✗", code)
	}
}

// ── 信令侧取不到时的表现 ──────────────────────────────────

// 信令侧那个端点没开是**正常情况**（只开联机、没开指标），
// 界面该显示一句人话，而不是"服务器错误"。
func TestRoomsDegradesGracefully(t *testing.T) {
	ts, _, st := newTest(t)
	c, _ := adminLogin(t, ts, st)
	for _, p := range []string{"/api/admin/rooms", "/api/admin/stats"} {
		code, out, _ := do(t, c, "GET", ts.URL+p, nil, nil)
		if code != http.StatusOK {
			t.Fatalf("%s 该回 200（用 available 表达状态），实得 %d", p, code)
		}
		if out["available"] != false {
			t.Errorf("%s 该说 available=false，实得 %v", p, out["available"])
		}
		if s, _ := out["error"].(string); s == "" {
			t.Errorf("%s 该带一句能看懂的话", p)
		}
	}
}

// ── 日志纪律 ─────────────────────────────────────────────

// 🩸 日志里**不许**出现密码、令牌、邀请码原文。
// 日志会被别人看到（也可能被打包发出去），这些东西进去了就等于写在墙上 ✗
func TestLogsDoNotLeakSecrets(t *testing.T) {
	ts, srv, st := newTest(t)

	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	codes, err := st.GenCodes(1, "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mc, why := srv.mailCodes.beginSend(auth.NormalizeEmail("a@qq.com"), "127.0.0.1", srv.now())
	if why != "" {
		t.Fatal(why)
	}
	secret := "Sup3rSecretPassw0rd"
	code, out, _ := do(t, ts.Client(), "POST", ts.URL+"/api/signup",
		map[string]string{"invite": codes[0], "email": "a@qq.com",
			"password": secret, "mailCode": mc}, nil)
	if code != http.StatusOK {
		t.Fatalf("注册失败 %d %v", code, out)
	}
	tok, _ := out["token"].(string)

	logged := buf.String()
	for _, bad := range []string{secret, tok, codes[0]} {
		if bad != "" && strings.Contains(logged, bad) {
			t.Errorf("日志里出现了不该出现的东西 %q：\n%s", bad, logged)
		}
	}
	// 但**该**有日志（不然就是日志没了而不是安全了）
	if !strings.Contains(logged, "面板") {
		t.Errorf("一条访问日志都没有？\n%s", logged)
	}
}
