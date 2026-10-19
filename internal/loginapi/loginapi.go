// Package loginapi 是**客户端 → 管理面板「用户接口」**的调用方。
//
// 面板那边有两块接口，别混：
//
//	/api/…        ← 这个包只碰这一块（客户端用，凭据走 Authorization: Bearer）
//	/api/admin/…  ← 那是浏览器后台，走 cookie + CSRF，跟客户端没关系
//
// 🩸 **密码只在这一层过手一次**，而且只往 https 发（见 CheckBase）。
// 这个包里**不许**把邮箱 / 密码 / 令牌写进任何日志 ✗
package loginapi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBase 是默认的登录服务器地址。
//
// 2026-10-16：老板把 `panel.tinyaii.cn` 解析到了信令那台机器，所以用它 ✓
// 走标准的 443（nginx 反代到面板的 127.0.0.1:8443），用户地址里不带端口 ——
// 让用户手打或口述 `https://panel.tinyaii.cn` 比带个 :8444 好得多 ✓
//
// ⚠️ 配错了也不会静默失败：地址连不上时界面说的是"连不上服务器"，
// 登录页底下那行小字还能点开改（见 ui/js/10-auth.js）✓
const DefaultBase = "https://panel.tinyaii.cn"

const (
	// maxBody 兜住响应体大小。登录接口正常就几百字节，给 8KB 很宽松了。
	maxBody = 8 << 10
	// timeout 是**必须有**的：没有它，服务器半死不活时界面会一直转圈，
	// 用户只能强杀进程 —— 而且他根本不知道是在等网络 ✗
	timeout = 10 * time.Second
)

// Account 是登录/注册成功之后拿到的东西。
type Account struct {
	Token string
	ID    int64
	Email string
	Role  string
}

// Client 是面板用户接口的客户端。
type Client struct {
	base string
	hc   *http.Client
}

// New 造一个客户端。base 先过一遍 CheckBase（不对就直接失败）。
func New(base string) (*Client, error) {
	b, err := CheckBase(base)
	if err != nil {
		return nil, err
	}
	return &Client{
		base: b,
		hc: &http.Client{
			Timeout: timeout,
			// 不许重定向：登录接口往别处跳本身就是件可疑的事，
			// 跟着跳等于把密码递给第三方 ✗
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("服务器要求跳转，已拒绝")
			},
		},
	}, nil
}

// CheckBase 校验并规范化服务器地址，返回能用的形式（失败时返回给用户看的中文）。
//
// 干三件事：
//
//	① 没写协议就补 https（用户多半只会填 mc.tinyaii.cn:8444）
//	② 地址必须是完整的主机（不能是空的、不能只有端口）
//	③ 🩸 **http 只允许回环地址**（127.0.0.1 / ::1 / localhost）
//	   非回环还用 http ⇒ 密码在网络上是明文 ✗
//	   这条挡的是"有人把地址改成 http://某台机器"这种降级 ——
//	   跟信令那边"域名走 TLS、IP 走明文且不降级"是同一族规矩 ✓
//	   （本地开发例外：面板用 -dev-tls 之外也能用明文跑，那会儿地址就是 127.0.0.1）
func CheckBase(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("没填服务器地址")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("服务器地址看不懂")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("服务器地址只能用 http:// 或 https://")
	}
	if u.Host == "" {
		return "", errors.New("服务器地址里没有主机名")
	}
	if u.Path != "" && u.Path != "/" {
		// 带着路径的地址多半是用户复制错了（比如粘了个文档链接）
		return "", errors.New("服务器地址不该带路径")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return "", errors.New("这个地址不是本机，只能用 https（http 会让密码在网上裸奔）")
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopback(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ServerErr 是服务器明确回了「哪里不对」时的那种错误。
// Msg 就是能直接给用户看的中文（面板那边回的就是中文）。
type ServerErr struct {
	Status int
	Msg    string
}

func (e *ServerErr) Error() string { return e.Msg }

// 这几个错误是"我们自己分不清到底是哪种"时的兜底 —— 但**话得说清楚**，
// 别笼统回一句"登录失败"让人以为是自己密码打错了 ✗
var (
	ErrUnreachable = errors.New("连不上服务器。检查一下网络，或者服务器地址填得对不对")
	ErrTimeout     = errors.New("服务器没响应，稍后再试")
)

func (c *Client) post(path string, in any, out any, token string) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req, out)
}

func (c *Client) get(path, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return classify(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return &ServerErr{Status: resp.StatusCode, Msg: e.Error}
		}
		return &ServerErr{Status: resp.StatusCode,
			Msg: fmt.Sprintf("服务器回了 %d，请稍后再试", resp.StatusCode)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// 回了 200 但不是 JSON —— 多半是地址指错了（指到了个网页）
		return errors.New("服务器回的内容看不懂，检查一下服务器地址")
	}
	return nil
}

