// 登录相关的页面接口（暴露给 UI）。
//
// 界面上四件事：注册 / 登录 / 退出 / 启动时悄悄验一下。
// 凭据（令牌）落在数据目录里、由 internal/identity 管；这个文件只负责
// "跟服务器说话"和"把结果翻成人话"。
//
// 🩸 这一层**不许**把邮箱、密码、令牌打进日志 —— 它可以被导出成排障日志，
// 而日志会被发出去。要记就记"有一次登录失败"，别记是谁、用什么密码 ✗
package main

import (
	"errors"

	"p2plink/internal/identity"
	"p2plink/internal/loginapi"
)

// loginBase 是当前生效的登录服务器地址（设置里改过就用设置里的）。
func loginBase() string {
	if s := currentSettings(); s.LoginBase != "" {
		return s.LoginBase
	}
	return loginapi.DefaultBase
}

func newLoginClient() (*loginapi.Client, error) {
	c, err := loginapi.New(loginBase())
	if err != nil {
		// 地址是用户填的 ⇒ 话直接给他看
		return nil, err
	}
	return c, nil
}

// authResult 是这几个接口统一的返回形状。
//
// 📌 一律**回一个对象、带 ok**，而不是"成功回空、失败抛异常"：
// 页面那边所有分支都长得一样，少一处漏判就可能把失败当成功 ✓
type authResult map[string]any

func authOK(email string) authResult {
	return authResult{"ok": true, "error": "", "email": email}
}

func authFail(err error) authResult {
	return authResult{"ok": false, "error": err.Error(), "email": ""}
}

// AccountState 给页面：本机现在算不算「已登录」。
//
// 📌 **不发网络请求，只看本地有没有令牌** —— 启动时要是先等一次网络往返，
// 窗口就得晚几百毫秒到几秒才出来，用户看到的是"点了图标没反应" ✗
// 令牌到底还有效没有，交给 VerifyAccount 在后台悄悄验 ✓
func (b *backend) AccountState() map[string]any {
	return map[string]any{
		"loggedIn": identity.Token() != "",
		"server":   loginBase(),
	}
}

// VerifyAccount 在后台验一下本地令牌还有没有效，顺便把邮箱取回来。
//
// 三种结果，页面要分得清：
//
//	{ok:true,  email:...}       ← 还登录着
//	{expired:true}              ← 令牌不认了（过期 / 被停用 / 被强制下线）⇒ 本地清掉
//	{ok:false, error:...}       ← 网络不通之类 —— **不要清令牌**！
//	                              网断了一下就把人登出，下次还得重新输密码，纯属添乱 ✗
func (b *backend) VerifyAccount() map[string]any {
	tok := identity.Token()
	if tok == "" {
		return map[string]any{"ok": false, "error": "还没登录", "expired": true}
	}
	c, err := newLoginClient()
	if err != nil {
		return authResult{"ok": false, "error": err.Error(), "email": "", "expired": false}
	}
	a, err := c.Me(tok)
	if err != nil {
		var se *loginapi.ServerErr
		if errors.As(err, &se) && (se.Status == 401 || se.Status == 403) {
			// 服务器明确说"这个令牌不认" ⇒ 本地留着也没用，清掉让用户重新登 ✓
			_ = identity.ClearToken()
			return map[string]any{"ok": false, "expired": true,
				"error": "登录已过期，请重新登录", "email": ""}
		}
		return authResult{"ok": false, "error": err.Error(), "email": "", "expired": false}
	}
	return authOK(a.Email)
}

// SendCode 让服务器往这个邮箱发一封验证码。返回错误文案，空 = 发出去了。
//
// purpose："login" 或 "signup"。
// ⚠️ signup 那条**必须带邀请码**（服务器那边也是这么卡的）——
// 不带的话这个接口就成了"谁都能让我们群发" ✗
func (b *backend) SendCode(email, purpose, invite string) string {
	c, err := newLoginClient()
	if err != nil {
		return err.Error()
	}
	if err := c.SendCode(email, purpose, invite); err != nil {
		return err.Error()
	}
	return ""
}

