// Package auth 管账号、邀请码与会话 —— 也就是"谁有资格连服务器"这件事。
//
// 它替掉的是什么，得说清楚：在此之前，客户端二进制里写死了共用口令
// （cmd/gui 的 defaultPass），`strings` 一下就能挖出来 ⇒ 那时所谓的"鉴权"
// 实际上是「你有没有下载过客户端」。账号 + 邀请码把这句话改成
// 「你有没有被人发过码」，而且每人一把凭据，能单独停用。
//
// ── 为什么不是数据库 ──────────────────────────────────────────────
//
// 数据量是"几百个账号"。真引一个库（哪怕纯 Go 的）换不来什么，
// 反而多背一份依赖、多一截编译时间，还要处理 schema 迁移。
// 一个 JSON 文件 + 原子替换就够了 —— 简单到能一眼看完，这本身就是好处。
//
// ── 单写者约定（整套设计的地基，别破） ──────────────────────────────
//
//	accounts.json —— **只有管理面板**能写
//	sessions.json —— **只有管理面板**能写
//	信令服务器   —— **只读**，任何情况下都不写盘
//
// 为什么把信令侧钉死成只读：它必须一直在，而"写盘"会带来锁、半截文件、
// 磁盘满这些"能把一个只读服务拖死"的问题。只读就没有这些。
//
// 为什么两个文件各只有一个写者：省掉跨进程锁。两个进程同时读改写同一个文件
// 必然丢更新，而要做对就得引入文件锁 —— 那是个跨平台的坑（Linux 用 flock、
// Windows 用 LockFileEx），能不碰就不碰。分开写各写各的，问题从根上没了。
//
// 读写的方式：
//   - 写：临时文件 → fsync → rename（原子）⇒ 读的一边永远看到完整的一份，
//     不存在"读到写了一半"这种事；顺手留一份 .bak。
//   - 读：持有内存快照，靠 mtime + size 判断要不要重读（信令侧每次校验前查一次）。
package auth

import (
	"errors"
	"time"
)

// 角色。目前只有两种：普通用户，和管理员（只有管理员能进后台）。
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// 账号状态。停用是"立刻断"：信令侧每次校验都会重读账号表，
// 所以停用之后**下一个连接**就进不来了（已经在房间里的不受影响 —— 房间是内存态，
// 见 docs/账号与邀请码.md 的"停用的边界"）。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

const (
	// SessionTTL 是会话的寿命，**从登录那一刻算，不滑动**。定 30 天是想让
	// "记住我"这件事真的成立 —— 一个要联机的工具，隔三差五弹登录没人受得了。
	//
	// 🩸 为什么不做"滑动续期"（有活动就往后顺延）：要续期就得**写盘**，
	// 而校验是信令服务器每来一个连接都要走的路径 —— 那是它的主循环，
	// 不能为了续命往里插磁盘写 ✗ 而信令侧一旦要写，"只读"这条地基就没了。
	// 代价是满 30 天要重新登录一次。这个代价我认为值得。
	SessionTTL = 30 * 24 * time.Hour

	// MinPassRunes 密码最短（按字符数）。
	// 不强制大小写数字符号 —— 现在通行的建议是"长度优先"，强制复杂度
	// 只会把人逼成 Abc123! 这种反而更好猜的东西。
	MinPassRunes = 8
	// MaxPassBytes 密码最长（按**字节**数）。
	// 🩸 bcrypt 只看前 72 字节，超了的部分**静默丢弃** ⇒ 不卡上限的话，
	// 用户以为设了个 100 位的密码，实际后面 28 位根本没参与运算 ✓
	MaxPassBytes = 72

	// MaxEmailLen 邮箱长度上限（RFC 5321 的规定值）。
	MaxEmailLen = 254
)

