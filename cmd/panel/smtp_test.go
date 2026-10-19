// 发信那一层的测试。
//
// 🩸 为什么非要写个**假 SMTP 服务器**：真发一封信只能证明"这次成功"，
// 证明不了那些**出错路径** —— 对面不支持 STARTTLS 时会不会把口令明文递出去、
// 认证失败时报的是不是人话、正文到底长什么样。这几件都是"错了也不报错"的地方 ✗
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"p2plink/internal/tlsconf"
)

// ---- ① 拼出来的邮件本身 ----

func TestBuildMailShape(t *testing.T) {
	raw := string(buildMail("a@qq.com", "b@qq.com", "TI 联机 · 登录验证码", "你的验证码是：123456", ""))
	for _, want := range []string{
		"From: ", "To: <b@qq.com>\r\n",
		"MIME-Version: 1.0", "charset=UTF-8",
		// 📌 中文主题必须做 MIME 编码 —— 不编的话一堆客户端显示成乱码 ✗
		"Subject: =?UTF-8?",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("邮件里少了 %q：\n%s", want, raw)
		}
	}
	// 正文是 base64：解出来该是我们给的那段原文
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("头和正文没分开：\n%s", raw)
	}
	body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", ""))
	if err != nil {
		t.Fatalf("正文不是合法 base64（中文会被中间那些只认 7bit 的服务器折断）: %v", err)
	}
	if !strings.Contains(string(body), "123456") {
		t.Errorf("正文里该有那个码，实得 %q", string(body))
	}
	// 每一行都不超过 SMTP 的 1000 字节上限（RFC 5321）
	for _, line := range strings.Split(raw, "\r\n") {
		if len(line) > 998 {
			t.Errorf("有一行 %d 字节，超了 SMTP 的上限", len(line))
		}
	}
}

func TestCodeMailBodyWording(t *testing.T) {
	b := codeMailBody(mailSceneOf("login"), "654321", codeTTL)
	// 三样都要有：码本身、有效期、"不是你干的就别理"
	for _, want := range []string{"654321", "5 分钟", "忽略"} {
		if !strings.Contains(b, want) {
			t.Errorf("正文里少了 %q：\n%s", want, b)
		}
	}
}

func TestSmtpNotConfigured(t *testing.T) {
	var c smtpConf
	if c.ready() {
		t.Fatal("空配置不该算「配好了」")
	}
	if err := c.send("a@qq.com", "s", "b", ""); err == nil {
		t.Fatal("没配就该明确报错，不能假装发出去 ✗")
	}
}

// ---- ② 假 SMTP 服务器 ----

// fakeSMTP 是个只会说最少那几句的 SMTP 服务器，用来验我们这边的对话。
type fakeSMTP struct {
	ln       net.Listener
	host     string
	port     int
	needAuth bool // 要不要 AUTH

	mu       sync.Mutex
	authUser string
	authPass string
	helo     string
	from     string
	to       []string
	msgs     []string // 每收到一封就追加一条（**别攒成一大串** —— 两封信拼起来 base64 就解不开了 ✗）
	// startTLS 是"**这台服务器**收过的信里，有没有一次走过 STARTTLS"（给断言用）。
	// 🩸 它**不是**"这条连接升没升级" —— 那个必须用每条连接自己的变量，
	// 混用的话第一条连接升完级，第二条就不播 STARTTLS 了 ⇒
	// 产品会**正确地**拒绝发信，而测试看起来像产品坏了 ✗（2026-10-16 刚踩到）
	startTLS bool
}

