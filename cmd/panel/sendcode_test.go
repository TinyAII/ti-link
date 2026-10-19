// /api/sendcode 这一层的测试。
//
// 🩸 这个接口是整套东西里**唯一会让服务器主动往外发信**的地方。
// 所以最要紧的断言不是"能发出去"，而是**"不该发的时候一封都不发"** ——
// 少一道闸，它就成了拿我们邮箱对外群发的机器 ✗
package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"p2plink/internal/auth"
)

// insecureTLSForTest：假 SMTP 当然没有真证书，测试里不校验它。
// （这只在测试里用 —— 产品代码**永远**不许出现 InsecureSkipVerify ✗）
func insecureTLSForTest() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }

// lockedBuf 是个并发安全的日志缓冲：
// 面板会从多个 goroutine 打日志，直接 strings.Builder 会被 race 检测抓到 ✓
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// wireMail 给测试里的面板接上一个假 SMTP，返回那个假服务器。
func wireMail(t *testing.T, srv *Server) *fakeSMTP {
	t.Helper()
	f := startFakeSMTP(t, true)
	srv.mail = smtpConf{
		Host: f.host, Port: f.port,
		User: "u@qq.com", Pass: "p", From: "u@qq.com",
		TLS: insecureTLSForTest(),
	}
	return f
}

// mailCount 假服务器收过几封
func (f *fakeSMTP) mailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.to)
}

// lastCode 从最后收到的那封信里把 6 位验证码抠出来。
// 🩸 正文是 base64 ⇒ 得先解 —— 直接搜明文搜不到（smtp_test.go 上刚踩过）✗
func (f *fakeSMTP) lastCode(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	if len(f.msgs) == 0 {
		f.mu.Unlock()
		t.Fatal("一封都没收到")
	}
	// ⚠️ 取**最后一封**，不是把所有信拼起来 —— 拼起来 base64 就解不开了 ✗
	data := f.msgs[len(f.msgs)-1]
	f.mu.Unlock()
	body := []byte(mailText(t, data))
	m := regexp.MustCompile(`\b\d{6}\b`).FindString(string(body))
	if m == "" {
		t.Fatalf("信里没找到 6 位验证码：\n%s", string(body))
	}
	return m
}

// ⭐⭐ 没配发信 ⇒ 老实说"没开"，**不许假装发出去了**。
// 假装的话用户会对着一个永远收不到的码反复试，还以为是自己邮箱的问题 ✗
func TestSendCodeNotConfigured(t *testing.T) {
	ts, _, _ := newTest(t)
	c := newClient(t, ts)
	code, out, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "a@qq.com", "purpose": "login"}, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("没配发信该回 503，实得 %d %v", code, out)
	}
	if s, _ := out["error"].(string); !strings.Contains(s, "密码") {
		t.Errorf("该告诉他先用密码登录，实得 %q", s)
	}
}

// ⭐⭐ 这道闸最要紧：**注册要验证码时，没有有效邀请码就一封都不发**。
//
// 反证方法：把 apiSendCode 里 signup 那段 CheckCode 删掉，这条立刻红 ✓
// 它红了意味着：外网任何一个人都能让我们给任意邮箱发信 ⇒ 我们成了发垃圾邮件的跳板 ✗
func TestSendCodeSignupNeedsValidInvite(t *testing.T) {
	ts, srv, _ := newTest(t)
	f := wireMail(t, srv)
	c := newClient(t, ts)

	for _, invite := range []string{"", "AAAAAAAAAA", "不存在的码"} {
		code, out, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
			map[string]any{"email": "a@qq.com", "purpose": "signup", "invite": invite}, nil)
		if code == http.StatusOK {
			t.Fatalf("邀请码 %q 无效却放行了 ✗ %v", invite, out)
		}
	}
	if n := f.mailCount(); n != 0 {
		t.Fatalf("邀请码无效却已经发了 %d 封信 ✗ —— 这就成对外群发机了", n)
	}
}

// 有效邀请码 + 合法邮箱 ⇒ 真发一封，而且信里有个 6 位码
func TestSendCodeSignupSends(t *testing.T) {
	ts, srv, st := newTest(t)
	codes, err := st.GenCodes(1, "t", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := wireMail(t, srv)
	c := newClient(t, ts)

	code, out, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "a@qq.com", "purpose": "signup", "invite": codes[0]}, nil)
	if code != http.StatusOK {
		t.Fatalf("该发成功，实得 %d %v", code, out)
	}
	if n := f.mailCount(); n != 1 {
		t.Fatalf("该发一封，实得 %d 封", n)
	}
	if got := f.lastCode(t); len(got) != codeLen {
		t.Fatalf("信里的码不是 %d 位：%q", codeLen, got)
	}
	// 而且那个码真能用（走完注册）
	tok := signUpWithCode(t, ts, c, codes[0], "a@qq.com", "goodpassword", f.lastCode(t))
	if tok == "" {
		t.Fatal("拿信里的码该能注册成功")
	}
}

