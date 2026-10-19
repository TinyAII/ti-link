// 后台接口（浏览器用）。全部在 `/api/admin/` 下面 ⇒ 一律先过鉴权（见 server.go）。
//
// 这里是"老板一个人用"的地方，所以有几件事跟面向客户端的接口不一样：
//
//	· 看得到**完整**的邮箱（他就是数据的管理者，要他管理就得给他看全 ✓）
//	· 但**看不到**任何地址原文 —— 房间列表里的来源地址是打码的（见 rooms.go）
//	· 写操作要 CSRF（cookie 会自动带上，必须有第二重证明）
package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"p2plink/internal/auth"
)

// ---- 登录 ----

type adminLoginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type adminSessionResp struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	// CSRF 放在响应体里，不另外开一个 cookie。
	// 📌 为什么这样是安全的：跨站的页面**读不到**这个响应（CORS 不放行），
	// 而它自己那份 cookie 因为 SameSite=Strict 根本不会被带上 ⇒ 拿不到令牌 ✓
	CSRF string `json:"csrf"`
}

func (s *Server) apiAdminLogin(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	ip := clientIP(r)
	if ok, retry := s.rlAdminIP.allow("admin:"+ip, s.now()); !ok {
		retryAfter(w, retry)
		logLine(r, http.StatusTooManyRequests, "后台登录太频繁")
		return
	}
	var req adminLoginReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	a, tok, err := s.st.Login(req.Email, req.Password, s.now())
	if err != nil {
		// 回给外面的话**一律一样** —— 别让人从响应里分出
		// "这个号存在但不是管理员"和"这个号压根没有" ✗
		logLine(r, http.StatusUnauthorized, "后台登录失败")
		writeErr(w, http.StatusUnauthorized, "邮箱或密码不对")
		return
	}
	if a.Role != auth.RoleAdmin {
		// 是个普通用户：会话已经发出去了，立刻撤掉，别留着
		_ = s.st.Logout(tok)
		logLine(r, http.StatusUnauthorized, "后台登录失败")
		writeErr(w, http.StatusUnauthorized, "邮箱或密码不对")
		return
	}
	s.rlAdminIP.reset("admin:" + ip)
	setSessionCookie(w, tok)
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, adminSessionResp{Email: a.Email, Role: a.Role, CSRF: s.csrfFor(tok)})
}

func (s *Server) apiAdminLogout(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	if err := s.st.Logout(cookieToken(r)); err != nil {
		log.Printf("面板：后台注销失败: %v", err)
	}
	clearSessionCookie(w)
	logLine(r, http.StatusOK, "")
	writeOK(w)
}

func (s *Server) apiAdminSession(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	a := accountFrom(r)
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, adminSessionResp{Email: a.Email, Role: a.Role, CSRF: s.csrfFor(cookieToken(r))})
}

// ---- 邀请码 ----

type codeRow struct {
	Code      string `json:"code"`
	Display   string `json:"display"`
	Batch     string `json:"batch"`
	Note      string `json:"note"`
	State     string `json:"state"` // unused | used
	UsedEmail string `json:"usedEmail"`
	CreatedAt int64  `json:"createdAt"`
	UsedAt    int64  `json:"usedAt"`
}

func (s *Server) codeRows(batch string) []codeRow {
	byID := map[int64]string{}
	for _, a := range s.st.Accounts() {
		byID[a.ID] = a.Email
	}
	out := make([]codeRow, 0, 64)
	for _, c := range s.st.Codes() {
		if batch != "" && c.Batch != batch {
			continue
		}
		row := codeRow{
			Code: c.Code, Display: auth.FormatCode(c.Code), Batch: c.Batch, Note: c.Note,
			State: "unused", CreatedAt: c.CreatedAt,
		}
		if c.Used() {
			row.State = "used"
			row.UsedAt = c.UsedAt
			row.UsedEmail = byID[c.UsedBy]
		}
		out = append(out, row)
	}
	// 新的排前面（后台用起来顺手）
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (s *Server) apiAdminCodes(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	rows := s.codeRows(r.URL.Query().Get("batch"))
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, map[string]any{"codes": rows, "batches": s.batches()})
}

