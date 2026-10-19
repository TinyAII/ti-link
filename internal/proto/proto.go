// Package proto 定义信令服务器与节点之间的消息格式。
// 传输方式是 TCP 上的「一行一个 JSON 对象」。不同 Type 只用其中部分字段。
package proto

import "strconv"

// Msg 是一条信令消息。
type Msg struct {
	Type string `json:"type"`

	// 房间与鉴权
	//
	// 注意区分两个「口令」：
	//   Token    —— 登录令牌（账号体系里的会话令牌，服务器每个连接都校验）
	//   RoomPass —— 房间密码（房主自己设的，朋友加入要填对）
	// 这两件事互不相干，别混着用。
	//
	// 📌 RoomPass **不再当隧道加密密钥**（原来是那样的）。现在隧道密钥靠
	// PubKey 做临时 ECDH 协商：服务器只见公钥、算不出密钥，
	// 所以房间密码设得再弱也不影响机密性 —— 它只管"放不放你进来"。
	Room     string `json:"room,omitempty"`
	RoomPass string `json:"roomPass,omitempty"`

	// Token 是**登录令牌**：账号体系里那把会话令牌（见 internal/auth）。
	//
	// 🩸 它替掉的是原来的 Pass —— 那个是**所有客户端共用一把**的访问口令，
	// 而且是**写死在客户端二进制里**的 ⇒ strings 一下就能挖出来 ⇒
	// 那时的"鉴权"实际上是「你有没有下载过客户端」✗
	//
	// 现在：每人一把、服务器逐个连接校验、能单独停用、能强制下线 ✓
	//
	// 空 = 没登录 ⇒ 服务器直接拒（除非它显式开在 -insecure-no-auth 下，那只给本地测试）。
	Token string `json:"token,omitempty"`

	// PubKey 是临时 ECDH 公钥（X25519，32 字节）。JSON 里是 base64。
	//
	// 用法：注册时带上自己的公钥，服务器转给对方，两边各自算会话密钥。
	// 私钥永远不出本机。**服务器拿着两把公钥也算不出密钥** —— 这就是它相比
	// "拿密码当密钥"的价值：连服务器都看不到游戏数据。
	PubKey []byte `json:"pubKey,omitempty"`

	// GamePort 是房主那边**游戏自己监听的端口**（本机 127.0.0.1 上那个）。
	//
	// 🩸 服务器本来**看不到**这个 —— 它只帮忙牵线、不碰游戏数据，
	// 房主的 `127.0.0.1:25565` 从头到尾没离开过他的机器 ✗
	// 所以要拦"敏感端口"就得让客户端**主动报上来** ✓
	//
	// ⚠️⚠️ **但客户端能撒谎** —— 改一下就能报别的数字 ✓
	// ⇒ 这道校验的强度上限是「**挡住不会改客户端的人**」✗
	//   它是"提高门槛"，**不是安全边界** ✓ 别当防线去宣传 ✗
	//   （想真挡住，得有身份能封禁 —— 那是另一件事 ✓）
	//
	// 0 表示"没报"（老式客户端 / 玩家侧不发）⇒ 服务器**不校验** ✓
	// 端口 0 本来就不合法，所以拿 0 当"没报"是安全的 ✓
	GamePort int `json:"gamePort,omitempty"`

	// GamePort2 是**同一款游戏的第二个端口**，目前只有《饥荒》用得上。
	//
	// 🩸 为什么需要它：《饥荒》主世界走 UDP 10999、**洞穴另走 UDP 10998**。
	//    玩家跳进虫洞下地底时，客户端会**断开 10999、改连 10998** ✗
	//    ⇒ 只打通地上那一路的话，玩家一进洞穴就掉线 ✓
	//
	// 📌 语义跟 GamePort 完全一致：0 = 没有 / 没报，服务器不校验 ✓
	// 📌 顺序有意义：GamePort 是"主"（地上），GamePort2 是"附"（洞穴）——
	//    界面上出错时优先保主端口 ✓
	GamePort2 int `json:"gamePort2,omitempty"`

	// Game 是**房主上报的自己正在开哪款游戏**（名字取自客户端那份游戏清单）。
	//
	// 用途只有两个，**都不是安全边界**：
	//	① 后台看板展示 + 按游戏筛选、事后追责（"谁老在开什么"）✓
	//	② 同一房里两边报的游戏不一致时，客户端提示"可能连错了"（防**误连**）✓
	//
	// 🩸🩸 **它是客户端自报的，改一下就能伪装** —— 跟上面 GamePort 同一性质：
	// 只能当"展示"，**不是防线** ✗
	//
	// 特别是：「**两个客户端互相校验游戏名**」这件事**证明不了对面软件没被改** ——
	// 判断方（校验逻辑）和被判断方**都在用户自己手里**，改客户端的人可以同时改校验 ✗
	// 这跟"改个 exe 文件名就能绕"是同一类错误。**别把它当防护去写、去宣传** ✗
	//
	// 🩸 **这个字段是必填的**（2026-10-16 晚起）：
	//   · 建房 / 加入时没带它 ⇒ 服务器**直接拒** ✓
	//   · 理由：这个项目在测试阶段、**没有兼容负担** —— 让用户更新客户端就行，
	//     不必为"老客户端"留一条"没报也能进"的路（那种口子只会越留越大）✗
	//   · 所以别再写"空就放行"那种分支了 ✗
	//
	// 空串只在一种情况下合法：**服务端内部还没赋值**（解析前后）✓
	Game string `json:"game,omitempty"`

	// 身份
	Role     string `json:"role,omitempty"`     // host | join
	ClientID string `json:"clientID,omitempty"` // 客户端本地生成的持久 ID（拉黑认人用）
	Label    string `json:"label,omitempty"`    // 昵称，如「小明」
	// Avatar 是内置头像库的下标（一个数字，不是图）。
	// 图库两端各自内置一份，网络上只跑这个下标 —— 传头像本身太占带宽，
	// 而且图库是固定的一小撮，传数字等价且省得多。
	// 0 是合法下标，所以不带 omitempty 的语义差异要心里有数：
	// 老客户端不发这个字段 → 收到 0 → 显示第 0 张，无害。
	Avatar int `json:"avatar,omitempty"`

	// 候选地址
	Cands []string `json:"cands,omitempty"` // 本端候选
	Peer  []string `json:"peer,omitempty"`  // 对端候选

	// 定向操作（踢 / 拉黑 / 移出黑名单）
	TargetID string `json:"targetID,omitempty"` // 房间内的连接 ID（踢 / 拉黑用）
	BanKey   string `json:"banKey,omitempty"`   // 黑名单条目的键（= 该人的 clientID），移出时回传
	// Seat 跟 TargetID 一起用：pair 里告诉房主"这位朋友是第几个玩家"，
	// 房主日志里好把它拼成「玩家 1」（Label 是空的时候，光有名字拼不出来）。
	Seat int `json:"seat,omitempty"`

	// Left 是服务器 -> 房主，随 TypeLeave 一起来，说明这位朋友是**怎么走的**：
	//   true  = 主动退出（客户端发了 leave）
	//   false = 非正常退出（直接关掉程序 / 断网，leave 没送到）
	//
	// 🩸 两种断开在 TCP 层面**一模一样**（都是连接断掉），服务器没法从连接本身分辨，
	// 只能靠客户端在退出前主动发一条 leave 来区分。老客户端不发这条 ⇒ 一律当非正常退出。
	// 用 omitempty 是协议向后兼容的铁律：老端收到这个字段只是忽略，不会报错。
	Left bool `json:"left,omitempty"`

	// 房间配置
	MaxSize int `json:"maxSize,omitempty"`

	// 名单
	Members []Member `json:"members,omitempty"`
	Bans    []Ban    `json:"bans,omitempty"`

	// 文案
	Err    string `json:"err,omitempty"`    // 给程序看的错误
	Notice string `json:"notice,omitempty"` // 给玩家看的提示
}