// 登录要验证码：没注册过的邮箱不发（不然任何人都能让我们给陌生地址发信）
func TestSendCodeLoginNeedsRegisteredEmail(t *testing.T) {
	ts, srv, _ := newTest(t)
	f := wireMail(t, srv)
	c := newClient(t, ts)

	code, out, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "nobody@qq.com", "purpose": "login"}, nil)
	if code == http.StatusOK {
		t.Fatalf("没注册过的邮箱不该发信 ✗ %v", out)
	}
	if n := f.mailCount(); n != 0 {
		t.Fatalf("给没注册的邮箱发了 %d 封信 ✗", n)
	}
}

// 邮箱不合规（不在白名单）⇒ 连一条记录都不该有
func TestSendCodeRejectsBadDomain(t *testing.T) {
	ts, srv, _ := newTest(t)
	f := wireMail(t, srv)
	c := newClient(t, ts)
	code, _, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "a@gmail.com", "purpose": "login"}, nil)
	if code == http.StatusOK {
		t.Fatal("不在白名单的邮箱不该发 ✗")
	}
	if f.mailCount() != 0 {
		t.Fatal("不该发出去任何一封")
	}
}

// ⭐ 整条链：注册（邀请码 + 邮箱验证码）→ 退出 → **用验证码登录**
//
// 这条是把两边接起来验：注册时用的是"邮箱验证码"，登录时也用它 ⇒
// 说明这套码在两个流程里是同一套、且都被真正校验过 ✓
func TestLoginWithMailCode(t *testing.T) {
	ts, srv, st := newTest(t)
	codes, err := st.GenCodes(1, "t", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f := wireMail(t, srv)
	c := newClient(t, ts)

	// ① 要注册的验证码
	if code, out, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "a@qq.com", "purpose": "signup", "invite": codes[0]}, nil); code != http.StatusOK {
		t.Fatalf("要注册验证码失败 %d %v", code, out)
	}
	signupCode := f.lastCode(t)

	// ② 注册
	tok := signUpWithCode(t, ts, c, codes[0], "a@qq.com", "goodpassword", signupCode)
	if tok == "" {
		t.Fatal("注册该成功")
	}

	// ③ 用刚注册的邮箱要一个**登录**验证码
	//    ⚠️ 要等过 60 秒重发间隔 —— 拿测试里的"现在"往后推
	srv.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if code, out, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "a@qq.com", "purpose": "login"}, nil); code != http.StatusOK {
		t.Fatalf("要登录验证码失败 %d %v", code, out)
	}
	loginCode := f.lastCode(t)

	// ④ 用它登录（不给密码）
	code, out, _ := do(t, c, "POST", ts.URL+"/api/login",
		map[string]any{"email": "a@qq.com", "mailCode": loginCode}, nil)
	if code != http.StatusOK {
		t.Fatalf("验证码登录该成功，实得 %d %v", code, out)
	}
	newTok, _ := out["token"].(string)
	if newTok == "" {
		t.Fatal("没拿到令牌")
	}
	// ⑤ 那枚令牌真能用来查"我是谁"
	if code, _, _ := do(t, c, "GET", ts.URL+"/api/me", nil,
		map[string]string{"Authorization": "Bearer " + newTok}); code != http.StatusOK {
		t.Fatalf("验证码登录拿到的令牌该能用，实得 %d ✗", code)
	}

	// ⑥ 同一个码**不能再用第二次**（单次使用）
	if code, _, _ := do(t, c, "POST", ts.URL+"/api/login",
		map[string]any{"email": "a@qq.com", "mailCode": loginCode}, nil); code == http.StatusOK {
		t.Fatal("同一个验证码登了两次 ✗")
	}
}

