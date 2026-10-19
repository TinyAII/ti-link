// 发验证码邮件。
//
// 🩸 **口令只从环境变量来，绝不写进任何文件、也绝不进日志** ✗
// （写进 git 就是把发信权限公开；进日志就是写在墙上）
//
// 走的是标准库 net/smtp，没引第三方：
//
//	465 → **隐式 TLS**（连上就是 TLS）—— 腾讯、网易都推荐这个
//	587/25 → 先明文、再 STARTTLS 升级
//
// 两种都支持，是因为换一家邮箱服务商就换了端口 —— 不想为此再改一次代码 ✓
package main

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type smtpConf struct {
	Host string
	Port int
	User string
	Pass string
	From string

	// TLS 留空就用"按主机名校验证书"的默认配置。
	// 📌 留这个口子是给两件事用的：① 自签证书的场景；② 测试里那个假 SMTP 服务器
	// （它当然没有真证书）。**正常部署永远留空** ✓
	TLS *tls.Config
}

// tlsConfig 造这次连接用的 TLS 配置。
func (c smtpConf) tlsConfig() *tls.Config {
	if c.TLS != nil {
		return c.TLS
	}
	return &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
}

// ready 判断"配没配发信"。
// 📌 没配的时候**不能装作发出去了** —— 调用方要据这个决定"验证码功能整个关掉"，
// 而不是发个假的然后让用户对着一个永远收不到的码发呆 ✗
func (c smtpConf) ready() bool {
	return c.Host != "" && c.From != ""
}

// smtpTimeout 是**每一步**的时限：连接、认证、发数据，都卡在它以内。
// 不设的话，对面半死不活时这个 HTTP 请求会一直挂着（用户只能强杀）✗
const smtpTimeout = 15 * time.Second