// Member 是房间成员在名单里的样子。
type Member struct {
	ID string `json:"id"`
	// Label 是昵称，**纯名字、不带角色前缀**；没起名就是空串。
	// 界面负责拼「房主 / 玩家」那截前缀 —— 服务器别替它拼，
	// 不然客户端再拼一次就成了「玩家 玩家3」。
	Label string `json:"label"`
	// Host 标出哪一条是房主 —— 界面靠它区分，别拿 label 去猜
	// （房主也可以起名叫「房主」，猜不得）。
	Host bool `json:"host,omitempty"`
	// Seat 是「第几个玩家」：房主 0，朋友从 1 起，按当前房内最小空号分配。
	// 界面在对方没起昵称时用它拼「玩家 N」。
	// 编号由服务器给、客户端不要自己数 —— 两边各数一遍迟早会岔。
	Seat int `json:"seat,omitempty"`
	// Avatar 是内置头像库的下标。两端各自查自己那份图库，
	// 索引越界（图库不一样/对方版本老）就退回用名字首字画一个。
	Avatar int `json:"avatar,omitempty"`

	// Game 是这位成员上报的"我在开哪款游戏"（房主报房主的、朋友报自己的）。
	//
	// 📌 它给客户端一个**防误连**信号：同一个房里两边报的游戏不一样就提示
	// "你们开的可能不是同一款" ✓（比"连上去才发现进错游戏"强）
	//
	// 🩸 它**不是**"校验对方软件有没有被改过" —— 那种事做不到
	// （判断方和被判断方都在用户手里），详见 Msg.Game 那段注释 ✗
	// 空 = 这位没上报 ⇒ 界面按"未上报"显示，**不参与比较** ✓
	Game string `json:"game,omitempty"`
}

