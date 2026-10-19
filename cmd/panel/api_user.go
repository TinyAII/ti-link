// 面向**客户端**的接口（GUI / 命令行用）。
//
// 凭据走 `Authorization: Bearer <token>`，**不用 cookie** ——
// 客户端是原生程序，没有"浏览器自动带 cookie"这回事，也就没有 CSRF 问题 ✓
// 这套接口和后台那套共用同一个账号库、同一种会话令牌。
package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"p2plink/internal/auth"
)

// signupReq 是注册请求。
//
// 📌 字段名从 `code` 改成了 `invite`：现在有**两种码**了
// （邀请码 / 邮箱验证码），再叫 `code` 谁都分不清哪个是哪个 ✗
type signupReq struct {
	Invite   string `json:"invite"` // 邀请码（管理员发的）
	Email    string `json:"email"`
	Password string `json:"password"`
	MailCode string `json:"mailCode"` // 邮箱验证码（证明这个邮箱是他的）
}

// loginReq 是登录请求。**两条路二选一**：
//
//	{email, password} → 密码登录
//	{email, mailCode} → 验证码登录
//
// 判据就是"mailCode 是不是空的" ✓ 两个都填的话按验证码那条走
type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	MailCode string `json:"mailCode"`
}

// sendCodeReq 是"给我发个验证码"。
//
// purpose:
//
//	"signup" → 要**同时带上邀请码**。没邀请码的请求一律不发 ——
//	           不然这个接口就是一台对外开放的群发机 ✗
//	"login"  → 要求这个邮箱**已经注册过**，所以也只能发给自己的用户
type sendCodeReq struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose"`
	Invite  string `json:"invite"`
}

// authResp 是登录/注册成功回来的东西。
//
// 🩸 里面**只有**令牌和账号的基本信息。没有邮箱之外的个人信息，
// 也没有任何"服务器知道你在哪"的东西 —— 这条跟日志打码是同一件事 ✓
type authResp struct {
	// Token / ExpiresAt 带 omitempty：`/api/me` 那种"只是查一下我是谁"的响应里
	// 不该出现一个**空的** token 字段 —— 客户端万一照着读了，会拿空串去连服务器，
	// 表现是"明明登录了却说没登录"，很难查 ✗
	Token     string `json:"token,omitempty"`
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresAt int64  `json:"expiresAt,omitempty"`
}

// expiresAt 跟 auth 那边保持一致的算法。
//
// 📌 之所以能这么算：auth 的注册/登录都是拿**我们传进去的 now** 算到期时间的，
// 这里用同一个 now 加同一个 SessionTTL，结果必然一样 ✓
// （真不一致的话客户端会提前要求重登 —— 是"多登一次"而不是"用不了"，安全侧 ✓）
func (s *Server) expiresAt() int64 { return s.now().Add(auth.SessionTTL).Unix() }

func (s *Server) apiSignup(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	ip := clientIP(r)
	if ok, retry := s.rlSignupIP.allow(ip, s.now()); !ok {
		retryAfter(w, retry)
		logLine(r, http.StatusTooManyRequests, "注册太频繁")
		return
	}
	var req signupReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	// ⭐ 先验**邮箱验证码**（证明这个邮箱真是他的），再走注册。
	// 🩸 次序不能反：验证码是"这个人拥有这个邮箱"的唯一凭据，
	// 先建账号再验的话，码错了就留下一个"邮箱没验过"的账号 ✗
	// （而且那个账号已经吃掉一张邀请码了 —— 邀请码是一次性的）
	if ok, why := s.mailCodes.verify(auth.NormalizeEmail(req.Email), req.MailCode, s.now()); !ok {
		logLine(r, http.StatusForbidden, "注册：邮箱验证码没过")
		writeErr(w, http.StatusForbidden, why)
		return
	}
	a, tok, err := s.st.SignUp(req.Invite, req.Email, req.Password, s.domainList(), s.now())
	if err != nil {
		code, msg := signupErrCode(err)
		if code >= 500 {
			log.Printf("面板：注册失败（内部错误）: %v", err)
		}
		logLine(r, code, "注册被拒")
		writeErr(w, code, msg)
		return
	}
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, authResp{
		Token: tok, ID: a.ID, Email: a.Email, Role: a.Role, ExpiresAt: s.expiresAt(),
	})
}