// classify 把网络层的错误翻成用户能懂的话。
//
// 🩸 分不清就老实说"连不上"，**绝不说"密码错"** ——
// 那种误导会让人一遍遍改密码，而问题根本不在那儿 ✗
func classify(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	var ce *tls.CertificateVerificationError
	if errors.As(err, &ce) {
		// 证书不对是个**严重**信号，不能建议用户"忽略" ✗
		return errors.New("服务器证书校验没通过 —— 可能是网络被劫持，先别登录，联系我们")
	}
	var ue *url.Error
	if errors.As(err, &ue) && strings.Contains(ue.Error(), "跳转") {
		return errors.New("服务器要求跳转，已拒绝")
	}
	return ErrUnreachable
}

type authResp struct {
	Token string `json:"token"`
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

// SignUp 用邀请码 + 邮箱验证码注册，并顺手登录（服务器那边注册即登录）。
//
// 📌 参数名叫 invite 不叫 code：现在有**两种码**（邀请码 / 邮箱验证码），
// 再叫 code 谁都分不清是哪一个 ✗
func (c *Client) SignUp(invite, email, password, mailCode string) (*Account, error) {
	var r authResp
	if err := c.post("/api/signup", map[string]string{
		"invite": invite, "email": email, "password": password, "mailCode": mailCode,
	}, &r, ""); err != nil {
		return nil, err
	}
	if r.Token == "" {
		return nil, errors.New("服务器没给登录凭据，请稍后再试")
	}
	return &Account{Token: r.Token, ID: r.ID, Email: r.Email, Role: r.Role}, nil
}

// Login 用邮箱 + 密码登录。
func (c *Client) Login(email, password string) (*Account, error) {
	var r authResp
	if err := c.post("/api/login",
		map[string]string{"email": email, "password": password}, &r, ""); err != nil {
		return nil, err
	}
	if r.Token == "" {
		return nil, errors.New("服务器没给登录凭据，请稍后再试")
	}
	return &Account{Token: r.Token, ID: r.ID, Email: r.Email, Role: r.Role}, nil
}

// LoginByCode 用**邮箱验证码**登录（不用密码）。
//
// 📌 跟 Login 是两条路，不合成一个带可选参数的函数：
// 合成之后"两个都空"和"两个都填"这种状态就得靠调用方自觉处理，
// 而这里恰恰是**认证入口**，宁可写两个明确的函数 ✓
func (c *Client) LoginByCode(email, mailCode string) (*Account, error) {
	var r authResp
	if err := c.post("/api/login",
		map[string]string{"email": email, "mailCode": mailCode}, &r, ""); err != nil {
		return nil, err
	}
	if r.Token == "" {
		return nil, errors.New("服务器没给登录凭据，请稍后再试")
	}
	return &Account{Token: r.Token, ID: r.ID, Email: r.Email, Role: r.Role}, nil
}

// SendCode 让服务器往这个邮箱发一封验证码。
//
// purpose：
//
//	"login"  → 登录用（要求这个邮箱注册过）
//	"signup" → 注册用（**必须同时给邀请码** —— 不然这个接口谁都能拿来群发）
//
// ⚠️ 这是唯一一个会**主动往外发信**的调用。调用方那边要防着用户连点
// （按钮置灰 + 倒计时），不然很容易撞上服务器的 60 秒重发间隔 ✓
func (c *Client) SendCode(email, purpose, invite string) error {
	body := map[string]string{"email": email, "purpose": purpose}
	if invite != "" {
		body["invite"] = invite
	}
	return c.post("/api/sendcode", body, nil, "")
}

// ResetPassword 用邮箱验证码重置密码（「忘记密码」那条路）✓
//
// 🩸 不需要旧密码、也不需要令牌 —— 身份完全靠验证码证明 ✓
//
//	改完之后服务器会**撤销这个账号的所有会话** ⇒ 之前登录过的设备都得重新登 ✓
func (c *Client) ResetPassword(email, code, pass string) error {
	return c.post("/api/reset", map[string]string{
		"email": email, "code": code, "pass": pass,
	}, nil, "")
}

// SignupMode 问服务器"注册现在要不要邀请码"（注册页据此决定写不写「可选」）✓
func (c *Client) SignupMode() (needsCode bool, err error) {
	var r struct {
		NeedsCode bool `json:"needsCode"`
	}
	if err := c.get("/api/signupmode", "", &r); err != nil {
		return false, err
	}
	return r.NeedsCode, nil
}

// Me 拿令牌问一句"我还算登录着吗"。用来在启动时悄悄验证本地令牌还有没有效。
func (c *Client) Me(token string) (*Account, error) {
	var r authResp
	if err := c.get("/api/me", token, &r); err != nil {
		return nil, err
	}
	return &Account{ID: r.ID, Email: r.Email, Role: r.Role}, nil
}

// Logout 让服务器把这个会话作废。
//
// ⚠️ 本地那枚令牌**无论如何都要清掉**（调用方负责）——
// 网络不通的时候注销也必须成功，不然用户会以为"退不出去" ✗
func (c *Client) Logout(token string) error {
	return c.post("/api/logout", map[string]string{}, nil, token)
}