func (c smtpConf) send(to, subject, text, html string) error {
	if !c.ready() {
		return errors.New("没配发信服务器")
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	d := &net.Dialer{Timeout: smtpTimeout}

	var conn net.Conn
	var err error
	tlsCfg := c.tlsConfig()
	if c.Port == 465 {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tlsCfg)
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连不上发信服务器: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(smtpTimeout))

	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return fmt.Errorf("发信握手失败: %w", err)
	}
	defer cl.Close()

	// 非 465 的：必须升到 TLS 才继续。
	// 🩸 **升不上去就不发** —— 明文递账号密码是把凭据白送出去 ✗
	if c.Port != 465 {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("发信服务器不支持 STARTTLS，拒发明文")
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("STARTTLS 失败: %w", err)
		}
	}

	if c.User != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.User, c.Pass, c.Host)); err != nil {
			// ⚠️ 错误里带上 err，但 err 里**不会**有密码（net/smtp 不回显它）✓
			return fmt.Errorf("发信账号认证失败: %w", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return fmt.Errorf("发信被拒（发件人）: %w", err)
	}
	if err := cl.Rcpt(to); err != nil {
		return fmt.Errorf("发信被拒（收件人）: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("发信被拒（正文）: %w", err)
	}
	if _, err := w.Write(buildMail(c.From, to, subject, text, html)); err != nil {
		return fmt.Errorf("写正文失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("发送失败: %w", err)
	}
	// Quit 失败不算失败：信已经交出去了（错误多半只是"再见"没说完）✓
	_ = cl.Quit()
	return nil
}

// buildMail 拼一封信。
//
// 📌 html 传空 ⇒ 只发纯文本（老路径，测试也靠它）；传了 ⇒ **multipart/alternative**，
//
//	纯文本和 HTML 各一份，客户端挑能显示的 ✓
//	（没有纯文本版的 HTML 邮件，在纯文本模式的客户端里是一片空白 ✗）
//
// 两个地方容易踩：
//   - **主题里有中文**必须先做 MIME 编码，不然一堆邮件客户端显示成乱码 ✗
//   - **正文用 base64**：中文 + 长行都不会被某些中间的 SMTP 服务器折断 ✗
//     （裸 UTF-8 正文遇到只认 7bit 的中转就会烂掉）
func buildMail(from, to, subject, text, html string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s <%s>\r\n", mime.BEncoding.Encode("UTF-8", "TI 联机"), from)
	fmt.Fprintf(&b, "To: <%s>\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.BEncoding.Encode("UTF-8", subject))
	b.WriteString("MIME-Version: 1.0\r\n")

	if strings.TrimSpace(html) == "" {
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n")
		b.WriteString("\r\n")
		writeB64(&b, text)
		return []byte(b.String())
	}

	// 🩸 multipart 的 boundary 只许 ASCII 且不含空格/引号，用固定的随机串最省事 ✓
	const bd = "=_TI_LINK_MAIL_7f3a2c91_="
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", bd)
	b.WriteString("\r\n")
	b.WriteString("这封信需要支持 HTML 或纯文本的客户端才能显示。\r\n")

	// 第一段：纯文本（客户端优先用最后一段能显示的，所以顺序是"由简到繁"）
	b.WriteString("--" + bd + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	writeB64(&b, text)

	// 第二段：HTML
	b.WriteString("--" + bd + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")
	writeB64(&b, html)

	b.WriteString("--" + bd + "--\r\n")
	return []byte(b.String())
}

// writeB64 按 RFC 2045 折行（每行不超过 76 字符）。
//
// 🩸 不折行的话，某些只认 998 字符行的中转会把这封信判成坏信 ✗
func writeB64(b *strings.Builder, s string) {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
}

// mailScene 是一封验证码邮件的"场景文案"。
//
// 📌 视觉三封**保持同一套**（用户一眼认得出是我们的信 ✓），
//
//	变的是这几处文字 —— 让收信的人不用读完就知道"这封是为什么来的" ✓
type mailScene struct {
	Subject string // 邮件主题
	Title   string // 卡片大标题
	Sub     string // 标题下面那句
	Note    string // 底部提醒（每个场景该提醒的都不一样）
}

// mailSceneOf 按 purpose 选文案。
//
// 🩸 三个场景的"提醒"必须分开写：
//
//	· 注册：告诉他注册完直接登录，省得再登一次
//	· 登录：告诉他"不是你操作就忽略"
//	· 重置密码：**要提醒他"有人在试你的邮箱"** —— 这是唯一一个能改掉密码的入口 ✗
func mailSceneOf(purpose string) mailScene {
	switch purpose {
	case "signup":
		return mailScene{
			Subject: "TI 联机 · 注册验证码",
			Title:   "欢迎加入",
			Sub:     "填完这个码就注册好了",
			Note:    "注册成功后会直接登录，不用再登一次。如果不是你本人操作，忽略这封邮件就行。",
		}
	case "reset":
		return mailScene{
			Subject: "TI 联机 · 重置密码验证码",
			Title:   "重置密码",
			Sub:     "有人申请重设这个账号的密码",
			Note:    "**如果不是你**，说明有人在试你的邮箱 —— 忽略这封邮件即可，他拿不到码就改不了密码。要是担心，建议登录后自己改一次密码。",
		}
	default: // login
		return mailScene{
			Subject: "TI 联机 · 登录验证码",
			Title:   "登录验证",
			Sub:     "你正在登录 TI 联机",
			Note:    "如果不是你本人操作，忽略这封邮件就行 —— 别人拿不到这个码，也进不去你的账号。",
		}
	}
}

// codeMailBody 是验证码邮件的**纯文本版**。
//
// 📌 措辞规矩：**第一行就把码写出来**（手机上不用展开就能看到）+ 说清楚多久过期 +
// 说清楚"不是你干的就别理"。这三样少了哪样都会有人来问 ✓
func codeMailBody(sc mailScene, code string, ttl time.Duration) string {
	minutes := int(ttl.Minutes())
	return fmt.Sprintf(`%s —— 你的验证码是：%s

%d 分钟内有效，用完即失效。
%s

——
TI 联机
（这是一封系统邮件，不用回复）`, sc.Title, code, minutes, strings.ReplaceAll(sc.Note, "**", ""))
}

// codeMailHTML 是验证码邮件的 HTML 版。
//
// 🩸 三条必须守住的（不然在 QQ 邮箱 / Gmail / 手机上会变形）：
//  1. **table 布局**、**全内联样式** —— 邮件客户端不认 flex/grid，Gmail 还会剥 <style>
//  2. **不引任何外部资源**（图/字体/CSS 都会被拦，也是我们自己的规矩）
//  3. 码要**第一眼看到**：放标题下面第一块、32px 等宽字、带字间距
//
// 📌 配色跟官网/客户端一套（浅色 + 同一个绿 #0e9c74）——
//
//	邮件是用户看到的第一眼，不能像个野生的系统通知 ✗
func codeMailHTML(sc mailScene, code string, ttl time.Duration) string {
	minutes := int(ttl.Minutes())
	// ⚠️ 码只含字母数字，但还是过一道 —— 万一以后码的格式变了，别把 HTML 注进去 ✗
	safe := html.EscapeString(code)
	return `<!DOCTYPE html>
<html lang="zh-CN"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light">
<title>TI 联机 · 验证码</title>
</head>
<body style="margin:0;padding:0;background:#f4f6f7;">
<!-- 外层：铺满的背景色（邮件客户端里 table 才能铺满）-->
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"
       style="background:#f4f6f7;padding:32px 12px;">
  <tr><td align="center">
    <!-- 卡片 -->
    <table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0"
           style="width:600px;max-width:600px;background:#ffffff;border:1px solid #e5e9ed;border-radius:14px;">
      <tr><td style="padding:28px 32px 0 32px;">
        <!-- 品牌行 -->
        <table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
          <td style="width:30px;height:30px;background:#0e9c74;border-radius:8px;
                     text-align:center;vertical-align:middle;color:#ffffff;
                     font:600 15px/30px -apple-system,'Segoe UI','Microsoft YaHei',sans-serif;">TI</td>
          <td style="padding-left:10px;font:600 15px/1.4 -apple-system,'Segoe UI','Microsoft YaHei',sans-serif;color:#101418;">
            TI 联机
          </td>
        </tr></table>
      </td></tr>

      <tr><td style="padding:22px 32px 0 32px;">
        <div style="font:600 20px/1.4 -apple-system,'Segoe UI','Microsoft YaHei',sans-serif;color:#101418;">
          ` + html.EscapeString(sc.Title) + `
        </div>
        <div style="margin-top:6px;font:400 14px/1.6 -apple-system,'Segoe UI','Microsoft YaHei',sans-serif;color:#5a626b;">
          ` + strconv.Itoa(minutes) + ` 分钟内有效，用完即失效 · ` + html.EscapeString(sc.Sub) + `
        </div>
      </td></tr>

      <!-- 码：这一块是整个邮件的重点 -->
      <tr><td style="padding:20px 32px 0 32px;">
        <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
          <tr><td align="center"
                  style="background:#f1f8f5;border:1px solid #d6f0e6;border-radius:12px;
                         padding:20px 12px;">
            <span style="font:700 34px/1.1 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
                         color:#0b7d5e;letter-spacing:6px;">` + safe + `</span>
          </td></tr>
        </table>
      </td></tr>

      <tr><td style="padding:20px 32px 0 32px;">
        <p style="margin:0;font:400 14px/1.7 -apple-system,'Segoe UI','Microsoft YaHei',sans-serif;color:#5a626b;">
          ` + noteHTML(sc.Note) + `
        </p>
      </td></tr>

      <tr><td style="padding:24px 32px 26px 32px;">
        <div style="height:1px;background:#eef1f3;line-height:1px;font-size:0;">&nbsp;</div>
        <p style="margin:14px 0 0 0;font:400 12.5px/1.7 -apple-system,'Segoe UI','Microsoft YaHei',sans-serif;color:#8b939c;">
          这是一封系统邮件，不用回复。<br>
          TI 联机 · 两台电脑直连，游戏数据不经过服务器。
        </p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body></html>`
}

// noteHTML 把 Note 里的 **…** 换成粗体标签（纯文本版则把 ** 去掉）。
//
// 📌 先转义再加标签：Note 是我们自己写的文案，但**别开这个口子** ✗
//
//	以后万一有人把用户输入拼进来，这就是注入点 ✓
func noteHTML(note string) string {
	esc := html.EscapeString(note)
	// 成对替换 **x** → <b>x</b>（只处理简单情况，够用 ✓）
	for {
		i := strings.Index(esc, "**")
		if i < 0 {
			break
		}
		j := strings.Index(esc[i+2:], "**")
		if j < 0 {
			break
		}
		esc = esc[:i] + "<b style=\"color:#101418;\">" + esc[i+2:i+2+j] + "</b>" + esc[i+2+j+4:]
	}
	return esc
}
