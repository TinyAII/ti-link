// 存储层：两个 JSON 文件 + 内存快照。
//
// 写的一律是「临时文件 → fsync → rename」—— rename 在同一文件系统里是原子的，
// 所以读的一方**永远**看到完整的一份，不存在"读到写了一半"。
// 顺手留一份 .bak：主文件要是坏了（磁盘满 / 断电写坏），还有人能救。
package auth

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// 文件名。放在服务端的数据目录里（和二进制分开，见 docs/账号与邀请码.md）。
const (
	AccountsFile = "accounts.json"
	SessionsFile = "sessions.json"
)

// 文件里的版本号。以后要改格式（加必填字段之类），靠它认。
// 📌 加**可选**字段不用动它 —— 老的解析器会忽略不认识的字段 ✓
const fileVersion = 1

// accountsFile 是 accounts.json 的形状：账号 + 邀请码放一个文件。
//
// 为什么不拆成两个文件：它俩在**注册**这一步是一起变的（同一个事务：
// 码标成已用 + 建账号），放一个文件就能靠"一次原子替换"一起生效 ✓
// 拆开就变成"码用掉了但账号没建上"这种半截状态，得自己写补偿逻辑 ✗
type accountsFile struct {
	Version  int           `json:"version"`
	Accounts []*Account    `json:"accounts"`
	Codes    []*InviteCode `json:"codes"`

	// Settings 是账号体系相关的开关，跟 Accounts/Codes 一起存 ——
	// 它们本来就是"同一份东西"（注册时一起变）✓
	// nil = 老文件（还没有这个字段）⇒ 按**默认值**处理 ✓
	Settings *Settings `json:"settings,omitempty"`
}

// Settings 是管理面板能改的账号体系开关。
//
// 🩸🩸 **SignupOpen 是正向语义、默认 false**：
//   - false（默认）= 注册**需要邀请码**（发布初期的谨慎状态 ✓）
//   - true         = 注册**不需要邀请码**（谁都能注册，靠邮箱验证码防刷 ✓）
//
// 📌 为什么不用 `NeedsCode bool`：老库文件里没这个字段，读出来是零值 false，
//
//	在 `NeedsCode` 语义下就是"不需要码" ⇒ **一升级就门户大开** ✗✗
//	换成 `SignupOpen`，零值 false 恰好等于"要码" ✓ 升级不会出事 ✓
type Settings struct {
	SignupOpen bool `json:"signupOpen"`
}

type sessionsFile struct {
	Version  int        `json:"version"`
	Sessions []*Session `json:"sessions"`
}

// stamp 是"上次读到的是哪一份"。靠 mtime + size 判断外面有没有人改过。
//
// 🩸 只比 mtime 不够：同一秒内的两次写，mtime 可能一样（文件系统精度），
// 加上 size 就基本不会漏了。
type stamp struct {
	mtime time.Time
	size  int64
	ok    bool // 文件当时存在吗
}

func stampOf(path string) stamp {
	fi, err := os.Stat(path)
	if err != nil {
		return stamp{}
	}
	return stamp{mtime: fi.ModTime(), size: fi.Size(), ok: true}
}

func (s stamp) same(o stamp) bool {
	return s.ok == o.ok && s.size == o.size && s.mtime.Equal(o.mtime)
}

// Store 是一份账号数据的句柄。
//
// 🩸 **maps 是真相，切片只是落盘时的样子。** 反过来（切片是真相 + 一堆下标索引）
// 每次增删都要同步维护索引，漏一处就是"列表里有、登录时找不到"这种鬼故事。
type Store struct {
	dir string
	// writable=false 是信令服务器那边：**它一个字都不许写** ✓
	writable bool

	mu       sync.Mutex
	accounts map[string]*Account    // key = 规范化邮箱
	byID     map[int64]*Account     // key = 账号 ID
	codes    map[string]*InviteCode // key = 规范化邀请码
	sessions map[string]*Session

	// signupOpen = "注册不需要邀请码"。**默认 false（要邀请码）** ✓
	signupOpen bool // key = token 的 sha256 十六进制
	nextID     int64

	accStamp stamp
	sesStamp stamp
}