// ⭐⭐ 注册**必须真的验那个验证码**：错的码一律拒。
//
// 🩸 这条是反证时补出来的：原来只测了"用对码能注册成功"，
// 而把 apiSignup 里那段校验**整段删掉**，那些测试照样绿 ✗
// —— 等于从没测过「验证码是必须的」这件事。这就是"假绿"的典型形状 ✓
func TestSignupRejectsWrongMailCode(t *testing.T) {
	ts, srv, st := newTest(t)
	codes, err := st.GenCodes(1, "t", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_ = wireMail(t, srv)
	c := newClient(t, ts)

	// 先走一遍"要验证码"（证明这条路本身是通的，不是因为没配发信才失败）
	if code, out, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "a@qq.com", "purpose": "signup", "invite": codes[0]}, nil); code != http.StatusOK {
		t.Fatalf("要验证码该成功，实得 %d %v", code, out)
	}

	// 注册时填一个**错的**码
	code, out, _ := do(t, c, "POST", ts.URL+"/api/signup",
		map[string]any{"invite": codes[0], "email": "a@qq.com",
			"password": "goodpassword", "mailCode": "000000"}, nil)
	if code == http.StatusOK {
		t.Fatalf("验证码是错的却注册成功了 ✗ %v", out)
	}

	// 而且**邀请码没被吃掉**：没注册成功就不该消耗它。
	// （校验得排在 SignUp 前面才对 —— 这条顺带把次序也钉住了）
	srv.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	mc, why := srv.mailCodes.beginSend(auth.NormalizeEmail("a@qq.com"), "127.0.0.1", srv.now())
	if why != "" {
		t.Fatal(why)
	}
	if tok := signUpWithCode(t, ts, c, codes[0], "a@qq.com", "goodpassword", mc); tok == "" {
		t.Fatal("邀请码该还在（上一次没注册成功，不该消耗它）✗")
	}
}

// 密码那条路没被弄坏：给密码照样能登（不能因为加了验证码就把老路堵了）
func TestPasswordLoginStillWorks(t *testing.T) {
	ts, srv, st := newTest(t)
	signUp(t, ts, srv, st, "a@qq.com")
	c := newClient(t, ts)
	code, out, _ := do(t, c, "POST", ts.URL+"/api/login",
		map[string]any{"email": "a@qq.com", "password": "goodpassword"}, nil)
	if code != http.StatusOK {
		t.Fatalf("密码登录该还能用，实得 %d %v", code, out)
	}
}

// 邮箱验证码写进日志了没有？**不许** ✗
func TestCodeNotLogged(t *testing.T) {
	ts, srv, st := newTest(t)
	codes, _ := st.GenCodes(1, "t", "", time.Now())
	f := wireMail(t, srv)

	var buf lockedBuf
	// 🩸 log 的输出来源是全局的 ⇒ 这个测试**不能并行跑**
	// （别的测试同时打日志就会串进来，断言会随机红/绿）
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	c := newClient(t, ts)
	if code, _, _ := do(t, c, "POST", ts.URL+"/api/sendcode",
		map[string]any{"email": "a@qq.com", "purpose": "signup", "invite": codes[0]}, nil); code != http.StatusOK {
		t.Fatal("该发成功")
	}
	sent := f.lastCode(t)
	logged := buf.String()
	if strings.Contains(logged, sent) {
		t.Errorf("日志里出现了验证码 ✗\n%s", logged)
	}
	if strings.Contains(logged, "goodpassword") {
		t.Errorf("日志里出现了密码 ✗\n%s", logged)
	}
}

// signUpWithCode 是一次带验证码的注册。
func signUpWithCode(t *testing.T, ts *httptest.Server, c *http.Client,
	invite, email, password, mailCode string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"invite": invite, "email": email, "password": password, "mailCode": mailCode,
	})
	resp, err := c.Post(ts.URL+"/api/signup", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Token
}

// mailText 从一封**原始邮件**里取出正文（纯文本那段）✓
//
// 📌 2026-10-17 加 HTML 版之后，正文变成了 multipart/alternative：
//
//	直接拿第一个空行之后的全部内容去 base64 解码会失败 ✗（那里面混着 boundary）
//	⇒ 这里自己找 `Content-Type: text/plain` 那一段解 ✓
//	单段（没有 html）的邮件也照样能解 ✓
func mailText(t *testing.T, raw string) string {
	t.Helper()
	norm := strings.ReplaceAll(raw, "\r\n", "\n")

	// 找纯文本段的起点
	marker := "Content-Type: text/plain"
	i := strings.Index(norm, marker)
	if i < 0 {
		t.Fatalf("邮件里没有 text/plain 段：\n%s", norm)
	}
	rest := norm[i:]
	// 段头结束（第一个空行）之后就是正文
	sep := strings.Index(rest, "\n\n")
	if sep < 0 {
		t.Fatalf("邮件段不完整：\n%s", rest)
	}
	body := rest[sep+2:]
	// 到下一个 boundary 为止（没有 boundary 就是到结尾）
	if j := strings.Index(body, "\n--"); j >= 0 {
		body = body[:j]
	}
	enc := strings.ReplaceAll(strings.TrimSpace(body), "\n", "")
	out, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("正文 base64 解不开: %v\n原文：\n%s", err, enc[:min(120, len(enc))])
	}
	return string(out)
}
