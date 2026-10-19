package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 15, 21, 0, 0, 0, time.UTC)

func newStore(t *testing.T, writable bool) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, writable)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	return s, dir
}

// signUpOne 是测试里最常用的一步：发码 → 注册 → 拿到账号和 token。
func signUpOne(t *testing.T, s *Store, email string) (*Account, string, string) {
	t.Helper()
	codes, err := s.GenCodes(1, "test", "", now)
	if err != nil {
		t.Fatalf("GenCodes 失败: %v", err)
	}
	a, tok, err := s.SignUp(codes[0], email, "goodpassword", DefaultEmailDomains, now)
	if err != nil {
		t.Fatalf("SignUp(%s) 失败: %v", email, err)
	}
	return a, tok, codes[0]
}

func TestOpenEmpty(t *testing.T) {
	s, _ := newStore(t, true)
	if n := len(s.Accounts()); n != 0 {
		t.Errorf("新库该是空的，实得 %d 个账号", n)
	}
	if n := len(s.Codes()); n != 0 {
		t.Errorf("新库该是空的，实得 %d 个码", n)
	}
}

func TestGenCodesAndPersist(t *testing.T) {
	s, dir := newStore(t, true)
	codes, err := s.GenCodes(20, "2026-10-15-第1批", "老乡群", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 20 {
		t.Fatalf("该生成 20 个，实得 %d", len(codes))
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("批量生成里有重复：%q", c)
		}
		seen[c] = true
		if len(c) != CodeLen {
			t.Fatalf("码 %q 长度不对", c)
		}
	}
	// 重开一次，得还在（这是"真的落盘了"的判据）
	s2, err := Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Codes()
	if len(got) != 20 {
		t.Fatalf("重开后该有 20 个码，实得 %d", len(got))
	}
	for _, c := range got {
		if c.Batch != "2026-10-15-第1批" || c.Note != "老乡群" {
			t.Errorf("批号/备注没存住：%+v", c)
		}
		if c.Used() {
			t.Errorf("刚发出来的码不该是已用：%+v", c)
		}
	}
}

// 参数边界
func TestGenCodesBounds(t *testing.T) {
	s, _ := newStore(t, true)
	if _, err := s.GenCodes(0, "", "", now); err == nil {
		t.Error("生成 0 个该报错 ✗")
	}
	if _, err := s.GenCodes(-1, "", "", now); err == nil {
		t.Error("生成 -1 个该报错 ✗")
	}
	if _, err := s.GenCodes(MaxCodesPerCall+1, "", "", now); err == nil {
		t.Error("超过上界该报错 ✗（挡的是手滑填 100000）")
	}
}