// SignUp 用邀请码 + 邮箱验证码注册（服务器那边注册即登录）。
func (b *backend) SignUp(invite, email, password, mailCode string) authResult {
	c, err := newLoginClient()
	if err != nil {
		return authFail(err)
	}
	a, err := c.SignUp(invite, email, password, mailCode)
	if err != nil {
		return authFail(err)
	}
	if err := identity.SetToken(a.Token); err != nil {
		// 令牌存不下来 ⇒ 这次登录是"一次性的"，重启就得重登。
		// 不能装作没事：得让用户知道，这里的错法只有诚实告诉他 ✓
		return authResult{"ok": false, "email": "", "error": "注册成功了，但登录状态存不下来：" + err.Error()}
	}
	return authOK(a.Email)
}

// LoginByCode 邮箱 + 验证码登录（不用密码的那条路）。
func (b *backend) LoginByCode(email, mailCode string) authResult {
	c, err := newLoginClient()
	if err != nil {
		return authFail(err)
	}
	a, err := c.LoginByCode(email, mailCode)
	if err != nil {
		return authFail(err)
	}
	if err := identity.SetToken(a.Token); err != nil {
		return authResult{"ok": false, "email": "", "error": "登录成功了，但登录状态存不下来：" + err.Error()}
	}
	return authOK(a.Email)
}

// Login 邮箱 + 密码登录。
func (b *backend) Login(email, password string) authResult {
	c, err := newLoginClient()
	if err != nil {
		return authFail(err)
	}
	a, err := c.Login(email, password)
	if err != nil {
		return authFail(err)
	}
	if err := identity.SetToken(a.Token); err != nil {
		return authResult{"ok": false, "email": "", "error": "登录成功了，但登录状态存不下来：" + err.Error()}
	}
	return authOK(a.Email)
}

// Logout 退出登录。
//
// 🩸 **本地的令牌无论如何都要清掉** —— 服务器那边作废失败（比如没网）也得清。
// 不清的话用户点"退出"没反应，会以为退不出去；而且下次启动还得先撞一次 401 ✗
func (b *backend) Logout() authResult {
	tok := identity.Token()
	if tok != "" {
		if c, err := newLoginClient(); err == nil {
			// 失败就算了：这一步只是"顺手告诉服务器别留着这个会话"
			_ = c.Logout(tok)
		}
	}
	if err := identity.ClearToken(); err != nil {
		return authResult{"ok": false, "error": "清本地登录状态失败：" + err.Error()}
	}
	return authResult{"ok": true}
}

// SetLoginServer 由页面调用：改登录服务器地址。返回错误文案，空 = 成功。
//
// 地址先过一遍校验（见 loginapi.CheckBase）—— 尤其是"非本机不许用 http"。
// 让一个错地址存进去的话，下一版客户端一启动就是"连不上"，而用户完全不知道
// 是自己上次填错了什么 ✗
func (b *backend) SetLoginServer(url string) string {
	norm, err := loginapi.CheckBase(url)
	if err != nil {
		return err.Error()
	}
	if err := updateSettings(func(s *Settings) { s.LoginBase = norm }); err != nil {
		return "保存失败：" + err.Error()
	}
	return ""
}

// ResetPassword 用邮箱验证码改密码（忘记密码）。返回错误文案，空 = 成功 ✓
//
// 📌 成功后**不自动登录**：后端只改密码、不返回令牌 ✓ 界面会切回登录页让他登一次 ✓
// 📌 服务器顺手撤销了他所有会话 ⇒ 这台机器上原来那个令牌也作废了 ✓
func (b *backend) ResetPassword(email, code, pass string) string {
	c, err := newLoginClient()
	if err != nil {
		return err.Error()
	}
	if err := c.ResetPassword(email, code, pass); err != nil {
		return err.Error()
	}
	return ""
}

// SignupMode 问服务器"注册要不要邀请码"。
//
// 返回 "" 表示"要"（默认，安全那侧）、"可选" 表示"不要"、出错返回 "?"
// —— 界面据此在"邀请码"旁边写不写「（可选）」✓
//
// 📌 出错时当"要"处理：宁可多要求一个码，也别让人以为可以留空然后被拒 ✓
func (b *backend) SignupMode() string {
	c, err := newLoginClient()
	if err != nil {
		return "?"
	}
	needsCode, err := c.SignupMode()
	if err != nil {
		return "?"
	}
	if needsCode {
		return ""
	}
	return "可选"
}
