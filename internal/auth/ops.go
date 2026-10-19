// 业务动作：注册（兑换邀请码）、登录、校验、改密、停用、发码。
//
// 这一层是**唯一**允许改账号数据的地方。所有写操作都在 s.mu 里做 ——
// 因为写者只有一个进程，那把互斥锁就是"事务"：不用数据库事务也能保证
// 「码标成已用」和「建账号」是一起生效的，中间不会被别人插进来 ✓
//
// 🩸 只有一条铁律要记：**bcrypt 绝不放在锁里**。
// 它一次要几十毫秒，放进临界区就等于"一个人登录，所有人排队"。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost 是 bcrypt 的代价参数。
//
// 取 10（默认值）而不是更硬的 12：12 对离线爆破大约多 4 倍成本，
// 但**对在线的 CPU 消耗也多 4 倍** —— 而我们这台是 4h4g，
// 登录接口又是公开的。真正挡住在线尝试的是限流（见 cmd/panel），
// 不是把单次哈希拖慢。
// 📌 代价 10 配 8 位以上随机密码，离线爆破依然不现实 ✓
const bcryptCost = bcrypt.DefaultCost

// tokenBytes 是会话令牌的随机长度（32 字节 = 256 位）。
// 它不是"密码"，是**不透明凭据**：够长就行，不需要人记。
const tokenBytes = 32

// MaxCodesPerCall 一次最多生成多少个码。
// 挡的是"手滑填了 100000"把内存和文件撑爆。
const MaxCodesPerCall = 500

// newToken 生成一个会话令牌（原文，只在这一次返回给客户端）。
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken 是令牌在**盘上**的形式。
//
// 🩸 存哈希不存原文：文件被拿走时，攻击者得到的是一串用不上的 sha256，
// 而不是一串能直接连服务器的凭据 ✓
// （这里用 sha256 而不是 bcrypt：令牌是 32 字节**随机数**，没有"猜"的余地，
// 不需要慢哈希；而校验是每个连接都要走的路径，慢了就是白慢 ✓）
func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// CheckPassword 校验密码，返回能直接给用户看的中文（空 = 通过）。
func CheckPassword(p string) string {
	if len([]rune(p)) < MinPassRunes {
		return fmt.Sprintf("密码至少 %d 位", MinPassRunes)
	}
	if len(p) > MaxPassBytes {
		return "密码太长了（最多 72 个字节，一个汉字算 3 个）"
	}
	return ""
}

// dummyHash 用于"账号不存在时也照样算一次 bcrypt"。
//
// 🩸 不这么做的话，登录接口就变成了一个**计时器**：不存在的账号立刻返回，
// 存在的账号要 60ms 才返回 ⇒ 攻击者靠响应时间就能问出"这个邮箱注册过没有" ✓
// 虽然对"猜密码"没帮助，但"哪些人用了这个服务"本身就是不该白给的信息。
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("timing-equalizer"), bcryptCost)
	return h
})

// ---- 发码 ----