// startFakeSMTP 起一个**有自签证书**的假服务器（要能 STARTTLS）。
func startFakeSMTP(t *testing.T, needAuth bool) *fakeSMTP {
	t.Helper()
	cfg, err := tlsconf.NewDev() // 复用那套自签证书（只认 localhost）
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, host: "127.0.0.1", needAuth: needAuth}
	f.port = ln.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c, cfg)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn, cfg *tls.Config) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")

	var dataMode bool
	didTLS := false         // 这条连接升过级没有（跟上面那个 f.startTLS 不是一回事）
	var cur strings.Builder // 这一封的正文（每封单独攒）
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)

		if dataMode {
			if line == "." {
				dataMode = false
				// 收满一封就存成**独立的一条** —— 攒成一大串的话，
				// 两封信拼在一起 base64 就解不开了 ✗（刚踩到）
				f.mu.Lock()
				f.msgs = append(f.msgs, cur.String())
				f.mu.Unlock()
				w("250 ok queued")
				continue
			}
			cur.WriteString(line)
			cur.WriteString("\n")
			continue
		}

		switch {
		case strings.HasPrefix(up, "EHLO"):
			f.mu.Lock()
			f.helo = line
			f.mu.Unlock()
			w("250-fake")
			if f.needAuth {
				w("250-AUTH PLAIN")
			}
			if !didTLS {
				w("250 STARTTLS")
			} else {
				w("250 OK")
			}
		case strings.HasPrefix(up, "STARTTLS"):
			w("220 ready")
			tc := tls.Server(c, cfg)
			if err := tc.Handshake(); err != nil {
				return
			}
			didTLS = true
			f.mu.Lock()
			f.startTLS = true
			f.mu.Unlock()
			c = tc
			br = bufio.NewReader(c)
			w = func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		case strings.HasPrefix(up, "AUTH PLAIN"):
			// AUTH PLAIN <base64(\0user\0pass)>
			fields := strings.Fields(line)
			if len(fields) < 3 {
				w("501 bad auth")
				continue
			}
			dec, err := base64.StdEncoding.DecodeString(fields[2])
			if err != nil {
				w("501 bad auth")
				continue
			}
			parts := strings.Split(string(dec), "\x00")
			f.mu.Lock()
			if len(parts) >= 3 {
				f.authUser, f.authPass = parts[1], parts[2]
			}
			f.mu.Unlock()
			w("235 ok")
		case strings.HasPrefix(up, "MAIL FROM"):
			f.mu.Lock()
			f.from = line
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO"):
			f.mu.Lock()
			f.to = append(f.to, line)
			f.mu.Unlock()
			w("250 ok")
		case up == "DATA":
			dataMode = true
			cur.Reset()
			w("354 go ahead")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

// ⭐ 一条完整的对话跑通：EHLO → STARTTLS → AUTH → MAIL → RCPT → DATA
func TestSmtpSendHappyPath(t *testing.T) {
	f := startFakeSMTP(t, true)
	c := smtpConf{
		Host: f.host, Port: f.port,
		User: "发信账号@qq.com", Pass: "授权码",
		From: "发信账号@qq.com",
		TLS:  &tls.Config{InsecureSkipVerify: true}, // 假的当然没真证书
	}
	if err := c.send("收件人@qq.com", "TI 联机 · 登录验证码", "你的验证码是：112233", ""); err != nil {
		t.Fatalf("该发成功，实得 %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.startTLS {
		t.Error("没走 STARTTLS —— 口令就明文出去了 ✗")
	}
	if f.authUser != "发信账号@qq.com" || f.authPass != "授权码" {
		t.Errorf("认证信息不对：%q / %q", f.authUser, f.authPass)
	}
	if !strings.Contains(f.from, "发信账号@qq.com") {
		t.Errorf("发件人不对：%q", f.from)
	}
	if len(f.to) != 1 || !strings.Contains(f.to[0], "收件人@qq.com") {
		t.Errorf("收件人不对：%v", f.to)
	}
	// 正文是 base64 ⇒ 断言前先解出来（直接搜明文是搜不到的，别犯这个傻）
	last := f.msgs[len(f.msgs)-1]
	sep := strings.Index(last, "\n\n")
	if sep < 0 {
		t.Fatalf("收到的邮件里头和正文没分开：\n%s", last)
	}
	enc := strings.ReplaceAll(strings.TrimSpace(last[sep+2:]), "\n", "")
	body, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("收到的正文 base64 解不开: %v\n%s", err, last)
	}
	if !strings.Contains(string(body), "112233") {
		t.Errorf("正文里该有那个码，实得 %q", string(body))
	}
}

// ⭐⭐ 对面不给 STARTTLS ⇒ **必须拒绝发**（不能把口令明文递出去）
//
// 反证方法：把 send() 里那段 STARTTLS 检查删掉，这条立刻红 ✓
func TestSmtpRefusesWithoutSTARTTLS(t *testing.T) {
	// 这个假服务器**永远不播** STARTTLS
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				_, _ = c.Write([]byte("220 fake\r\n"))
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(strings.ToUpper(line), "EHLO") {
						_, _ = c.Write([]byte("250-fake\r\n250 OK\r\n")) // 没有 STARTTLS
					} else {
						_, _ = c.Write([]byte("250 ok\r\n"))
					}
				}
			}(c)
		}
	}()

	c := smtpConf{
		Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port,
		User: "u", Pass: "p", From: "u@qq.com",
	}
	err = c.send("a@qq.com", "s", "b", "")
	if err == nil {
		t.Fatal("对面不支持 STARTTLS 却还是发了 —— 口令就明文上网了 ✗")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("错误里该提到 STARTTLS（不然运维看不懂为什么发不出去）：%v", err)
	}
}

// 连不上时报的是人话，而且**不能**把口令写进错误里
func TestSmtpUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // 关掉，端口空出来

	c := smtpConf{Host: "127.0.0.1", Port: port, User: "u", Pass: "超级秘密口令", From: "u@qq.com"}
	err = c.send("a@qq.com", "s", "b", "")
	if err == nil {
		t.Fatal("连不上该报错")
	}
	if strings.Contains(err.Error(), "超级秘密口令") {
		t.Fatalf("错误信息里带上了口令 ✗ 这行会被写进日志：%v", err)
	}
	if !strings.Contains(err.Error(), "发信") {
		t.Errorf("该说清是发信这一步出的问题：%v", err)
	}
}

// ── 邮件美化（2026-10-17）───────────────────────────────