// apiSendCode 发一封验证码邮件。
//
// 🩸🩸 这个接口是**唯一会让服务器主动往外发信**的地方，所以它有三道闸，
// 一道都不能少（少任何一道，它就能被当成一台对外群发的机器）：
//
//	① signup 必须带**有效邀请码** ⇒ 陌生人拿不到码就发不出去
//	② login 必须**邮箱已注册** ⇒ 只能发给自己的用户，不能拿来给别人发垃圾
//	③ 每个邮箱 / 每个 IP 每小时的次数上限（在 mailCodeStore 里，同一把锁里判）
//
// 另外还有一层：请求本身按 IP 限流 ✓
func (s *Server) apiSendCode(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	ip := clientIP(r)
	if ok, retry := s.rlSendCodeIP.allow("ip:"+ip, s.now()); !ok {
		retryAfter(w, retry)
		logLine(r, http.StatusTooManyRequests, "要验证码太频繁")
		return
	}
	if !s.mail.ready() {
		// 📌 没配发信就**老实说**，别假装发出去了 ——
		// 那样用户会对着一个永远收不到的码反复试 ✗
		logLine(r, http.StatusServiceUnavailable, "没配发信")
		writeErr(w, http.StatusServiceUnavailable, "服务器还没开发验证码登录，请用密码登录")
		return
	}
	var req sendCodeReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	ne := auth.NormalizeEmail(req.Email)
	if msg := auth.CheckEmail(ne, s.domainList()); msg != "" {
		logLine(r, http.StatusBadRequest, "邮箱不合规")
		writeErr(w, http.StatusBadRequest, msg)
		return
	}

	switch req.Purpose {
	case "signup":
		// ① 邀请码必须有效。**发信之前就查** —— 不然白白发一封出去
		//
		// 🩸 2026-10-17：管理员可以把注册改成"开放"（不需要邀请码）。
		//    那种情况下**这一关要跳过** ✗ 不然用户点了"发验证码"直接被拒，
		//    注册流程根本走不到注册那一步 ⇒ 功能等于没做 ✗
		//    但填了码的话仍然校验（填错了要早点告诉他 ✓）
		open := s.st.SignupOpen()
		if !open || strings.TrimSpace(req.Invite) != "" {
			if _, err := s.st.CheckCode(req.Invite); err != nil {
				logLine(r, http.StatusForbidden, "要验证码：邀请码无效")
				writeErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
	case "login", "reset":
		// ② 邮箱必须是已注册的（且没被停用）
		//    📌 reset（找回密码）跟 login 是同一个条件 —— 没注册的邮箱根本收不到码 ✓
		//      但**场景不同、信也不同**（主题和措辞分开，见 mailSceneOf）✓
		if _, ok := s.st.FindByEmail(ne); !ok {
			// ⚠️ 这里确实泄露了"这个邮箱有没有注册过"。
			// 权衡过：注册要邀请码 ⇒ 外人注册不了，这个信息的价值很低；
			// 而"不告诉他的话"他会对着收不到的码反复点，代价更实在 ✓
			logLine(r, http.StatusNotFound, "要验证码：邮箱没注册过")
			writeErr(w, http.StatusNotFound, "这个邮箱还没注册过")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}

	// ③ 次数闸（同一把锁里判 + 计数，见 mailCodeStore.beginSend 的注释）
	code, why := s.mailCodes.beginSend(ne, ip, s.now())
	if why != "" {
		logLine(r, http.StatusTooManyRequests, "要验证码被闸住")
		writeErr(w, http.StatusTooManyRequests, why)
		return
	}

	// ⚠️ 发信这一步可能要十几秒（连 SMTP、认证、发数据都有时限）。
	// 但它**不能**塞进上面那把锁里 —— 拿着锁去等网络，别人全被堵住 ✗
	sc := mailSceneOf(req.Purpose)
	if err := s.mail.send(ne, sc.Subject,
		codeMailBody(sc, code, codeTTL), codeMailHTML(sc, code, codeTTL)); err != nil {
		// 🩸 发不出去就把刚存下的码**吊销掉**，否则用户拿不到码、
		// 却还得等 60 秒重发间隔 + 白占一次每小时额度 ✗
		s.mailCodes.revoke(ne)
		logLine(r, http.StatusBadGateway, "发信失败")
		writeErr(w, http.StatusBadGateway, "验证码没发出去（服务器发信出问题了），请稍后再试")
		return
	}
	// 日志里**只说发了一封**，不写码、不写完整邮箱 ✗
	logLine(r, http.StatusOK, "验证码已发出")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	ip := clientIP(r)
	if ok, retry := s.rlLoginIP.allow("ip:"+ip, s.now()); !ok {
		retryAfter(w, retry)
		logLine(r, http.StatusTooManyRequests, "登录太频繁（按 IP）")
		return
	}
	var req loginReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	// 按邮箱再限一道：换 IP 重试也躲不过（宿舍/机房出口 IP 一大把）
	ne := auth.NormalizeEmail(req.Email)
	key := "mail:" + ne
	if ok, retry := s.rlLoginEmail.allow(key, s.now()); !ok {
		retryAfter(w, retry)
		logLine(r, http.StatusTooManyRequests, "登录太频繁（按邮箱）")
		return
	}

	// ⭐ 两条路二选一：给了 mailCode 就走验证码，否则走密码。
	var a *auth.Account
	var tok string
	var err error
	if strings.TrimSpace(req.MailCode) != "" {
		// 🩸 次序：**先验码、再发会话**。
		// verify 成功即把码作废（单次使用）✓
		if ok, why := s.mailCodes.verify(ne, req.MailCode, s.now()); !ok {
			logLine(r, http.StatusUnauthorized, "验证码没过")
			writeErr(w, http.StatusUnauthorized, why)
			return
		}
		// 走到这儿说明"他手里有这个邮箱收到的码" ⇒
		// 用那个**只此一处**的口子发会话（见 auth.LoginTrustingEmail 的注释）✓
		a, tok, err = s.st.LoginTrustingEmail(ne, s.now())
	} else {
		a, tok, err = s.st.Login(req.Email, req.Password, s.now())
	}
	if err != nil {
		// 登录成功之外的一律按"凭据不对"回，只有"停用"说实话 ——
		// 停用了的人**必须**知道为什么进不去，不然他会一直以为是自己密码打错了 ✗
		if errors.Is(err, auth.ErrDisabled) {
			logLine(r, http.StatusForbidden, "账号已停用")
			writeErr(w, http.StatusForbidden, auth.ErrDisabled.Error())
			return
		}
		logLine(r, http.StatusUnauthorized, "凭据不对")
		writeErr(w, http.StatusUnauthorized, auth.ErrBadLogin.Error())
		return
	}
	// 成功了就把失败次数销掉：不然正常用户白天登几次、晚上再登被自己的历史拖累
	s.rlLoginEmail.reset(key)
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, authResp{
		Token: tok, ID: a.ID, Email: a.Email, Role: a.Role, ExpiresAt: s.expiresAt(),
	})
}

// bearerAccount 取出"这个 Bearer 令牌对应谁"。
// 客户端那套接口的鉴权入口就这一个。
func (s *Server) bearerAccount(w http.ResponseWriter, r *http.Request) *auth.Account {
	a, ok := s.st.VerifyToken(bearerToken(r), s.now())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "登录已过期，请重新登录")
		return nil
	}
	return a
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	if err := s.st.Logout(bearerToken(r)); err != nil {
		log.Printf("面板：注销失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	logLine(r, http.StatusOK, "")
	writeOK(w)
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	a := s.bearerAccount(w, r)
	if a == nil {
		logLine(r, http.StatusUnauthorized, "")
		return
	}
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, authResp{ID: a.ID, Email: a.Email, Role: a.Role})
}

type passwordReq struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// apiPassword 改密码。
//
// ⚠️ 改完**所有会话立刻作废**（包括发起这次修改的这一条）——
// 客户端会收到 200，然后用旧令牌继续用就会 401，必须重新登录。
// 这是有意的：改密码在现实里往往就是"我怀疑号被登了" ✓
func (s *Server) apiPassword(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	a := s.bearerAccount(w, r)
	if a == nil {
		logLine(r, http.StatusUnauthorized, "")
		return
	}
	var req passwordReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	switch err := s.st.ChangePassword(a.ID, req.OldPassword, req.NewPassword, s.now()); {
	case err == nil:
		logLine(r, http.StatusOK, "改了密码（全部会话已作废）")
		writeOK(w)
	case errors.Is(err, auth.ErrBadOldPass):
		logLine(r, http.StatusForbidden, "原密码不对")
		writeErr(w, http.StatusForbidden, auth.ErrBadOldPass.Error())
	default:
		// 新密码不合规之类：是用户的问题，照原话说
		logLine(r, http.StatusBadRequest, "新密码不合规")
		log.Printf("改密码失败: %v", err)
		writeErr(w, http.StatusBadRequest, "改密码失败了，稍后再试一次")
	}
}

// domainList 返回当前生效的邮箱域名白名单。
func (s *Server) domainList() []string { return s.domains }

// retryAfter 是 429 的响应：告诉对方等多久再来。
func retryAfter(w http.ResponseWriter, d time.Duration) {
	sec := int(d.Seconds())
	if sec < 1 {
		sec = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(sec))
	// 不说"你被限流了"以外的东西 —— 尤其别说"还剩几次"，
	// 那等于告诉试探的人"再试几次就到头了" ✓
	writeErr(w, http.StatusTooManyRequests, "尝试太频繁了，请稍后再试")
}

// signupErrCode 把 auth 那层的错误翻成 HTTP 状态码。
//
// 🩸 最容易写错的就在最后那个 default：**别把"我们自己的故障"当成"你填错了"**。
// 保存失败却回一句"邀请码无效"，用户会拿着好码反复试到崩溃，
// 而我们这边一条线索都没有 ✗
func signupErrCode(err error) (int, string) {
	switch {
	case errors.Is(err, auth.ErrBadCode), errors.Is(err, auth.ErrEmailTaken):
		// ⚠️ "这个邮箱注册过"确实泄露了一点点信息，但**避不开**：
		// 不告诉他的话，他会拿着那张码反复试，完全不知道问题在哪 ✗
		// 而且这条路要先有一张**有效的邀请码**才走得到 ⇒ 泄露面极小 ✓
		return http.StatusConflict, err.Error()
	case errors.Is(err, auth.ErrDisabled):
		return http.StatusForbidden, err.Error()
	}
	var ie *auth.InputError
	if errors.As(err, &ie) {
		return http.StatusBadRequest, ie.Msg // 用户填的不合规：照原话说
	}
	return http.StatusInternalServerError, "服务器开小差了，请稍后再试"
}

// ── 找回密码（2026-10-17 加）────────────────────────────────
//
// 🩸 唯一不需要旧密码的改密入口 ⇒ 必须验邮箱验证码 ✓
// 📌 发码复用 purpose=login（条件一样：邮箱必须已注册）—— 前端不用另开一个分支 ✓

type resetReq struct {
	Email string `json:"email"`
	Code  string `json:"code"`
	Pass  string `json:"pass"`
}

func (s *Server) apiReset(w http.ResponseWriter, r *http.Request) {
	var req resetReq
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	ne := auth.NormalizeEmail(req.Email)

	// ① 邮箱格式：只用基本校验（不套域名白名单 —— 白名单以后可能改，
	//    这里的目的是"格式对"，不是"允许注册"）✓
	if ne == "" || !strings.Contains(ne, "@") {
		logLine(r, http.StatusBadRequest, "重置密码：邮箱格式不对")
		writeErr(w, http.StatusBadRequest, "邮箱填得不对")
		return
	}

	// ② 验证码：这一步就是"证明邮箱是你的" ✓ 它自带 5 次试错上限 ✓
	if ok, why := s.mailCodes.verify(ne, req.Code, s.now()); !ok {
		logLine(r, http.StatusBadRequest, "重置密码：验证码不对")
		writeErr(w, http.StatusBadRequest, why)
		return
	}

	// ③ 密码本身先自查一遍 —— 这样"至少 8 位"那种提示能直接给用户看，
	//    也不用去猜 auth 返回的是什么错 ✓
	if msg := auth.CheckPassword(req.Pass); msg != "" {
		logLine(r, http.StatusBadRequest, "重置密码：新密码不合规")
		writeErr(w, http.StatusBadRequest, msg)
		return
	}

	// ④ 改密码（会同时撤销这个账号的所有会话 ✓）
	if err := s.st.ResetPassword(ne, req.Pass, s.now()); err != nil {
		// 🩸 不区分"没这个账号"和别的错 —— 别给人当账号枚举用 ✓
		//    （正常流程里没注册的邮箱根本收不到验证码，走不到这一步）
		log.Printf("重置密码失败: %v", err)
		logLine(r, http.StatusBadRequest, "重置密码：失败")
		writeErr(w, http.StatusBadRequest, "改密码没成功，稍后再试一次")
		return
	}
	logLine(r, http.StatusOK, "重置密码成功")
	writeOK(w)
}

// GET /api/signupmode —— 客户端进注册页时问一句"现在要不要邀请码"。
//
// 📌 公开接口：这个开关不是秘密（界面上本来就要写出来）✓
// 🩸 但**别**顺手把账号库别的信息带出去 ✗ 就回这一个字段 ✓
func (s *Server) apiSignupMode(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"needsCode": !s.st.SignupOpen(),
	})
}