// 这几个错误是给界面直接用的：字符串就是能展示给用户的中文。
//
// 📌 登录失败**只有一种**错误（ErrBadLogin），不区分"没这个邮箱"和"密码不对" ——
// 分开说等于给了一个"这个邮箱注册过没有"的查询接口 ✗
// （注册那儿的 ErrEmailTaken 避不开，但它得先有有效邀请码才走得到 ✓）
var (
	ErrBadCode    = errors.New("邀请码无效或已经被用过了")
	ErrEmailTaken = errors.New("这个邮箱已经注册过了")
	ErrBadLogin   = errors.New("邮箱或密码不对")
	ErrDisabled   = errors.New("这个账号已被停用")
	ErrNotFound   = errors.New("找不到这个账号")
	ErrBadOldPass = errors.New("原密码不对")
)

// InputError 是"用户填的东西不合规"，Msg 就是能直接给他看的中文。
//
// 🩸 为什么要单独一个类型，而不是上面那种哨兵错误：
// 上面那几个是**业务结论**（码被用了、账号停用了），调用方要按它决定
// "回 409 还是 403"；而这一种是**用户输入问题**，处理方式都一样
// （原话回给他、回 400）✓ 混作一谈的话，"保存失败"这种我们自己的故障
// 会被当成"你填错了"回给用户 —— 那是最误导人的一种错误 ✗
//
// 结果就是调用方一句 `var ie *InputError; errors.As(err, &ie)` 就能分辨，
// 剩下的归"我们自己的麻烦"，对外只说"稍后再试"、细节进日志 ✓
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

func inputErr(msg string) error { return &InputError{Msg: msg} }

// Account 是一个账号。**里面没有明文密码，也没有可逆的密码** ——
// 只有 bcrypt 哈希（bcrypt 本身带盐、单向），所以这份文件就算被拿走也拿不到密码。
type Account struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"` // 已规范化（小写、去空白），见 NormalizeEmail
	PassHash string `json:"passHash"`
	Role     string `json:"role"`   // user | admin
	Status   string `json:"status"` // active | disabled
	// Code 是注册时用的那个邀请码 —— 留着是为了**追溯**
	// （"这个账号是谁发的码"）。邀请码上还有 Note 字段记"发给谁了"。
	Code      string `json:"code,omitempty"`
	CreatedAt int64  `json:"createdAt"`
	// DisabledAt 停用的时刻。
	DisabledAt int64 `json:"disabledAt,omitempty"`
	// RevokeBefore 是"这个时刻之前建立的会话一律作废"，用来做**强制下线**。
	//
	// 🩸 为什么不直接去 sessions.json 里把某个人的会话删掉：
	// 那个文件的写者只能是管理面板（单写者约定），绕开它去改就变成了两个写者 ✗
	// 改成"在账号上记一个时间线"，信令侧校验时顺手比一下就有了 ✓
	// 效果一样（旧 token 立刻失效），代价是多存一个整数 ✓
	RevokeBefore int64 `json:"revokeBefore,omitempty"`
}

// InviteCode 是一个邀请码。**一码一人**：UsedBy 一旦被写上就再也不改回去。
type InviteCode struct {
	Code      string `json:"code"` // 规范形式（10 位 Crockford Base32），见 NormalizeCode
	Batch     string `json:"batch,omitempty"`
	Note      string `json:"note,omitempty"` // "发给谁了" —— 追责靠它
	CreatedAt int64  `json:"createdAt"`
	UsedBy    int64  `json:"usedBy,omitempty"`
	UsedAt    int64  `json:"usedAt,omitempty"`
}

// Used 说这个码是不是已经被兑换过。
func (c *InviteCode) Used() bool { return c.UsedBy != 0 }

// Session 是一次登录。
//
// 🩸 存的是**哈希**不是 token 本身：这份文件被拿走时，攻击者拿到的是一串
// 用不上的 sha256，而不是一串能直接连服务器的凭据 ✓
// （token 本身是 32 字节随机数，猜不出来；存哈希是为了"文件泄露"这个场景。）
type Session struct {
	TokenHash string `json:"tokenHash"` // sha256(token) 的十六进制
	AccountID int64  `json:"accountId"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

// Valid 判断会话本身是否还在有效期内（不涉及账号状态 —— 那要查账号表）。
func (s *Session) Valid(now time.Time) bool {
	return now.Unix() < s.ExpiresAt
}