// ⭐ HTML 版必须守住的三条铁律（守不住在 QQ 邮箱/Gmail 里就变形）
func TestCodeMailHTMLRules(t *testing.T) {
	h := codeMailHTML(mailSceneOf("reset"), "K7QX9M2P", 5*time.Minute)

	// ① 码在里面（而且没被转义坏）
	if !strings.Contains(h, "K7QX9M2P") {
		t.Error("HTML 版里找不到验证码 ✗ 那用户看什么")
	}
	// ② **不许有外部资源**：图片/CSS/字体会被拦，也是我们自己的规矩
	for _, bad := range []string{"http://", "https://", "<link", "<script", "<img", "@import"} {
		if strings.Contains(h, bad) {
			t.Errorf("HTML 里出现了 %q ✗（外部资源会被拦，也不该有）", bad)
		}
	}
	// ③ 不许用 <style> 块（Gmail 会剥掉，样式就全丢了 ✗）
	if strings.Contains(h, "<style") {
		t.Error("用了 <style> 块 ✗ 邮件里必须全内联")
	}
	// ④ 用 table 布局（flex/grid 邮件客户端不认）
	if !strings.Contains(h, "table") {
		t.Error("没有 table 布局 ✗")
	}
}

// ⭐ 带 html 时是 multipart/alternative，两段都要能解出正确内容
//
// 反证方法：把 buildMail 里的 html 分支删掉（永远走纯文本）⇒ 立刻红 ✓
func TestBuildMailMultipart(t *testing.T) {
	const text = "纯文本版：验证码 123456"
	const html = "<b>HTML 版：验证码 123456</b>"
	raw := string(buildMail("a@qq.com", "b@qq.com", "TI 联机 · 登录验证码", text, html))

	if !strings.Contains(raw, "multipart/alternative") {
		t.Fatalf("没标成 multipart/alternative ✗\n%s", raw[:200])
	}
	if !strings.Contains(raw, "boundary=") {
		t.Fatal("没有 boundary ✗")
	}

	// 纯文本段能解出来
	if got := mailText(t, raw); got != text {
		t.Errorf("纯文本段不对：%q", got)
	}
	// HTML 段也能解出来（用同样的思路，找 text/html 那段）
	i := strings.Index(raw, "Content-Type: text/html")
	if i < 0 {
		t.Fatal("没有 text/html 段 ✗")
	}
	rest := raw[i:]
	sep := strings.Index(rest, "\r\n\r\n")
	if sep < 0 {
		t.Fatal("text/html 段不完整 ✗")
	}
	body := rest[sep+4:]
	if j := strings.Index(body, "\r\n--"); j >= 0 {
		body = body[:j]
	}
	dec, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(body), "\r\n", ""))
	if err != nil {
		t.Fatalf("HTML 段 base64 解不开: %v", err)
	}
	if string(dec) != html {
		t.Errorf("HTML 段不对：%q", string(dec))
	}
}

// ⭐ 三种场景不能长得一样（老板原话：「注册，登录，找回密码怎么都是同一套模板」）
//
// 反证方法：把 mailSceneOf 改成永远返回 login 那套 ⇒ 立刻红 ✓
func TestMailScenesDiffer(t *testing.T) {
	purposes := []string{"signup", "login", "reset"}
	seenSubj := map[string]string{}
	seenTitle := map[string]string{}

	for _, p := range purposes {
		sc := mailSceneOf(p)
		if sc.Subject == "" || sc.Title == "" || sc.Sub == "" || sc.Note == "" {
			t.Errorf("%s 场景有空白字段（%+v）✗", p, sc)
			continue
		}
		if prev, dup := seenSubj[sc.Subject]; dup {
			t.Errorf("%s 和 %s 的主题一样：%q ✗ 收信人分不出是哪件事", p, prev, sc.Subject)
		}
		seenSubj[sc.Subject] = p
		if prev, dup := seenTitle[sc.Title]; dup {
			t.Errorf("%s 和 %s 的标题一样：%q ✗", p, prev, sc.Title)
		}
		seenTitle[sc.Title] = p
	}

	// 重置密码那封的提醒必须比登录那封"重"：要提到有人在试他的邮箱
	if !strings.Contains(mailSceneOf("reset").Note, "不是") {
		t.Error("重置密码的提醒没有\"如果不是你\"这句 ✗ 用户不知道该警惕")
	}
}

// ⭐ 三个场景的正文都要能生成、都要带码、都要说清有效期
func TestMailBodiesPerScene(t *testing.T) {
	for _, p := range []string{"signup", "login", "reset"} {
		sc := mailSceneOf(p)
		txt := codeMailBody(sc, "778899", 5*time.Minute)
		htm := codeMailHTML(sc, "778899", 5*time.Minute)

		for name, body := range map[string]string{"纯文本": txt, "HTML": htm} {
			if !strings.Contains(body, "778899") {
				t.Errorf("%s/%s 正文里没有验证码 ✗", p, name)
			}
			if !strings.Contains(body, "5 分钟") {
				t.Errorf("%s/%s 正文里没说有效期 ✗", p, name)
			}
		}
		// HTML 里不该出现裸的 ** 标记（那是给纯文本用的语法）
		if strings.Contains(htm, "**") {
			t.Errorf("%s 的 HTML 里漏了 ** 标记 ✗（noteHTML 没替换干净）", p)
		}
	}
}