// Open 打开存储目录。dir 不存在会自动建（0700）。
//
// writable 只有管理面板传 true。信令服务器传 false —— 这样"信令侧不写盘"
// 这条约定就不是靠人自觉，而是靠**代码结构**保证的（写方法会直接报错）。
func Open(dir string, writable bool) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("auth: 存储目录没给")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("auth: 建目录 %s 失败: %w", dir, err)
	}
	s := &Store{
		dir:      dir,
		writable: writable,
		accounts: map[string]*Account{},
		byID:     map[int64]*Account{},
		codes:    map[string]*InviteCode{},
		sessions: map[string]*Session{},
	}
	// 开局必须**读成功**。读不出来就是读不出来 —— 绝不能"读失败就当空的"：
	// 那样管理面板会显示"一个账号都没有"，老板一慌就可能重建，把真数据覆盖掉 ✗
	if err := s.loadAccounts(); err != nil {
		return nil, err
	}
	if err := s.loadSessions(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) accountsPath() string { return filepath.Join(s.dir, AccountsFile) }
func (s *Store) sessionsPath() string { return filepath.Join(s.dir, SessionsFile) }

// Dir 返回存储目录（日志里说明"数据在哪儿"用）。
func (s *Store) Dir() string { return s.dir }

// ---- 读盘 ----

// readJSON 读一个 JSON 文件；主文件坏了就退回 .bak。
// 返回 (数据, 是否来自 .bak, err)。
func readJSON[T any](path string) (T, bool, error) {
	var out T
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在 = 从零开始（第一次启动）。这不是错误。
			return out, false, nil
		}
		return out, false, err
	}
	if err := json.Unmarshal(b, &out); err == nil {
		return out, false, nil
	} else {
		log.Printf("auth: %s 解析失败（%v），改用备份重试", path, err)
	}
	bak := path + ".bak"
	bb, err2 := os.ReadFile(bak)
	if err2 != nil {
		return out, false, fmt.Errorf("auth: %s 坏了，备份也读不到: %w", path, err2)
	}
	if err3 := json.Unmarshal(bb, &out); err3 != nil {
		return out, false, fmt.Errorf("auth: %s 和备份都坏了（主: %v 备: %v）", path, err, err3)
	}
	return out, true, nil
}

func (s *Store) loadAccounts() error {
	f, fromBak, err := readJSON[accountsFile](s.accountsPath())
	if err != nil {
		return err
	}
	if fromBak {
		log.Printf("auth: 账号表是从备份恢复的 —— 主文件曾经损坏，请检查磁盘")
	}
	s.accounts = make(map[string]*Account, len(f.Accounts))
	s.byID = make(map[int64]*Account, len(f.Accounts))
	// 🩸🩸 从 **1** 开始，不是 0。
	//
	// 0 是"没有"的意思 —— 邀请码的 UsedBy 用 0 表示未使用。要是第一个账号的
	// ID 是 0，那么它兑换时写下的 UsedBy 就是 0，`Used()` 会判成"还没用过"⇒
	// **同一张码能被兑换两次** ✗（2026-10-15 被并发测试当场抓出来过。）
	s.nextID = 1
	// 设置：老文件没有 Settings ⇒ 保持零值 false（= 仍然要邀请码）✓
	if f.Settings != nil {
		s.signupOpen = f.Settings.SignupOpen
	}

	for _, a := range f.Accounts {
		if a == nil || a.Email == "" {
			continue
		}
		s.accounts[a.Email] = a
		s.byID[a.ID] = a
		if a.ID >= s.nextID {
			s.nextID = a.ID + 1
		}
	}
	s.codes = make(map[string]*InviteCode, len(f.Codes))
	for _, c := range f.Codes {
		if c == nil || c.Code == "" {
			continue
		}
		s.codes[c.Code] = c
	}
	s.accStamp = stampOf(s.accountsPath())
	return nil
}

func (s *Store) loadSessions() error {
	f, fromBak, err := readJSON[sessionsFile](s.sessionsPath())
	if err != nil {
		return err
	}
	if fromBak {
		log.Printf("auth: 会话表是从备份恢复的")
	}
	s.sessions = make(map[string]*Session, len(f.Sessions))
	for _, x := range f.Sessions {
		if x == nil || x.TokenHash == "" {
			continue
		}
		s.sessions[x.TokenHash] = x
	}
	s.sesStamp = stampOf(s.sessionsPath())
	return nil
}