// ⭐⭐ 一码一人 —— 而且必须**并发下**也成立。
//
// 这是整套东西里最容易写错的一处：常见的错误写法是"先查码没用过，
// 再标记成已用"，两步之间没有互斥 ⇒ 两个人同时提交，两个人都过了检查，
// 结果一个码注册出两个账号 ✗
//
// 反证方法：把 SignUp 提交段里的 `if c == nil || c.Used() { return ErrBadCode }`
// 那两行删掉，这条测试立刻变红（会成功 8 次而不是 1 次）。
func TestSignUp_OneCodeOnlyOnce_Concurrent(t *testing.T) {
	s, _ := newStore(t, true)
	codes, err := s.GenCodes(1, "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	code := codes[0]

	const n = 8
	var wg sync.WaitGroup
	var okCount atomic.Int64
	var errMu sync.Mutex
	var unexpected []error

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, _, err := s.SignUp(code, emailN(i), "goodpassword", DefaultEmailDomains, now)
			if err == nil {
				okCount.Add(1)
				return
			}
			if !errors.Is(err, ErrBadCode) {
				errMu.Lock()
				unexpected = append(unexpected, err)
				errMu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	if len(unexpected) > 0 {
		t.Fatalf("抢输的那些该拿到 ErrBadCode，实得：%v", unexpected)
	}
	if got := okCount.Load(); got != 1 {
		t.Fatalf("同一个码并发注册，该只有 1 个成功，实得 %d 个 ✗", got)
	}

	// 而且盘上只该有 1 个账号
	if n := len(s.Accounts()); n != 1 {
		t.Fatalf("该只有 1 个账号，实得 %d", n)
	}
	// 码要标记成"被那个赢家用了"
	got := s.Codes()
	if len(got) != 1 || !got[0].Used() {
		t.Fatalf("码该被标记为已用：%+v", got)
	}
}

func emailN(i int) string {
	return string(rune('a'+i%26)) + "@qq.com"
}

// 同一个邮箱拿两个不同的码去注册：第二个必须失败，
// **而且第二个码不能被消耗掉** —— 不然那张码就白瞎了，用户还以为自己注册成功了。
func TestSignUp_EmailTaken_DoesNotBurnTheCode(t *testing.T) {
	s, _ := newStore(t, true)
	codes, _ := s.GenCodes(2, "", "", now)

	if _, _, err := s.SignUp(codes[0], "a@qq.com", "goodpassword", DefaultEmailDomains, now); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.SignUp(codes[1], "A@QQ.COM", "goodpassword", DefaultEmailDomains, now)
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("同一个邮箱（大小写不同）该被判重复，实得：%v", err)
	}
	for _, c := range s.Codes() {
		if c.Code == codes[1] && c.Used() {
			t.Fatal("注册失败的码不该被消耗掉 ✗（那张码还能发给别人）")
		}
	}
}

// 邮箱写错了（不在白名单）也不该把码吃掉。
func TestSignUp_BadEmail_DoesNotBurnTheCode(t *testing.T) {
	s, _ := newStore(t, true)
	codes, _ := s.GenCodes(1, "", "", now)
	if _, _, err := s.SignUp(codes[0], "a@gmail.com", "goodpassword", DefaultEmailDomains, now); err == nil {
		t.Fatal("gmail 不在白名单，该被拒 ✗")
	}
	if s.Codes()[0].Used() {
		t.Fatal("被拒的注册不该消耗邀请码 ✗")
	}
}

// 用户手抄的码（小写 + 连字符 + 把 0 写成 O）也要能对上。
func TestSignUp_MessyCodeStillWorks(t *testing.T) {
	s, _ := newStore(t, true)
	codes, _ := s.GenCodes(1, "", "", now)
	c := codes[0]

	messy := ""
	for i, r := range c {
		if i == 5 {
			messy += "-"
		}
		if r == '0' {
			messy += "o"
		} else {
			messy += string(r)
		}
	}
	if _, _, err := s.SignUp(messy, "a@qq.com", "goodpassword", DefaultEmailDomains, now); err != nil {
		t.Fatalf("手抄形式 %q（原码 %q）该能对上，实得：%v", messy, c, err)
	}
}

func TestLogin(t *testing.T) {
	s, _ := newStore(t, true)
	a, _, _ := signUpOne(t, s, "bob@qq.com")

	got, tok, err := s.Login("Bob@QQ.com", "goodpassword", now)
	if err != nil {
		t.Fatalf("登录该成功，实得 %v", err)
	}
	if got.ID != a.ID {
		t.Errorf("登录拿到的是别人？期望 ID %d，实得 %d", a.ID, got.ID)
	}
	if tok == "" {
		t.Error("该发一个 token")
	}
	// token 不该等于它在盘上的形式
	if _, ok := s.sessions[hashToken(tok)]; !ok {
		t.Error("会话该按哈希存进表里")
	}
	if _, plain := s.sessions[tok]; plain {
		t.Error("🩸 盘上存了 token 原文 —— 该存 sha256 ✗")
	}
}

// 密码错 / 邮箱不存在，必须是**同一个**错误 ——
// 分开说等于送了一个"这个邮箱注册过没有"的查询接口。
func TestLogin_NoAccountEnumeration(t *testing.T) {
	s, _ := newStore(t, true)
	signUpOne(t, s, "bob@qq.com")

	_, _, err1 := s.Login("bob@qq.com", "wrongpassword", now)
	_, _, err2 := s.Login("nobody@qq.com", "wrongpassword", now)
	if !errors.Is(err1, ErrBadLogin) || !errors.Is(err2, ErrBadLogin) {
		t.Fatalf("两种失败该都是 ErrBadLogin，实得：%v / %v", err1, err2)
	}
}

func TestLogin_Disabled(t *testing.T) {
	s, _ := newStore(t, true)
	a, tok, _ := signUpOne(t, s, "bob@qq.com")
	if _, _, err := s.Login("bob@qq.com", "goodpassword", now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(a.ID, StatusDisabled, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Login("bob@qq.com", "goodpassword", now); !errors.Is(err, ErrDisabled) {
		t.Fatalf("停用后该登不上，实得 %v", err)
	}
	if _, ok := s.VerifyToken(tok, now); ok {
		t.Fatal("停用后旧 token 该立刻失效 ✗")
	}
}

func TestVerifyToken(t *testing.T) {
	s, _ := newStore(t, true)
	a, tok, _ := signUpOne(t, s, "bob@qq.com")

	got, ok := s.VerifyToken(tok, now)
	if !ok || got.ID != a.ID {
		t.Fatalf("刚发的 token 该有效，实得 ok=%v", ok)
	}

	for _, bad := range []string{"", "garbage", tok + "x", tok[:len(tok)-1]} {
		if _, ok := s.VerifyToken(bad, now); ok {
			t.Errorf("%q 不该通过 ✗", bad)
		}
	}

	// 过期
	if _, ok := s.VerifyToken(tok, now.Add(SessionTTL+time.Minute)); ok {
		t.Error("过期会话该失效 ✗")
	}
	// 到期前一秒还行
	if _, ok := s.VerifyToken(tok, now.Add(SessionTTL-time.Second)); !ok {
		t.Error("还没到期就失效了 ✗")
	}
}

func TestLogout(t *testing.T) {
	s, _ := newStore(t, true)
	_, tok, _ := signUpOne(t, s, "bob@qq.com")
	if err := s.Logout(tok); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(tok, now); ok {
		t.Fatal("注销之后 token 该失效 ✗")
	}
	// 注销一个不存在的 token 不该报错（幂等）
	if err := s.Logout("garbage"); err != nil {
		t.Errorf("注销不存在的 token 该安静通过，实得 %v", err)
	}
}

func TestChangePassword_RevokesOldSessions(t *testing.T) {
	s, _ := newStore(t, true)
	a, oldTok, _ := signUpOne(t, s, "bob@qq.com")

	// 先错一次原密码
	if err := s.ChangePassword(a.ID, "wrongold", "newpassword", now); !errors.Is(err, ErrBadOldPass) {
		t.Fatalf("原密码错该报 ErrBadOldPass，实得 %v", err)
	}
	if err := s.ChangePassword(a.ID, "goodpassword", "newpassword", now); err != nil {
		t.Fatal(err)
	}
	// 旧 token 该作废
	if _, ok := s.VerifyToken(oldTok, now); ok {
		t.Fatal("改密码之后旧会话该一起踢掉 ✗（现实里改密码往往就是怀疑号被人登了）")
	}
	// 新密码能登，旧密码不能
	if _, _, err := s.Login("bob@qq.com", "newpassword", now.Add(time.Second)); err != nil {
		t.Fatalf("新密码该能登，实得 %v", err)
	}
	if _, _, err := s.Login("bob@qq.com", "goodpassword", now.Add(time.Second)); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("旧密码该登不上，实得 %v", err)
	}
}

func TestRevokeSessions(t *testing.T) {
	s, _ := newStore(t, true)
	a, tok, _ := signUpOne(t, s, "bob@qq.com")
	later := now.Add(time.Second)
	if err := s.RevokeSessions(a.ID, later); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(tok, later); ok {
		t.Fatal("强制下线之后旧 token 该失效 ✗")
	}
	// 强制下线之后重新登录，新的该能用
	_, tok2, err := s.Login("bob@qq.com", "goodpassword", later.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(tok2, later.Add(time.Second)); !ok {
		t.Fatal("重新登录拿到的 token 该有效 ✗")
	}
}

// 🩸 强制下线和登录落在**同一秒**时，那条会话也必须死。
//
// 这是时间戳精度挖出来的坑：只推 `RevokeBefore` 的话，
// `CreatedAt < RevokeBefore` 在"同一秒"下是 false ⇒ 会话毫发无伤 ✗
// （2026-10-15 实测踩到过。）所以强制下线是**推时间线 + 真删会话**两道一起上 ——
// 反证方法：把 revokeLocked 里的 dropSessionsLocked 那两行删掉，这条立刻红。
func TestRevokeSessions_SameSecondAsLogin(t *testing.T) {
	s, _ := newStore(t, true)
	a, tok, _ := signUpOne(t, s, "bob@qq.com")
	if err := s.RevokeSessions(a.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(tok, now); ok {
		t.Fatal("同一秒内强制下线，会话也该立刻死 ✗（说明只靠时间戳、没真删）")
	}
	// 而下线之后**紧接着**（同一秒内）重新登录的人，不能被顺手误杀
	_, tok2, err := s.Login("bob@qq.com", "goodpassword", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.VerifyToken(tok2, now); !ok {
		t.Fatal("下线后立刻重新登录的会话该有效 ✗（被误杀了）")
	}
}

// 🩸🩸 账号 ID 必须**从 1 起**。
//
// 0 是"没有"的意思 —— 邀请码用 UsedBy == 0 表示"还没被用过"。
// 第一个账号要是拿到 ID 0，它兑换时写下的 UsedBy 就是 0，
// 于是 `Used()` 判成"没用过" ⇒ **同一张码能被兑换两次** ✗
//
// 这不是纸上推演：2026-10-15 写并发那条测试时就把它抓出来了（8 个并发跑了 2 个成功）。
func TestAccountIDStartsAtOne(t *testing.T) {
	s, _ := newStore(t, true)
	a1, _, _ := signUpOne(t, s, "a@qq.com")
	a2, _, _ := signUpOne(t, s, "b@qq.com")
	if a1.ID == 0 || a2.ID == 0 {
		t.Fatalf("账号 ID 不能是 0（0 在这里表示「没有」）。实得 %d / %d", a1.ID, a2.ID)
	}
	if a1.ID == a2.ID {
		t.Fatalf("两个账号拿了同一个 ID：%d", a1.ID)
	}
	// 每个"已用"的码，UsedBy 都得指向一个真账号
	for _, c := range s.Codes() {
		if c.Used() && c.UsedBy == 0 {
			t.Fatalf("码 %s 标成已用，但 UsedBy 是 0 ✗", c.Code)
		}
	}
}

func TestReadOnlyStoreRefusesWrites(t *testing.T) {
	w, dir := newStore(t, true)
	codes, err := w.GenCodes(1, "", "", now)
	if err != nil {
		t.Fatal(err)
	}

	ro, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	// 用一张**真码**去试注册：这样才会走到提交那一步，
	// 测的才是"只读库不让写"而不是"码不存在"。
	if _, _, err := ro.SignUp(codes[0], "a@qq.com", "goodpassword", DefaultEmailDomains, now); err == nil {
		t.Error("只读的库不该能注册 ✗")
	}
	if _, err := ro.GenCodes(1, "", "", now); err == nil {
		t.Error("只读的库不该能发码 ✗")
	}
	if _, _, err := ro.Login("a@qq.com", "goodpassword", now); err == nil {
		t.Error("只读的库不该能登录 ✗")
	}
	if err := ro.SetStatus(1, StatusDisabled, now); err == nil {
		t.Error("只读的库不该能停用账号 ✗")
	}
	if err := ro.Logout("whatever"); err == nil {
		t.Error("只读的库不该能注销 ✗")
	}
	// 反过来：只读的库**能读**（这是它存在的意义）
	if _, ok := ro.VerifyToken("nosuchtoken", now); ok {
		t.Error("乱给的 token 不该通过")
	}
	// 而写库这边一个字都没被写进去（用"账号数还是 0"当判据）
	if n := len(w.Accounts()); n != 0 {
		t.Fatalf("只读库的写尝试不该影响写库，实得 %d 个账号", n)
	}
}

// ⭐⭐ 这条是**跨进程**那件事的证明：面板写、信令读。
//
// 信令服务器是只读的，它靠 mtime+size 判断要不要重读。
// 这条测试要是红了，就说明"停用账号 / 强制下线"在信令侧根本不生效 ——
// 那种 bug 在真机上极难发现（面板上显示停用了，人却照样连得上）。
func TestReaderSeesWriterChanges(t *testing.T) {
	w, dir := newStore(t, true)
	a, tok, _ := signUpOne(t, w, "bob@qq.com")

	// 读侧在"写侧已经写好"之后打开
	r, err := Open(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.VerifyToken(tok, now); !ok {
		t.Fatal("读侧该认得写侧刚建的会话 ✗")
	}

	// ① 停用
	if err := w.SetStatus(a.ID, StatusDisabled, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.VerifyToken(tok, now); ok {
		t.Fatal("写侧停用了账号，读侧该立刻看到 ✗（重读逻辑没生效）")
	}
	// ② 恢复 → 又该能用
	if err := w.SetStatus(a.ID, StatusActive, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.VerifyToken(tok, now); !ok {
		t.Fatal("恢复之后读侧该又能用 ✗")
	}
	// ③ 强制下线
	if err := w.RevokeSessions(a.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.VerifyToken(tok, now.Add(time.Second)); ok {
		t.Fatal("写侧强制下线了，读侧该立刻失效 ✗")
	}
	// ④ 注销
	_, tok2, err := w.Login("bob@qq.com", "goodpassword", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.VerifyToken(tok2, now.Add(2*time.Second)); !ok {
		t.Fatal("读侧该认得新登录的会话 ✗")
	}
	if err := w.Logout(tok2); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.VerifyToken(tok2, now.Add(2*time.Second)); ok {
		t.Fatal("写侧注销了，读侧该失效 ✗")
	}
}

// 主文件坏了要能退回 .bak —— 磁盘满 / 断电写坏时最后一道保险。
func TestCorruptMainFallsBackToBak(t *testing.T) {
	s, dir := newStore(t, true)
	signUpOne(t, s, "bob@qq.com")

	// 造一份"上一个好版本"的备份：直接把当前文件复制过去
	good, err := os.ReadFile(filepath.Join(dir, AccountsFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, AccountsFile+".bak"), good, 0o600); err != nil {
		t.Fatal(err)
	}
	// 把主文件砸成垃圾
	if err := os.WriteFile(filepath.Join(dir, AccountsFile), []byte("{这不是 json"), 0o600); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir, true)
	if err != nil {
		t.Fatalf("主文件坏了该退回备份，实得错误：%v", err)
	}
	if n := len(s2.Accounts()); n != 1 {
		t.Fatalf("从备份恢复后该有 1 个账号，实得 %d", n)
	}
}

// 主文件和备份都坏了 ⇒ 必须**报错**，绝不能"当空的"继续跑。
// 当空的话管理面板会显示"一个账号都没有"，老板一慌就可能重发码覆盖真数据 ✗
func TestBothCorrupt_RefusesToStart(t *testing.T) {
	_, dir := newStore(t, true)
	bad := []byte("{这不是 json")
	os.WriteFile(filepath.Join(dir, AccountsFile), bad, 0o600)
	os.WriteFile(filepath.Join(dir, AccountsFile+".bak"), bad, 0o600)

	if _, err := Open(dir, true); err == nil {
		t.Fatal("主文件和备份都坏了，该拒绝启动 ✗（绝不能当空库继续跑）")
	}
}

// 备份里的账号是**旧版本**时的样子：也得能读（前向兼容的底线）。
func TestBakFromOlderFormatLoads(t *testing.T) {
	_, dir := newStore(t, true)
	old := accountsFile{
		Version: 1,
		Accounts: []*Account{{
			ID: 1, Email: "old@qq.com", PassHash: "x", Role: RoleUser,
			Status: StatusActive, CreatedAt: now.Unix(),
		}},
	}
	b, _ := json.Marshal(&old)
	os.WriteFile(filepath.Join(dir, AccountsFile), b, 0o600)

	s, err := Open(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.Accounts()); got != 1 {
		t.Fatalf("该读到 1 个账号，实得 %d", got)
	}
}

func TestAddAdmin(t *testing.T) {
	s, _ := newStore(t, true)
	a, err := s.AddAdmin("boss@qq.com", "bossstrongpass", DefaultEmailDomains, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Role != RoleAdmin {
		t.Fatalf("该是管理员，实得 %q", a.Role)
	}
	if _, err := s.AddAdmin("boss@qq.com", "anotherpass", DefaultEmailDomains, now); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("重复建管理员该报重复，实得 %v", err)
	}
	// 不用邀请码也能登
	if _, _, err := s.Login("boss@qq.com", "bossstrongpass", now); err != nil {
		t.Fatalf("管理员该能登，实得 %v", err)
	}
}

// Accounts()/Codes() 返回的必须是**副本**：
// 调用方（HTTP 层）拿到内部指针的话，一旦直接序列化出去就把密码哈希泄了 ✗
func TestListReturnsCopiesWithoutHash(t *testing.T) {
	s, _ := newStore(t, true)
	signUpOne(t, s, "bob@qq.com")

	list := s.Accounts()
	if list[0].PassHash != "" {
		t.Error("列表里的账号不该带密码哈希 ✗")
	}
	list[0].Status = StatusDisabled
	if got := s.Accounts()[0].Status; got != StatusActive {
		t.Fatal("改列表里的副本不该改到库里的真对象 ✗")
	}

	cs := s.Codes()
	cs[0].Note = "改着玩"
	if got := s.Codes()[0].Note; got == "改着玩" {
		t.Fatal("码列表也该是副本 ✗")
	}
}

func TestSetNote(t *testing.T) {
	s, _ := newStore(t, true)
	codes, _ := s.GenCodes(1, "", "", now)
	if err := s.SetNote(codes[0], "发给张三 / zhangsan@qq.com"); err != nil {
		t.Fatal(err)
	}
	if got := s.Codes()[0].Note; got != "发给张三 / zhangsan@qq.com" {
		t.Fatalf("备注没存住：%q", got)
	}
	// 用带连字符的形式也能定位到（后台页面上显示的是那种）
	if err := s.SetNote(FormatCode(codes[0]), "改了"); err != nil {
		t.Fatalf("拿显示形式也该找得到，实得 %v", err)
	}
	if err := s.SetNote("ZZZZZZZZZZ", "不存在"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("不存在的码该报 ErrBadCode，实得 %v", err)
	}
}

func TestSessionCount(t *testing.T) {
	s, _ := newStore(t, true)
	a, _, _ := signUpOne(t, s, "bob@qq.com")
	if n := s.SessionCount(a.ID, now); n != 1 {
		t.Fatalf("注册顺手登录该算 1 个会话，实得 %d", n)
	}
	s.Login("bob@qq.com", "goodpassword", now)
	if n := s.SessionCount(a.ID, now); n != 2 {
		t.Fatalf("该 2 个会话，实得 %d", n)
	}
	if n := s.SessionCount(a.ID, now.Add(SessionTTL+time.Minute)); n != 0 {
		t.Fatalf("过期之后该是 0，实得 %d", n)
	}
}

// 过期会话会在下次登录时被清掉（不然文件只涨不降）
func TestExpiredSessionsPruned(t *testing.T) {
	s, _ := newStore(t, true)
	signUpOne(t, s, "bob@qq.com")
	if n := len(s.sessions); n != 1 {
		t.Fatalf("该 1 条会话，实得 %d", n)
	}
	s.Login("bob@qq.com", "goodpassword", now.Add(SessionTTL+time.Hour))
	if n := len(s.sessions); n != 1 {
		t.Fatalf("过期的那条该被清掉，只剩新的 1 条，实得 %d", n)
	}
}