// GenCodes 生成 n 个新邀请码。
//
// n 是**一次调用**生成的数量（批量导出那件事靠它）；上界见 MaxCodesPerCall。
func (s *Store) GenCodes(n int, batch, note string, now time.Time) ([]string, error) {
	if n <= 0 {
		return nil, errors.New("至少要生成 1 个")
	}
	if n > MaxCodesPerCall {
		return nil, fmt.Errorf("一次最多生成 %d 个", MaxCodesPerCall)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mustWritable("发码"); err != nil {
		return nil, err
	}

	out := make([]string, 0, n)
	for len(out) < n {
		code, err := NewCode()
		if err != nil {
			return nil, err
		}
		if _, dup := s.codes[code]; dup {
			// 撞了（2^50 里撞一次）—— 换一个接着来，不报错。
			continue
		}
		s.codes[code] = &InviteCode{
			Code: code, Batch: batch, Note: note, CreatedAt: now.Unix(),
		}
		out = append(out, code)
	}
	if err := s.saveAccountsLocked(); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- 注册（兑换邀请码） ----

// SignUp 用邀请码注册一个账号，并**顺手登录**（返回 token）。
//
// 顺带登录是有意的：注册完还让用户再输一遍刚设的密码，纯属找骂。
//
// 并发的正确性：全流程分两段，中间那段（bcrypt）在锁外。
//
//	第一段（锁内）：查码、查邮箱 —— 便宜的检查，先挡掉绝大部分
//	第二段（锁外）：bcrypt 哈希
//	第三段（锁内）：**重新查一遍**码和邮箱再落库
//
// 🩸 第三段的重复检查不能省：两个人同时拿**同一个码**注册时，
// 第一段两人都会通过；真正的裁决在第三段 —— 谁先拿到锁谁赢，
// 后到的会发现码已经被用掉了 ✓
// （这也是"一码一人"的实现点，对应 ops_test.go 的并发反证测试。）
func (s *Store) SignUp(code, email, password string, domains []string, now time.Time) (*Account, string, error) {
	nc := NormalizeCode(code)
	ne := NormalizeEmail(email)
	// 🩸 **判"用户填没填码"必须用原始输入**：
	//    NormalizeCode 会把非法字符滤掉 ⇒ 用户填了一串乱码会变成空串，
	//    跟"没填"长得一模一样 ✗ （测试里 "瞎编的码" 就踩到了这个）
	//    ⇒ 那会让"填了错码"被静默当成"没填" ⇒ 用户以为码用上了，实际没有 ✗
	gaveCode := strings.TrimSpace(code) != ""

	s.mu.Lock()
	open := s.signupOpen
	c := s.codes[nc]
	// 🩸 见文件头那三种情况 ✓
	codeOK := true
	if !open || gaveCode {
		codeOK = c != nil && !c.Used()
	}
	_, taken := s.accounts[ne]
	s.mu.Unlock()

	if !codeOK {
		return nil, "", ErrBadCode
	}
	if msg := CheckEmail(ne, domains); msg != "" {
		return nil, "", inputErr(msg)
	}
	if msg := CheckPassword(password); msg != "" {
		return nil, "", inputErr(msg)
	}
	if taken {
		return nil, "", ErrEmailTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, "", fmt.Errorf("密码处理失败: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mustWritable("注册"); err != nil {
		return nil, "", err
	}
	// ⭐ 裁决点（重判）：规则跟上面那道**必须一致** —— 只改一处的话，
	// 两个请求并发时能钻过去（先生效的那次把设置改了，第二次还按老判断走）✗
	c = s.codes[nc]
	if !s.signupOpen || gaveCode {
		if c == nil || c.Used() {
			return nil, "", ErrBadCode
		}
	}
	if _, exists := s.accounts[ne]; exists {
		return nil, "", ErrEmailTaken
	}
	tok, err := newToken()
	if err != nil {
		return nil, "", err
	}

	a := &Account{
		ID:        s.nextID,
		Email:     ne,
		PassHash:  string(hash),
		Role:      RoleUser,
		Status:    StatusActive,
		CreatedAt: now.Unix(),
	}
	s.nextID++
	s.accounts[ne] = a
	s.byID[a.ID] = a
	// 📌 开放注册且没填码 ⇒ c 是 nil，什么也不用做（Account.Code 留空串）✓
	if c != nil {
		a.Code = c.Code
		c.UsedBy = a.ID
		c.UsedAt = now.Unix()
	}
	s.sessions[hashToken(tok)] = &Session{
		TokenHash: hashToken(tok),
		AccountID: a.ID,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(SessionTTL).Unix(),
	}

	// 账号表先落盘：万一会话表写失败，账号本身是有效的（用户还能登录），
	// 比"码用掉了但账号没建成"好得多。
	if err := s.saveAccountsLocked(); err != nil {
		// 落盘失败 ⇒ 内存里得回滚，不然进程内状态和盘上不一致。
		delete(s.accounts, ne)
		delete(s.byID, a.ID)
		s.nextID--
		c.UsedBy, c.UsedAt = 0, 0
		delete(s.sessions, hashToken(tok))
		return nil, "", fmt.Errorf("保存失败: %w", err)
	}
	if err := s.saveSessionsLocked(); err != nil {
		return nil, "", fmt.Errorf("账号已建好，但会话没能保存（请直接登录）: %w", err)
	}
	return a, tok, nil
}

// ---- 登录 ----

// Login 校验邮箱与密码，成功就发一个新 token。
func (s *Store) Login(email, password string, now time.Time) (*Account, string, error) {
	ne := NormalizeEmail(email)

	s.mu.Lock()
	if err := s.mustWritable("登录"); err != nil {
		s.mu.Unlock()
		return nil, "", err
	}
	s.reloadAccountsIfChanged()
	a := s.accounts[ne]
	var hash string
	if a != nil {
		hash = a.PassHash
	}
	s.mu.Unlock()

	// ⚠️ 锁外算哈希
	if a == nil {
		// 不存在的账号：也照样算一次，把响应时间拉平（见 dummyHash 的注释）。
		_ = bcrypt.CompareHashAndPassword(dummyHash(), []byte(password))
		return nil, "", ErrBadLogin
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, "", ErrBadLogin
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// 复核：上面那几十毫秒里账号可能已经被停用了。
	cur := s.byID[a.ID]
	if cur == nil || cur.Status != StatusActive {
		return nil, "", ErrDisabled
	}
	tok, err := newToken()
	if err != nil {
		return nil, "", err
	}
	s.pruneSessionsLocked(now)
	s.sessions[hashToken(tok)] = &Session{
		TokenHash: hashToken(tok),
		AccountID: cur.ID,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(SessionTTL).Unix(),
	}
	if err := s.saveSessionsLocked(); err != nil {
		delete(s.sessions, hashToken(tok))
		return nil, "", fmt.Errorf("保存失败: %w", err)
	}
	return cur, tok, nil
}

// CheckCode 只看一张邀请码"能不能用"，**不消费它**。
//
// 📌 为什么需要它：注册那条路要**先发一封验证码邮件**，而发信是"发出去就收不回"的动作
// —— 邀请码无效的话那封信就白发了（收件人干等一封永远不会来的信）✗
// 所以发信之前先用它查一遍 ✓
//
// ⚠️ 查和用之间有时间差，**别拿它当"原子占位"** ——
// 真正防"并发抢同一张码"的是 SignUp 提交段那次复核（见那儿的注释）✓
func (s *Store) CheckCode(code string) (*InviteCode, error) {
	nc := NormalizeCode(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	// 📌 这儿**不用**重读文件：邀请码只有面板会改，而面板就是本进程
	// ⇒ 内存里那份永远是最新的 ✓（账号那边要重读，是因为 admin-add 是**另一个进程**）
	c := s.codes[nc]
	if c == nil || c.Used() {
		return nil, ErrBadCode
	}
	return c, nil
}

// FindByEmail 按邮箱找一个账号（只看，不改任何东西）。
//
// 📌 "发验证码之前先确认这个邮箱注册过"用它 —— 不然外网随便填个地址
// 就能让我们的发信账号往外发信 ✗
func (s *Store) FindByEmail(email string) (*Account, bool) {
	ne := NormalizeEmail(email)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadAccountsIfChanged()
	a := s.accounts[ne]
	if a == nil {
		return nil, false
	}
	c := *a
	c.PassHash = ""
	return &c, true
}

// LoginTrustingEmail 给一个**已经证明过拥有这个邮箱**的人发会话。
//
// 🩸🩸 **它自己不做任何证明 —— 谁调都行。** 这是整套东西里最危险的一个口子。
// 动它之前先确认你手里**真的**有一个"他拥有这个邮箱"的凭据 ✗
//
// 那为什么还放在这儿、而不是让面板自己造一个会话存进去：
// 发会话必须由**唯一的写者**做（面板写 sessions.json、信令只读）——
// 绕过这个包去自己拼一个 Session，等于把"单写者"这条地基踹了 ✗
//
// 目前**唯一**的调用点：面板 apiLogin 里"邮箱验证码"那条路 ——
// 它前一行刚拿 mailCodeStore.verify() 验过码 ✓
// （也就是说：这条路的强度 = 那个邮箱本身的强度。别再给它加别的入口。）
func (s *Store) LoginTrustingEmail(email string, now time.Time) (*Account, string, error) {
	ne := NormalizeEmail(email)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mustWritable("登录"); err != nil {
		return nil, "", err
	}
	s.reloadAccountsIfChanged()
	a := s.accounts[ne]
	if a == nil {
		return nil, "", ErrBadLogin
	}
	if a.Status != StatusActive {
		return nil, "", ErrDisabled
	}
	tok, err := newToken()
	if err != nil {
		return nil, "", err
	}
	s.pruneSessionsLocked(now)
	s.sessions[hashToken(tok)] = &Session{
		TokenHash: hashToken(tok),
		AccountID: a.ID,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(SessionTTL).Unix(),
	}
	if err := s.saveSessionsLocked(); err != nil {
		delete(s.sessions, hashToken(tok))
		return nil, "", fmt.Errorf("保存失败: %w", err)
	}
	c := *a
	c.PassHash = ""
	return &c, tok, nil
}

// VerifyToken 校验一个令牌 —— **信令服务器每来一个连接就走这里**。
//
// 这是整套东西里唯一算数的鉴权点：它跑在服务器上，用户碰不着 ✓
//
// 返回的是**副本**，而且不带密码哈希：
//   - 副本 ⇒ 调用方（HTTP 层 / 信令层）改不到内存里的真对象 ✗
//   - 不带哈希 ⇒ 调用方直接把这个结构序列化出去也不会泄密 ✗
//
// （一次结构体拷贝几十字节，而这条路径每个连接走一次 —— 便宜得可以忽略。）
func (s *Store) VerifyToken(tok string, now time.Time) (*Account, bool) {
	if tok == "" {
		return nil, false
	}
	h := hashToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	// 信令侧是只读的 ⇒ 必须靠这两个重读才知道外面改了什么。
	// 没有它们，停用账号 / 强制下线都要等到重启才生效 ✗
	s.reloadAccountsIfChanged()
	s.reloadSessionsIfChanged()

	x := s.sessions[h]
	if x == nil || !x.Valid(now) {
		return nil, false
	}
	a := s.byID[x.AccountID]
	if a == nil || a.Status != StatusActive {
		return nil, false
	}
	// 强制下线：管理员按一下，这个时刻之前发的 token 全废。
	// 用 `<` 而不是 `<=`：同一秒内刚登录的人不该被顺手误杀 ✓
	// （同一秒内建出来的旧会话漏网那部分，靠 revokeLocked 里的"真删"兜住 ✓）
	if x.CreatedAt < a.RevokeBefore {
		return nil, false
	}
	c := *a
	c.PassHash = ""
	return &c, true
}

// Logout 主动注销一个会话（客户端点"退出登录"）。
func (s *Store) Logout(tok string) error {
	if tok == "" {
		return nil
	}
	h := hashToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mustWritable("注销"); err != nil {
		return err
	}
	if _, ok := s.sessions[h]; !ok {
		return nil
	}
	delete(s.sessions, h)
	return s.saveSessionsLocked()
}

// ---- 账号管理（管理面板用） ----

// revokeLocked 强制下线：**两道一起上**。调用方必须已持锁。
//
//	① 先把账号上的时间线推到 now ⇒ 读侧那一瞬间就算还拿着旧的会话表，
//	   也会因为 `CreatedAt < RevokeBefore` 而判无效 ✓
//	② 再把会话真删掉 ⇒ 时间戳是秒级的，同一秒内建出来的会话躲得过 ①，
//	   但躲不过删除 ✓
//
// 🩸 光靠 ① 是不够的：实测过 —— 登录和下线落在同一秒时，那条会话毫发无伤
// （`CreatedAt < RevokeBefore` 是 false）。光靠 ② 也不够：删会话这个动作
// 在读侧生效之前有个时间差，那个缝里 ① 顶着。两道都要。
//
// 顺序也不能反：**先推时间线、再删会话** —— 中途读侧不管落在哪一步都是安全的那一侧 ✓
func (s *Store) revokeLocked(accountID int64, now time.Time) error {
	a := s.byID[accountID]
	if a == nil {
		return ErrNotFound
	}
	a.RevokeBefore = now.Unix()
	if err := s.saveAccountsLocked(); err != nil {
		return err
	}
	if s.dropSessionsLocked(accountID) == 0 {
		return nil
	}
	return s.saveSessionsLocked()
}

// ChangePassword 改密码。**改完所有旧会话立刻作废**（要重新登录）。
//
// 这是有意的：改密码这个动作在现实里往往就是"我怀疑有人登了我的号"，
// 那就必须把别的会话一起踢掉，否则防了个寂寞。
func (s *Store) ChangePassword(accountID int64, oldPass, newPass string, now time.Time) error {
	s.mu.Lock()
	a := s.byID[accountID]
	var hash string
	if a != nil {
		hash = a.PassHash
	}
	s.mu.Unlock()
	if a == nil {
		return ErrNotFound
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPass)); err != nil {
		return ErrBadOldPass
	}
	if msg := CheckPassword(newPass); msg != "" {
		return inputErr(msg)
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(newPass), bcryptCost)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.byID[accountID]
	if cur == nil {
		return ErrNotFound
	}
	cur.PassHash = string(nh)
	return s.revokeLocked(accountID, now)
}

// ResetPassword 是「忘记密码」那条路：**不校验旧密码**，靠验证码证明邮箱是他的。
//
// 🩸🩸 这是全项目**唯一**一个不需要旧密码就能改密码的入口 ⇒
//
//	调用方**必须**先验过邮箱验证码 ✗ 别在别处直接调它 ✗
//	（面板的 /api/reset 就是先 verify 再调 ✓）
//
// 📌 改完会**撤销这个账号的所有会话**（跟 ChangePassword 一致）——
//
//	万一密码是被偷走之后被改的，那些正登录着的会话要一起踢掉 ✓
func (s *Store) ResetPassword(email, newPass string, now time.Time) error {
	ne := NormalizeEmail(email)
	s.mu.Lock()
	a := s.accounts[ne]
	var id int64
	if a != nil {
		id = a.ID
	}
	s.mu.Unlock()

	if a == nil {
		return ErrNotFound
	}
	if msg := CheckPassword(newPass); msg != "" {
		return inputErr(msg)
	}
	nh, err := bcrypt.GenerateFromPassword([]byte(newPass), bcryptCost)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mustWritable("重置密码"); err != nil {
		return err
	}
	cur := s.byID[id]
	if cur == nil {
		return ErrNotFound
	}
	cur.PassHash = string(nh)
	return s.revokeLocked(id, now)
}

// SetStatus 停用 / 启用一个账号。
func (s *Store) SetStatus(accountID int64, status string, now time.Time) error {
	if status != StatusActive && status != StatusDisabled {
		return fmt.Errorf("auth: 不认识的状态 %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.byID[accountID]
	if a == nil {
		return ErrNotFound
	}
	a.Status = status
	if status == StatusDisabled {
		a.DisabledAt = now.Unix()
	} else {
		a.DisabledAt = 0
	}
	return s.saveAccountsLocked()
}

// RevokeSessions 强制下线：这个时刻以前发的 token 全部作废。
func (s *Store) RevokeSessions(accountID int64, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeLocked(accountID, now)
}

// SetNote 改写某个邀请码的备注（"发给谁了"）。
func (s *Store) SetNote(code, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.codes[NormalizeCode(code)]
	if c == nil {
		return ErrBadCode
	}
	c.Note = note
	return s.saveAccountsLocked()
}

// ---- 读（管理面板用） ----

// Accounts 返回所有账号的副本，**不含密码哈希**。
//
// 🩸 副本 + 去掉哈希：返回内部指针的话，调用方（HTTP 层）一旦直接序列化出去，
// 密码哈希就跟着出去了；而且它还能顺手改到内存里的真对象 ✗
func (s *Store) Accounts() []Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		c := *a
		c.PassHash = ""
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Codes 返回邀请码副本，按创建时间排。
func (s *Store) Codes() []InviteCode {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]InviteCode, 0, len(s.codes))
	for _, c := range s.codes {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// SessionCount 返回某个账号当前的会话数（管理面板"登录中"那一列）。
func (s *Store) SessionCount(accountID int64, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reloadSessionsIfChanged()
	n := 0
	for _, x := range s.sessions {
		if x.AccountID == accountID && x.Valid(now) {
			n++
		}
	}
	return n
}

// AddAdmin 手工建一个管理员账号（部署时用，见 cmd/panel 的 admin-add）。
//
// 管理员**不走邀请码**：他是服务器主人，没有"谁给他发码"这回事。
func (s *Store) AddAdmin(email, password string, domains []string, now time.Time) (*Account, error) {
	ne := NormalizeEmail(email)
	if msg := CheckEmail(ne, domains); msg != "" {
		return nil, inputErr(msg)
	}
	if msg := CheckPassword(password); msg != "" {
		return nil, inputErr(msg)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[ne]; exists {
		return nil, ErrEmailTaken
	}
	a := &Account{ID: s.nextID, Email: ne, PassHash: string(hash), Role: RoleAdmin,
		Status: StatusActive, CreatedAt: now.Unix()}
	s.nextID++
	s.accounts[ne] = a
	s.byID[a.ID] = a
	if err := s.saveAccountsLocked(); err != nil {
		delete(s.accounts, ne)
		delete(s.byID, a.ID)
		s.nextID--
		return nil, err
	}
	return a, nil
}

// SignupOpen 报告"注册是否**不需要**邀请码" ✓
//
// 📌 读的是内存里的那份（跟着 accounts.json 一起加载 / 重载）✓
func (s *Store) SignupOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signupOpen
}

// SetSignupOpen 打开/关闭"注册不需要邀请码"，并**立刻落盘** ✓
//
// 🩸 只有管理面板会调它（单写者约定）✓
// ⚠️ 这个开关一改，正在进行的注册也会立刻按新值走 —— 不需要重启任何服务 ✓
//
//	（面板自己读内存、信令根本不关心注册 ✓）
func (s *Store) SetSignupOpen(open bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.mustWritable("改注册设置"); err != nil {
		return err
	}
	s.signupOpen = open
	return s.saveAccountsLocked()
}