// Ban 是黑名单里的一条。
//
// 按「人」合并：一个人一条，里面同时含他的设备 ID 与公网 IP
// （当初就是这两样一起拉黑的，解除时也要一起解除）。
type Ban struct {
	Key   string `json:"key"`   // 移出时回传 = 该人的 clientID
	Label string `json:"label"` // 昵称
	Short string `json:"short"` // clientID 前 6 位：昵称重名时用来区分
}

// DisplayName 把成员拼成给人看的名字：
//
//	房主 / 玩家 阿强 / 玩家 1（没起名就用座位号兜底）
//
// 界面上的成员列表要分色渲染（角色一截、名字一截），所以它自己拼；
// 日志、黑名单这些纯文本的地方走这里 —— 规则只写一份，省得几处各拼一遍拼岔。
func DisplayName(label string, seat int, host bool) string {
	role := "玩家"
	if host {
		role = "房主"
	}
	if label != "" {
		return role + " " + label
	}
	if !host && seat > 0 {
		return role + " " + strconv.Itoa(seat)
	}
	return role
}

// MemberName 是 DisplayName 的成员版，方便直接拿名单里的一条来用。
func (m Member) DisplayName() string { return DisplayName(m.Label, m.Seat, m.Host) }

const (
	TypeRegister = "register"  // 客户端 -> 服务器：role/room/token/roomPass/clientID/label/cands/maxSize/pubKey
	TypeRoomOK   = "room_ok"   // 服务器 -> host：建房成功
	TypePair     = "pair"      // 服务器 -> host：有新人来了（Peer = 该朋友的候选，PubKey = 该朋友的公钥）
	TypeHostCand = "host_cand" // host -> 服务器：为某个朋友准备的房主候选（TargetID + Cands）
	TypePeer     = "peer"      // 服务器 -> join：房主候选（Peer）
	// TypeReCand 是「重开端口之后重发候选」（2026-10-18 加，用于打洞重协商）。
	//
	// 方向：加入方 -> 服务器 -> 房主。
	// 🩸 房主 -> 加入方**不需要**新类型：房主照旧发 TypeHostCand，
	//    服务器转成 TypePeer 给加入方 —— 对加入方来说"候选来了"就是"可以打洞了" ✓
	//
	// 为什么加入方必须能有这一条：它的候选原来**只在注册那一刻发一次**，
	// 打洞失败后重开 socket，新端口对方根本不知道 ✗
	TypeReCand = "re_cand"
	// TypeHostKey 服务器 -> join：房主的临时公钥。
	// 单独一条消息，是因为朋友在打洞**之前**就必须拿到它 —— 隧道密钥要先算出来，
	// 连打洞探测包都要拿它签名。
	TypeHostKey  = "host_key"
	TypeMembers  = "members"   // 服务器 -> host：名单变动
	TypeKick     = "kick"      // host -> 服务器：踢出（可再进）
	TypeBan      = "ban"       // host -> 服务器：拉黑（不可再进）
	TypeKicked   = "kicked"    // 服务器 -> 被踢者
	TypeBanned   = "banned"    // 服务器 -> 被拉黑者
	TypeHostGone = "host_gone" // 服务器 -> 所有人：房主断开
	// TypeLeave 客户端 -> 服务器：朋友主动退出房间（不是被踢、不是断线）。
	// 服务器收到后转告房主，好让房主日志把「退出了房间」和「非正常退出」分开。
	// 房主自己关房间不用发这条 —— 服务器靠房主那条连接断开就知道了。
	TypeLeave = "leave"

	// 房间密码与黑名单
	TypeSetPass = "set_pass" // host -> 服务器：改房间密码（RoomPass）
	TypeBanList = "ban_list" // 服务器 -> host：黑名单列表（Bans）
	TypeUnban   = "unban"    // host -> 服务器：把某人移出黑名单（BanKey）

	TypeError = "error"

	// TypeNotice 是【服务器 -> 任意一端】的一句**纯提示**：只显示，不改变任何状态 ✓
	//
	// 🩸 跟 TypeError / TypeKicked 那几种**性质完全不同** —— 那些是"这事结束了"，
	// 收到就得中断；这个只是"顺便告诉你一句"，收到之后该干嘛还干嘛 ✓
	// 所以客户端**不能**把它当结束信号处理（别 return）✗
	//
	// 📌 加它的原因：房间的游戏名和加入者报的对不上时，要给两边各提一句 ——
	// 那是**防进错房间**（手滑），不是准入判定 ✓
	TypeNotice = "notice"
)