// reloadAccountsIfChanged 是**只读侧**（信令服务器）每次校验前调用的：
// 便宜（一次 stat），变了才真读。
//
// ⚠️ 重读失败**绝不能清空内存快照** —— 那会让所有正在联机的人当场掉线。
// 读失败就保持原样、只打日志，等下一轮。
func (s *Store) reloadAccountsIfChanged() {
	cur := stampOf(s.accountsPath())
	if cur.same(s.accStamp) {
		return
	}
	old := s.accounts
	oldByID := s.byID
	oldCodes := s.codes
	oldNext := s.nextID
	if err := s.loadAccounts(); err != nil {
		log.Printf("auth: 账号表变了但读不动（保持旧的一份继续跑）: %v", err)
		s.accounts, s.byID, s.codes, s.nextID = old, oldByID, oldCodes, oldNext
	}
}

func (s *Store) reloadSessionsIfChanged() {
	cur := stampOf(s.sessionsPath())
	if cur.same(s.sesStamp) {
		return
	}
	old := s.sessions
	if err := s.loadSessions(); err != nil {
		log.Printf("auth: 会话表变了但读不动（保持旧的一份继续跑）: %v", err)
		s.sessions = old
	}
}

// ---- 写盘 ----

// writeJSONAtomic 原子落盘。dir 要可写。
func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	// 🩸 fsync 不能省：不 fsync 的话 rename 只保证"名字换了"，
	// 内容可能还在页缓存里 —— 断电之后就成了个空文件 ✓
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	// 备份旧版（best-effort，失败了不影响这次写）。
	if old, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", old, 0o600)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir 让"改名"这件事也落盘。
// Windows 上对目录句柄 Sync 会失败 —— 所以是 best-effort，忍了。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

func (s *Store) mustWritable(what string) error {
	if !s.writable {
		// 这条错误本身就是防线：信令服务器永远不该走到这儿。
		return fmt.Errorf("auth: 本进程是只读的，不能%s（单写者约定，见包注释）", what)
	}
	return nil
}

func (s *Store) saveAccountsLocked() error {
	if err := s.mustWritable("改账号表"); err != nil {
		return err
	}
	f := accountsFile{Version: fileVersion}
	// 📌 显式写（不用 omitempty）：让人打开 accounts.json 就能看到开关状态 ✓
	f.Settings = &Settings{SignupOpen: s.signupOpen}
	f.Accounts = make([]*Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		f.Accounts = append(f.Accounts, a)
	}
	// 排序是为了**落盘内容稳定**：次序随机的话每次 diff 都全变，
	// 想拿"文件有没有变"当判据就没法用了。
	sort.Slice(f.Accounts, func(i, j int) bool { return f.Accounts[i].ID < f.Accounts[j].ID })
	f.Codes = make([]*InviteCode, 0, len(s.codes))
	for _, c := range s.codes {
		f.Codes = append(f.Codes, c)
	}
	sort.Slice(f.Codes, func(i, j int) bool {
		if f.Codes[i].CreatedAt != f.Codes[j].CreatedAt {
			return f.Codes[i].CreatedAt < f.Codes[j].CreatedAt
		}
		return f.Codes[i].Code < f.Codes[j].Code
	})
	if err := writeJSONAtomic(s.accountsPath(), &f); err != nil {
		return err
	}
	s.accStamp = stampOf(s.accountsPath())
	return nil
}

func (s *Store) saveSessionsLocked() error {
	if err := s.mustWritable("改会话表"); err != nil {
		return err
	}
	f := sessionsFile{Version: fileVersion, Sessions: make([]*Session, 0, len(s.sessions))}
	for _, x := range s.sessions {
		f.Sessions = append(f.Sessions, x)
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].CreatedAt < f.Sessions[j].CreatedAt })
	if err := writeJSONAtomic(s.sessionsPath(), &f); err != nil {
		return err
	}
	s.sesStamp = stampOf(s.sessionsPath())
	return nil
}

// pruneSessionsLocked 清掉过期会话。
//
// 📌 不清的话这个文件只涨不降：每登录一次多一条，一年下来几万条，
// 每次写盘都要把它们全序列化一遍 ⇒ 越用越慢。
func (s *Store) pruneSessionsLocked(now time.Time) int {
	cut := now.Unix()
	n := 0
	for h, x := range s.sessions {
		if x.ExpiresAt <= cut {
			delete(s.sessions, h)
			n++
		}
	}
	return n
}

// dropSessionsLocked 把某个账号的会话全部删掉。返回删了几条。
func (s *Store) dropSessionsLocked(accountID int64) int {
	n := 0
	for h, x := range s.sessions {
		if x.AccountID == accountID {
			delete(s.sessions, h)
			n++
		}
	}
	return n
}