func (s *Server) batches() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range s.st.Codes() {
		if c.Batch != "" && !seen[c.Batch] {
			seen[c.Batch] = true
			out = append(out, c.Batch)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

type genCodesReq struct {
	N     int    `json:"n"`
	Batch string `json:"batch"`
	Note  string `json:"note"`
}

func (s *Server) apiAdminGenCodes(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	var req genCodesReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	// 批号和备注会进文件、进 CSV、进后台页面 ⇒ 长度和字符都要收一收。
	// （后台是老板自己用，但"只挡普通用户"的校验在这个项目里一律不算数 ✓）
	batch := clipText(req.Batch, 64)
	note := clipText(req.Note, 200)

	codes, err := s.st.GenCodes(req.N, batch, note, s.now())
	if err != nil {
		logLine(r, http.StatusBadRequest, "发码被拒")
		log.Printf("生成邀请码失败: %v", err)
		writeErr(w, http.StatusBadRequest, "生成失败，先看看服务端日志")
		return
	}
	out := make([]codeRow, 0, len(codes))
	for _, c := range codes {
		out = append(out, codeRow{Code: c, Display: auth.FormatCode(c), Batch: batch, Note: note,
			State: "unused", CreatedAt: s.now().Unix()})
	}
	// ⚠️ 日志里只说个数，不把码打出来（日志会被别人看到；码就是凭据）
	logLine(r, http.StatusOK, fmt.Sprintf("发了 %d 个码", len(codes)))
	writeJSON(w, http.StatusOK, map[string]any{"codes": out})
}

type setNoteReq struct {
	Code string `json:"code"`
	Note string `json:"note"`
}

func (s *Server) apiAdminSetNote(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	var req setNoteReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if err := s.st.SetNote(req.Code, clipText(req.Note, 200)); err != nil {
		logLine(r, http.StatusNotFound, "改备注失败")
		writeErr(w, http.StatusNotFound, "找不到这个邀请码")
		return
	}
	logLine(r, http.StatusOK, "")
	writeOK(w)
}

// apiAdminCodesCSV 导出。
//
// 🩸 CSV 有个经典坑：**以 = + - @ 开头的单元格会被 Excel 当公式执行**（公式注入）。
// 备注是手填的，随手粘一句"=1+1"就能踩上。做法是在前面加一个单引号，
// Excel 会把它当纯文本，而其它工具里那一撇看得见、不影响认人 ✓
func (s *Server) apiAdminCodesCSV(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	name := "邀请码-" + s.now().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="invite-codes.csv"; filename*=UTF-8''`+urlEscape(name))
	// 📌 加 BOM：不加的话 Excel 打开中文全是乱码（它的默认编码是本地代码页）
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"邀请码", "批号", "备注", "状态", "使用者邮箱", "创建时间", "使用时间"})
	for _, c := range s.codeRows(r.URL.Query().Get("batch")) {
		state := "未使用"
		if c.State == "used" {
			state = "已使用"
		}
		_ = cw.Write([]string{
			csvSafe(c.Display), csvSafe(c.Batch), csvSafe(c.Note), state, csvSafe(c.UsedEmail),
			ts(c.CreatedAt), ts(c.UsedAt),
		})
	}
	cw.Flush()
	logLine(r, http.StatusOK, "导出 CSV")
}

// csvSafe 防公式注入：以危险字符开头的单元格前面加一撇。
func csvSafe(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func ts(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("2006-01-02 15:04:05")
}

// ---- 账号 ----

type accountRow struct {
	ID         int64  `json:"id"`
	Email      string `json:"email"`
	Role       string `json:"role"`
	Status     string `json:"status"`
	Code       string `json:"code"`
	CreatedAt  int64  `json:"createdAt"`
	DisabledAt int64  `json:"disabledAt"`
	Sessions   int    `json:"sessions"`
}

func (s *Server) apiAdminAccounts(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	list := s.st.Accounts()
	out := make([]accountRow, 0, len(list))
	for _, a := range list {
		out = append(out, accountRow{
			ID: a.ID, Email: a.Email, Role: a.Role, Status: a.Status, Code: a.Code,
			CreatedAt: a.CreatedAt, DisabledAt: a.DisabledAt,
			Sessions: s.st.SessionCount(a.ID, s.now()),
		})
	}
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

type setStatusReq struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

func (s *Server) apiAdminSetStatus(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	var req setStatusReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	me := accountFrom(r)
	if req.ID == me.ID {
		writeErr(w, http.StatusBadRequest, "不能停用你自己 —— 那样就没人能进后台了")
		return
	}
	// 📌 这里**故意没有**"不能停掉最后一个管理员"那一道 —— 不是漏了，是想清楚之后删的：
	//
	//    能走到这行的请求者，必然是**正在用的管理员**（requireAdmin 卡着），
	//    而他停不了自己 ⇒ 停掉任何别人之后，至少还剩他自己 ✓
	//    也就是说"最后一个管理员被停掉"这个场景**根本到不了这里**。
	//
	//    原来确实写了一道，反证时才发现它永远不触发 —— 而永远不触发就意味着
	//    **测不到**（写了也留不下一条会红的测试）⇒ 那是死代码 ✗
	//    死代码比没有代码更坏：读的人会以为"这个场景已经有人管了" ✓
	//    2026-10-15 拆掉，把结论写成上面这两行 ✓
	if err := s.st.SetStatus(req.ID, req.Status, s.now()); err != nil {
		logLine(r, http.StatusNotFound, "改状态失败")
		writeErr(w, http.StatusNotFound, "找不到这个账号")
		return
	}
	logLine(r, http.StatusOK, "改了账号状态")
	writeOK(w)
}

type idReq struct {
	ID int64 `json:"id"`
}

// apiAdminRevoke 强制下线：这个账号手上所有令牌立刻作废（本人下次请求要重新登录）。
func (s *Server) apiAdminRevoke(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	var req idReq
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if err := s.st.RevokeSessions(req.ID, s.now()); err != nil {
		logLine(r, http.StatusNotFound, "强制下线失败")
		writeErr(w, http.StatusNotFound, "找不到这个账号")
		return
	}
	// 如果踢的是自己，顺手把 cookie 也清了 ——
	// 不清的话浏览器还揣着一张废令牌，页面会一直在"已登录"和 401 之间打转 ✗
	if req.ID == accountFrom(r).ID {
		clearSessionCookie(w)
	}
	logLine(r, http.StatusOK, "强制下线")
	writeOK(w)
}

// apiAdminRoomsClose 从后台**强制关掉**一个房间（把里面的人断开）。
//
// 🩸 这是个能踢人的动作 ⇒ 走后台那套 cookie + CSRF（`needAuth=true`），
// 而且判定在服务端 —— 前端把按钮藏起来不算防线 ✗
//
// 📌 真正动手的是信令侧（房间是它的内存态）：这儿只是转发一句"关掉它"，
// 它自己走那条**房主断开**的清理路径（通知成员 / 删表 / 还"单 IP 房间名额"）✓
func (s *Server) apiAdminRoomsClose(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	var req struct {
		Room string `json:"room"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if req.Room == "" {
		writeErr(w, http.StatusBadRequest, "没说要关哪个房间")
		return
	}
	s.proxySignalPost(w, r, "/rooms/close", map[string]string{"room": req.Room})
}

// apiAdminMetrics 把概览页要的**历史序列**转给前端（折线图用）。
//
// 📌 range 要**原样透传**给信令侧（它那边才知道自己的历史有多长、
// 并且会把范围夹到合法区间）—— 面板这边不自己判，免得两边规则不一致 ✗
func (s *Server) apiAdminMetrics(w http.ResponseWriter, r *http.Request) {
	path := "/metrics"
	if q := r.URL.RawQuery; q != "" {
		path += "?" + q
	}
	s.proxySignal(w, r, path)
}

// roomCodeRe 限制房间号的形状（跟信令侧那套校验同一个口径）。
//
// 🩸 面板这一层也要挡一道：房间号要拼进**查询串**发给信令侧的
// ⇒ 不挡的话奇怪的字符能进到那条 URL 里 ✗（多一道门不多余）
var roomCodeRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,32}$`)

// apiAdminRoomDetail 取某个房间的详情（后台"点进去"那一页）。
//
// 📌 为什么详情单独一个端点、而不是房间列表里一次给全：
// 成员明细比列表重得多（每房一份），列表每秒刷一次 ⇒ 塞进去白费 ✓
func (s *Server) apiAdminRoomDetail(w http.ResponseWriter, r *http.Request) {
	room := strings.TrimSpace(r.URL.Query().Get("room"))
	if room == "" {
		writeErr(w, http.StatusBadRequest, "没说看哪个房间")
		return
	}
	if !roomCodeRe.MatchString(room) {
		writeErr(w, http.StatusBadRequest, "房间号格式不对")
		return
	}
	s.proxySignal(w, r, "/rooms/detail?room="+url.QueryEscape(room))
}

// apiAdminRoomKick 把一个成员请出房间。
//
// 🩸 **座位 0 是房主，不能单独踢** —— 那条判定在信令侧（这儿只是转发）；
// 前端把按钮藏起来不算防线 ✗
func (s *Server) apiAdminRoomKick(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	var req struct {
		Room string `json:"room"`
		Seat *int   `json:"seat"` // 用指针：0 是合法座位，不能跟"没传"混 ✗
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式不对")
		return
	}
	if req.Room == "" || req.Seat == nil {
		writeErr(w, http.StatusBadRequest, "要说清哪个房间、哪个座位")
		return
	}
	if !roomCodeRe.MatchString(req.Room) {
		writeErr(w, http.StatusBadRequest, "房间号格式不对")
		return
	}
	s.proxySignalPost(w, r, "/rooms/kick", map[string]any{"room": req.Room, "seat": *req.Seat})
}

// ---- 房间 / 指标（从信令侧的本机端点取，见 rooms.go） ----

func (s *Server) apiAdminRooms(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	s.proxySignal(w, r, "/rooms")
}

// apiAdminRoomHistory 由后台「历史房间」页调用：转发给信令服务的 /history。
//
// 📌 查询串原样带过去（q=关键词、days=看几天）——
// 在面板这边再解析一遍等于把过滤逻辑抄两份，迟早不一致 ✗
func (s *Server) apiAdminRoomHistory(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	path := "/history"
	if q := r.URL.RawQuery; q != "" {
		path += "?" + q
	}
	s.proxySignal(w, r, path)
}

func (s *Server) apiAdminStats(w http.ResponseWriter, r *http.Request) {
	defer drain(r)
	s.proxySignal(w, r, "/stats")
}

// clipText 收一收自由文本：去掉控制字符、按字符数截断。
//
// ⚠️ 批号和备注会进 JSON、进 CSV、进后台页面 —— 控制字符（换行、制表、退格）
// 在这三个地方都会捣乱（CSV 里还能撑出额外的行）⇒ 一律去掉 ✓
func clipText(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	var sb strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		sb.WriteRune(r)
	}
	rs := []rune(sb.String())
	if len(rs) > maxRunes {
		rs = rs[:maxRunes]
	}
	return string(rs)
}

// urlEscape 给 Content-Disposition 里的 filename* 用（RFC 5987 的百分号编码）。
// 中文文件名必须走它，不然浏览器存下来的名字是乱码 —— 或者干脆截断。
func urlEscape(s string) string { return url.PathEscape(s) }

// ── 注册设置（2026-10-17）──────────────────────────────────
//
// 目前只有一个开关：**注册是否还需要邀请码**。
// 🩸 它落在 accounts.json 的 settings 里（跟账号一起存 ✓ 单写者 = 面板 ✓）

// GET /api/admin/settings
func (s *Server) apiAdminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"signupOpen": s.st.SignupOpen(),
	})
}

// POST /api/admin/settings  {"signupOpen": true|false}
//
// 📌 改完立刻生效：面板读内存、信令根本不关心注册 ⇒ 不用重启任何东西 ✓
func (s *Server) apiAdminSetSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SignupOpen bool `json:"signupOpen"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if err := s.st.SetSignupOpen(req.SignupOpen); err != nil {
		// 🩸 内部错误只进日志，别回吐路径给前端（跟 P3 里 L1 那条一致）✓
		log.Printf("改注册设置失败: %v", err)
		writeErr(w, http.StatusInternalServerError, "保存失败了，先看看服务端日志")
		return
	}
	// 审计：**谁**把它打开了。这种开关一旦出事要能追到人 ✓
	who := "?"
	if a := accountFrom(r); a != nil {
		who = a.Email
	}
	if req.SignupOpen {
		log.Printf("管理员 %s 把注册改成【开放】（不需要邀请码）", who)
	} else {
		log.Printf("管理员 %s 把注册改成【需要邀请码】", who)
	}
	writeJSON(w, http.StatusOK, map[string]any{"signupOpen": req.SignupOpen})
}
