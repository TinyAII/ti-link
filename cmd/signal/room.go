// 房间：房主侧。
//
// 房间就是「一个房主 + 一串成员 + 一份黑名单 + 一个房间密码」。
// 房主那条连接一直挂着 —— 它会转发朋友候选、执行踢人/拉黑/改密码/解除拉黑，
// 断开时通知全房。
package main

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"p2plink/internal/auth"
	"p2plink/internal/netutil"
	"p2plink/internal/nick"
	"p2plink/internal/proto"
	"p2plink/internal/signal"
)

const (
	defaultMaxSize = 4
	hardMaxSize    = 100
	// 昵称上限。界面上就一行字，太长全是噪音；顺带防着有人拿超长字符串灌服务器。
	maxLabelLen = 12
)

var (
	roomsMu sync.Mutex
	rooms   = map[string]*room{}
)

// member 是房间里的一位（房主也算一条）。
type member struct {
	id       string // 本次连接的唯一 ID。朋友是 clientID-seq —— 同机多开时 clientID 会重复
	cid      string // 客户端持久 ID。拉黑认人、解除黑名单都认它（免得多开一次就换个马甲）
	label    string // 昵称（纯名字，没起名就是空串）
	seat     int    // 第几个玩家：房主 0，朋友从 1 起
	avatar   int    // 内置头像库的下标（不是图）
	pub      []byte // 临时 ECDH 公钥（X25519，32 字节）；空 = 老客户端
	cl       *signal.Client
	remoteIP string

	// game 是这位上报的"我在开哪款游戏"（清洗过的）。
	//
	// 🩸 **用户可控** —— 只用来展示 + 防误连，**不是防线** ✗（见 proto.Msg.Game）
	game string

	// joinedAt 是这位**进房**的时刻。只给后台详情页看（"加入多久"）✓
	// 📌 不落盘、不写日志 —— 房间本来就是内存态，这份数据跟着房间一起消失 ✓
	joinedAt time.Time

	// counting 是这条连接上"他自己"收发了多少字节（信令流量，**不是游戏数据**）。
	//
	// 📌 游戏数据走两台机器直连、根本不经过这儿 ⇒ 这个数**很小**是正常的，
	// 界面上要写清"这是跟服务器的信令流量"，不然会被误读成"他在传游戏数据" ✗
	counting *countingConn
}

// ban 是黑名单里的一条，按「人」合并：一次拉黑同时记下设备 ID 与公网 IP，
// 解除时也是一起解除（当初就是按这两样拉的，拆开解除等于没解干净）。
type ban struct {
	cid   string
	ip    string
	label string
}

type room struct {
	mu      sync.Mutex
	code    string
	pass    string // 房间密码，空 = 不设防
	host    *member
	joins   []*member
	maxSize int
	seq     int             // 连接序号：同机多开时 clientID 会重复，靠它凑出唯一 id（**不是**给界面看的座位号）
	bans    map[string]*ban // key = clientID

	// passFails 是"房间密码连续错了几次"的记账（key = **来源 IP**，不是 clientID ——
	// 那个是客户端自报的，能随便换 ✗）
	//
	// 🩸 规则见 passfail.go：错 3 次锁 30 秒、之后翻倍、上限 10 分钟，
	//    锁定期间**正确密码也进不来**（老板 2026-10-17 拍板）✓
	// 📌 只存内存（房间本来就是内存态），房间关掉自动没了 ✓ 不落盘 ✓
	passFails map[string]*passFail

	// openedAt 是这局什么时候开的。只给后台的"当前房间"列表看（"开了多久"）。
	openedAt time.Time

	// accountID 是**房主登录的那个账号**。
	// 有它才谈得上"追责"：后台看到某个房间，能顺着它查到是谁开的。
	// 0 = 没有账号（本地用 -insecure-no-auth 跑的，那种情况本来就没有账号）✓
	//
	// 📌 只存在内存里（房间本来就是内存态），不落盘、不写日志 ✓
	accountID int64

	// hostIP 是房主建这个房间时用的 IP。
	//
	// 存它是为了**房间关掉时把"单 IP 房间名额"还回去** ✓
	// （名额是按 IP 记的，房间自己得记得是哪个 IP 占的 ✓）
	//
	// ⚠️ 别拿它当"房主身份"用 —— IP 会变（重拨、换网），它不是身份 ✗
	hostIP string

	// closeReason 是"这个房间为什么关的"，给房里的人看的那句话。
	//
	// 空 = 房主自己断的（默认那句）；管理员从后台关的话会先把它写上，
	// 清理那条路照它发通知 ✓
	//
	// 📌 为什么不是"直接躺着发通知"：房间是**房主连接断开**时才真正清掉的
	// （那条路里还要还名额、删表），管理员只能"请求它关"，
	// 真正的收尾必须走同一条路 ⇒ 中间需要一个地方先把"理由是啥"存下来 ✓
	closeReason string
}

func (r *room) membersLocked() []proto.Member {
	out := make([]proto.Member, 0, 1+len(r.joins))
	if r.host != nil {
		out = append(out, proto.Member{ID: r.host.id, Label: r.host.label, Host: true,
			Seat: r.host.seat, Avatar: r.host.avatar, Game: r.host.game})
	}
	for _, j := range r.joins {
		out = append(out, proto.Member{ID: j.id, Label: j.label, Seat: j.seat,
			Avatar: j.avatar, Game: j.game})
	}
	return out
}

// nextSeatLocked 给新来的朋友发个座位号：当前没人占的最小正整数。
//
// 用「最小空号」而不是「只增计数器」—— 老写法（`seq++`）会把退过又进来的人
// 一路顶到「玩家3」：房里明明只有俩人，却显示玩家3，看着像出错了。
// 缺口补上之后，第一个进来的永远是「玩家1」。
func (r *room) nextSeatLocked() int {
	used := map[int]bool{}
	for _, j := range r.joins {
		used[j.seat] = true
	}
	for i := 1; i <= hardMaxSize; i++ {
		if !used[i] {
			return i
		}
	}
	return len(r.joins) + 1
}

func (r *room) pushMembersLocked() {
	if r.host != nil {
		r.host.cl.Send(proto.Msg{Type: proto.TypeMembers, Members: r.membersLocked()})
	}
}

// pushMembersToJoinsLocked 把名单也推给房里的朋友一份 —— 他们那一页也有成员列表，
// 不能老是空的。
//
// **故意不带 ID**：朋友不能踢人也不能拉黑，没必要把别人的设备 ID（持久、
// 跨房间不变）摊给同房的陌生人看；房主那份才带 ID，踢人/拉黑要用。
func (r *room) pushMembersToJoinsLocked() {
	if len(r.joins) == 0 {
		return
	}
	ms := r.membersLocked()
	for i := range ms {
		ms[i].ID = ""
	}
	for _, j := range r.joins {
		j.cl.Send(proto.Msg{Type: proto.TypeMembers, Members: ms})
	}
}

// broadcastMembersLocked 名单一变就两边都推：房主那份带 ID，朋友那份不带。
func (r *room) broadcastMembersLocked() {
	r.pushMembersLocked()
	r.pushMembersToJoinsLocked()
}

// bansLocked 把黑名单摊成列表。只给昵称和设备 ID 前几位 ——
// IP 是敏感信息，服务器记着就够了，不下发给客户端。
func (r *room) bansLocked() []proto.Ban {
	out := make([]proto.Ban, 0, len(r.bans))
	for _, b := range r.bans {
		out = append(out, proto.Ban{Key: b.cid, Label: b.label, Short: shortID(b.cid)})
	}
	return out
}

func (r *room) pushBansLocked() {
	if r.host != nil {
		r.host.cl.Send(proto.Msg{Type: proto.TypeBanList, Bans: r.bansLocked()})
	}
}

// findBanLocked 看这人是否在黑名单里：先认设备 ID，再认公网 IP。
//
// 认 IP 是兜底 —— 他把 %AppData% 删了换个新 ID 还能靠 IP 拦住；
// 代价是同一出口 IP 的其他人（宿舍、公司）会被一起挡在门外，
// 这是拉黑 IP 固有的取舍，界面上按人显示、按人解除。
func (r *room) findBanLocked(cid, ip string) *ban {
	if cid != "" {
		if b, ok := r.bans[cid]; ok {
			return b
		}
	}
	if ip != "" {
		for _, b := range r.bans {
			if b.ip == ip {
				return b
			}
		}
	}
	return nil
}

// uniqueLabelLocked 让同一个房间里不出现两个一模一样的昵称
// （重名的话列表里两行「小明」，房主都不知道该踢哪个）。
func (r *room) uniqueLabelLocked(base string) string {
	if base == "" {
		return base
	}
	used := map[string]bool{}
	if r.host != nil {
		used[r.host.label] = true
	}
	for _, j := range r.joins {
		used[j.label] = true
	}
	if !used[base] {
		return base
	}
	for i := 2; i <= 99; i++ {
		cand := base + strconv.Itoa(i)
		if !used[cand] {
			return cand
		}
	}
	return base
}

// serveHost 处理房主：建房、转发候选、执行踢人/拉黑/改密码/解除拉黑，房主断开时通知所有人。
func serveHost(cl *signal.Client, cc *countingConn, maskedRemote, ip string, m proto.Msg, owner *auth.Account) {
	// 🩸 **没说开哪款游戏 ⇒ 不许开房**（2026-10-16 晚）。
	//
	// 为什么连源头也管：房间要是没有游戏名，别人就无从判断"我该不该进" ⇒
	// 那等于给所有人开了一条"随便进"的路 ✗
	// 📌 放在最前面：别先做一堆准备工作再拒 ✓
	if why := refuseHostGame(sanitizeGame(m.Game)); why != "" {
		log.Printf("%s 想建房但没说明游戏名，拒了", maskedRemote)
		cl.Send(proto.Msg{Type: proto.TypeError, Err: why})
		return
	}

	maxSize := m.MaxSize
	if maxSize <= 0 {
		maxSize = defaultMaxSize
	}
	if maxSize > hardMaxSize {
		maxSize = hardMaxSize
	}

	roomsMu.Lock()
	if _, exists := rooms[m.Room]; exists {
		roomsMu.Unlock()
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "房间 ID 已被占用，换一个"})
		return
	}
	if len(rooms) >= maxRooms {
		roomsMu.Unlock()
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "服务器房间已满，请稍后再试"})
		return
	}
	roomsMu.Unlock()

	// ⭐ 单 IP 的房间数上限 —— **这条用户绕不过去**（判定在服务器 ✓）
	//
	// 🩸 上面那个是**全局**上限，一个人就能把它占满 ✗
	// 这条管的是"别让一个人占太多名额" ✓
	//
	// 📌 放在 roomsMu 之外调：allowRoom 自己拿 ipMu，
	// 两个锁嵌套的话要小心顺序（room.go 里别的路径是先 roomsMu 后 ipMu）✗
	if !allowRoom(ip) {
		cl.Send(proto.Msg{Type: proto.TypeError,
			Err: "你这边同时在开的房间太多了，先关掉一个再开新的"})
		return
	}

	roomsMu.Lock()
	// 拿锁这段时间里可能有人抢先建了同名房间 ⇒ 再查一次 ✓
	if _, exists := rooms[m.Room]; exists {
		roomsMu.Unlock()
		releaseRoom(ip) // ⚠️ 名额要还回去，不然白扣一个 ✗
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "房间 ID 已被占用，换一个"})
		return
	}
	// 昵称先清洗：超长截断 + 不在词库里的当没起名（见 sanitizeLabel）
	hostLabel := sanitizeLabel(m.Label)
	r := &room{code: m.Room, pass: m.RoomPass, maxSize: maxSize,
		bans: map[string]*ban{}, hostIP: ip, openedAt: time.Now()}
	if owner != nil {
		r.accountID = owner.ID
	}
	// 昵称只存纯名字，没起名就是空串 —— 界面上那截「房主 / 玩家」前缀由客户端拼
	r.host = &member{id: m.ClientID, cid: m.ClientID, label: hostLabel, avatar: m.Avatar,
		pub: m.PubKey, cl: cl, remoteIP: ip, game: sanitizeGame(m.Game),
		joinedAt: time.Now(), counting: cc}
	rooms[m.Room] = r
	roomsMu.Unlock()
	roomOpened()

	// 历史：记"开房"，再给房主补一条 join ——
	// 🩸 不补的话后台的成员列表里**没有房主**，一局看着像"没人" ✗
	histRecord(histEvent{Kind: histOpen, Room: r.code, Game: r.host.game, Acct: r.accountID})
	histRecord(histEvent{Kind: histJoin, Room: r.code, Acct: r.accountID,
		CID: r.host.cid, Label: hostLabel, Seat: 0, Host: true})

	// 名字不在词库里 → 上面已经按"没起名"处理了。
	// ⚠️ 只说"有这回事"，**不把那个名字写进日志** ✗ —— 那正是要避免的东西。
	if m.Label != "" && hostLabel == "" {
		log.Printf("房间 %s：房主来自 %s，名字不在词库里，按未起名处理", m.Room, maskedRemote)
	}

	// 日志里只说有没有密码，绝不说密码是什么
	if r.pass == "" {
		log.Printf("房间 %s 已创建（房主 %s，上限 %d 人，无密码）", m.Room, maskedRemote, maxSize)
	} else {
		log.Printf("房间 %s 已创建（房主 %s，上限 %d 人，已设密码）", m.Room, maskedRemote, maxSize)
	}
	// RoomPass 回给客户端：界面要靠它显示「房间密码」那一行
	cl.Send(proto.Msg{Type: proto.TypeRoomOK, Room: m.Room, MaxSize: maxSize, RoomPass: r.pass})
	r.mu.Lock()
	// 🩸 开局也要推一份名单 —— 而且要**把房主自己带进去**。
	// 原来这儿只推了黑名单、没推名单，结果房主在自己房间里看不见自己这一行；
	// （界面上那个「成员 (1 / 4)」是页面写死的初始值，所以看着像"有一个人但没显示"。）
	// 得等第一个朋友进来才会推到名单 —— 在那之前房主一直是空的。
	r.pushMembersLocked()
	// 开局先推一份空黑名单：客户端「管理黑名单」弹窗就不用再去问一次了，
	// 往后每次变动服务器都会重新推。
	r.pushBansLocked()
	r.mu.Unlock()

	// 🩸 **房主会话周期复查**（2026-10-17 补）：
	// 原来 VerifyToken 只在 register 那一刻调一次 ⇒ 连接建好之后，
	// 管理员在面板上点"强制下线 / 停用"，对这条连接**完全无效** ✗
	// 房主一挂就是几小时 ⇒ 追责和止损窗口被无限拉长 ✗
	// ⇒ 起个每 30 秒复查一次的小循环，失效就关连接；关掉之后下面那个
	//   Recv 循环会报错退出，走**本来就有的收尾路径** ✓（不用另写清理）
	stopRecheck := make(chan struct{})
	defer close(stopRecheck)
	go recheckHostSession(cl, m.Room, m.Token, stopRecheck, sessRecheckEvery)

	for {
		in, err := cl.Recv(0)
		if err != nil {
			break
		}
		switch in.Type {
		case proto.TypeHostCand:
			forwardHostCands(r, in.TargetID, in.Cands, in.GamePort2)
		case proto.TypeKick:
			removeMember(r, in.TargetID, proto.TypeKicked, "房主将你踢出房间", "踢出", "kicked")
		case proto.TypeBan:
			removeMember(r, in.TargetID, proto.TypeBanned, "房主已经将你拉黑，你不可再进入该房间", "拉黑", "banned")
		case proto.TypeSetPass:
			setRoomPass(r, in.RoomPass)
		case proto.TypeUnban:
			unban(r, in.BanKey)
		}
	}

	log.Printf("房间 %s 的房主已断开，通知房内 %d 人", m.Room, len(r.joins))
	r.mu.Lock()
	// 文案分两种：房主自己断的 / 管理员从后台关的。
	// 📌 不区分的话房主会一脸懵（"我没关啊"），房里的人还会一直等他回来 ✗
	notice := r.closeReason
	if notice == "" {
		notice = "房主已断开连接"
	}
	for _, j := range r.joins {
		j.cl.Send(proto.Msg{Type: proto.TypeHostGone, Notice: notice})
	}
	r.host = nil
	r.joins = nil
	r.mu.Unlock()

	histRecord(histEvent{Kind: histClose, Room: r.code})

	roomsMu.Lock()
	delete(rooms, m.Room)
	roomsMu.Unlock()

	// ⭐ 把"单 IP 房间名额"还回去 ✓
	//
	// 🩸 **必须在房间真的被删掉这条路径上** —— 漏了的话名额只减不增，
	// 那个用户以后就再也开不了房了 ✗
	// 而且这种 bug 特别隐蔽：第一次开房好好的、关了再开就不行了 ✓
	releaseRoom(r.hostIP)
}

// closeRoomByAdmin 从后台强制关掉一个房间（面板上那个「关闭」按钮走到这儿）。
//
// 🩸🩸 **它只做一件事：把房主的连接关掉** —— 然后让上面那条清理路径自己收尾
// （通知房里的人 / 从表里删掉 / 还"单 IP 房间名额"）。
//
// 为什么不干脆自己 delete(rooms, id)：
//
//	· 那条清理路径里有 releaseRoom(hostIP) —— 名额只减不增的话，
//	  那个房主以后**再也开不了房**，而且特别难查：第一次好好的，关了再开就不行 ✗
//	· 只删表不关连接的话，房主还以为自己在开房、连接还挂着 ⇒ 房间成了孤儿 ✗
//
// 返回的是给后台页面看的一句话。
func closeRoomByAdmin(roomID string) error {
	roomsMu.Lock()
	r := rooms[roomID]
	if r == nil {
		roomsMu.Unlock()
		return errors.New("没有这个房间（可能刚被关掉了）")
	}
	r.mu.Lock()
	if r.host == nil {
		r.mu.Unlock()
		roomsMu.Unlock()
		return errors.New("这个房间的房主已经不在了")
	}
	r.closeReason = "管理员关闭了这个房间"
	host := r.host
	r.mu.Unlock()
	roomsMu.Unlock()

	// 🩸 **先告诉房主一句，再断他的连接** ——
	// 不先说一声的话，他那边看到的是"与穿透服务器的连接断了，检查一下网络"，
	// 于是他会去查自己的网、然后重试，而其实是被管理员关了 ✗（实测过）
	//
	// 📌 为什么敢直接发 TypeHostGone 给房主：客户端那两个循环**本来就认它**
	// 并且会把 Notice 显示出来（internal/node/node.go 那两处）⇒ 不用改客户端 ✓
	// ⚠️ 要在 Close **之前**发：发完再关，数据已经进了发送缓冲，能送到 ✓
	_ = host.cl.Send(proto.Msg{Type: proto.TypeHostGone, Notice: "管理员关闭了这个房间"})

	// ⚠️ 关连接要在**锁外**做：Close 会唤醒房主那条 Recv，
	// 它一起来就要去拿 r.mu（清理那段）—— 拿着锁去 Close 等于自己等自己 ✗
	//
	// 🩸 用 **CloseWrite（半关）**而不是 Close：房主一直在发候选，
	// 我们这边还有没读完的数据，这时整关会让内核发 RST ⇒ 上面那句通知被丢掉 ✗
	// （实测：整关时房主只看到"连接断开：EOF"，看不到"管理员关闭了"）
	if err := host.cl.CloseWrite(); err != nil {
		return fmt.Errorf("关不掉房主连接: %w", err)
	}
	log.Printf("后台关闭房间 %s（只断了房主连接，收尾走正常那条路）", roomID)
	return nil
}

// setRoomPass 改房间密码。
//
// 注意：已经在房间里的朋友不受影响 —— 他们的隧道密钥早在握手时就定下了。
// 新密码只管「之后再来的人」。
func setRoomPass(r *room, pass string) {
	// 服务端自己也要卡长度：房主端虽然拦了，但那是给用户看的，拦不住改过的客户端。
	// 密码会被存进房间对象（而且会转发给后来的人），不卡就是一条撑内存的路子。
	if !validRoomPass(pass) {
		log.Printf("房间 %s：改密码被拒（超过 %d 位）", r.code, maxPassLen)
		return
	}
	r.mu.Lock()
	r.pass = pass
	r.mu.Unlock()
	if pass == "" {
		log.Printf("房间 %s：房主取消了房间密码", r.code)
	} else {
		log.Printf("房间 %s：房主更新了房间密码（%d 位）", r.code, len([]rune(pass)))
	}
}

// unban 把某人移出黑名单（他的设备 ID 与 IP 两条记录一起清）。
func unban(r *room, key string) {
	r.mu.Lock()
	b, ok := r.bans[key]
	if ok {
		delete(r.bans, key)
	}
	r.pushBansLocked()
	r.mu.Unlock()
	if !ok {
		return
	}
	log.Printf("房间 %s：房主把 %s 移出了黑名单", r.code, b.label)
}

// forwardHostCands 把房主的候选地址转给指定的那位朋友。
func forwardHostCands(r *room, targetID string, cands []string, gamePort2 int) {
	r.mu.Lock()
	var target *member
	for _, j := range r.joins {
		if j.id == targetID {
			target = j
			break
		}
	}
	n := len(r.joins)
	r.mu.Unlock()

	if target == nil {
		// 🩸 targetID 是房主给的、没清洗过的字符串 —— 直接打日志的话，
		// 塞个换行就能在日志里**伪造出一行假的**出来 ✗
		// （房间名/昵称/游戏名早就清洗了，这是漏掉的一处）
		log.Printf("房间 %s：收到房主候选，但找不到目标 %s（当前 %d 人）",
			r.code, sanitizeLabel(targetID), n)
		return
	}
	// gamePort2 原样转给加入方：0 = 房主只有一路 ✓
	if err := target.cl.Send(proto.Msg{Type: proto.TypePeer, Peer: cands, GamePort2: gamePort2}); err != nil {
		log.Printf("房间 %s：转发房主候选给 %s 失败: %v", r.code, dispName(target.label, target.seat), err)
		return
	}
	log.Printf("房间 %s：已把房主候选转发给 %s", r.code, dispName(target.label, target.seat))
}

// removeMember 把某人移出房间。
// notify 是发给对方的类型（被踢 / 被拉黑），拉黑时还会连同 IP 一起记进黑名单。
// 返回被移出的那个人（没找到就是 nil）—— 后台踢人要拿它的 cl 去**真的关连接** ✓
// removeMember 把一个人摘出房间（踢 / 拉黑 / 管理员移出）。
//
// reason 是给**历史记录**用的机器可读原因（kicked / banned / admin），
// verb 是给人看的（"踢出"），两者别混用 —— 后台要按原因筛，中文句子里没法筛 ✗
func removeMember(r *room, targetID, notify, notice, verb, reason string) *member {
	r.mu.Lock()
	idx := -1
	for i, j := range r.joins {
		if j.id == targetID {
			idx = i
			break
		}
	}
	if idx < 0 {
		r.mu.Unlock()
		return nil
	}
	target := r.joins[idx]
	r.joins = append(r.joins[:idx], r.joins[idx+1:]...)

	banned := notify == proto.TypeBanned
	if banned {
		// 记的键是 cid（纯 clientID），不是 id（带序号）——
		// 加入那边也是拿纯 clientID 来对，两边必须用同一个东西，
		// 否则拉黑写了、查的时候对不上，人照样能进来。
		r.bans[target.cid] = &ban{cid: target.cid, ip: target.remoteIP, label: banLabel(target)}
	}
	r.broadcastMembersLocked()
	if banned {
		r.pushBansLocked()
	}
	r.mu.Unlock()

	histRecord(histEvent{Kind: histLeave, Room: r.code, CID: target.cid, Why: reason})

	target.cl.Send(proto.Msg{Type: notify, Notice: notice})
	// 这儿用的是 member.remoteIP（**原始**地址，黑名单靠它记人）——
	// 所以要单独套一次打码，别让完整地址进日志 ✗（别处走 main.go 入口那次打码）
	log.Printf("房间 %s：%s 被%s（%s）", r.code, dispName(target.label, target.seat), verb,
		netutil.MaskAddr(target.remoteIP))
	return target
}

// notifyHostLeft 告诉房主：某位朋友走了，以及是**怎么走的**（主动退出 / 非正常退出）。
//
// 🩸 必须在 broadcastMembersLocked **之后**发：那时这位朋友已经从名单里摘掉了，
// 房主没法再去名单里查他是谁 —— 所以名字得跟着这条消息一起来（Label + Seat）。
func notifyHostLeft(r *room, j *member, left bool) {
	r.mu.Lock()
	host := r.host
	r.mu.Unlock()
	if host == nil {
		return
	}
	host.cl.Send(proto.Msg{Type: proto.TypeLeave, TargetID: j.id, Left: left, Label: j.label, Seat: j.seat})
}

// dispName 日志和黑名单里给人看的名字：「房主」「玩家 阿强」「玩家 1」。
//
// 判房主靠「座位号 == 0」—— 0 是房主专用，朋友从 1 起发（见 nextSeatLocked）。
// 规则本体在 proto.DisplayName，界面那边自己拼（它要分色，不能拿整串）。
func dispName(label string, seat int) string {
	return proto.DisplayName(label, seat, seat == 0)
}

// banLabel 黑名单里显示的名字：起了名就只用名字，没起名才退回「玩家 N」——
// 黑名单是一串"人"，没必要每个都挂个角色前缀。
func banLabel(m *member) string {
	if m.label != "" {
		return m.label
	}
	return dispName(m.label, m.seat)
}

// cleanLabel 收拾客户端给的昵称：去空白、超长截断。
// 空串是正常值 —— 表示这人没起名，界面会用座位号拼「玩家 N」。
func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) > maxLabelLen {
		rs = rs[:maxLabelLen]
	}
	return string(rs)
}

// sanitizeLabel 在 cleanLabel 的基础上再卡一道：**只放行词库能拼出来的名字**
// （词库在 internal/nick，客户端摇名字用的也是同一份，见那儿的包注释）。
//
// 🩸 为什么服务端也得管这件事：昵称是**客户端生成的字符串**，服务器拿到之后
// 会存进房间、广播给房里所有人、还写进自己的日志 —— 等于把"服务器主人"的名字
// 挂在上面。前端撤掉输入框只能挡住普通人（改一下本机 settings.json 就能绕）✗，
// 所以这道必须落在服务器上 ✓
//
// 📌 做法是"清洗"不是"拒绝"：不认识的名字当成没起名（空串），界面照旧走
// 「玩家 N」兜底。拒绝会误伤 —— 服务器词库一旦落后于客户端（新版客户端加了新词），
// 正常用户就被一个看不懂的报错挡在门外，而且他自己完全不知道为什么 ✗
//
// ⚠️ 清洗掉的名字**绝不能写进日志** —— 那正是要避免的东西。调用方只记"有一个"。
func sanitizeLabel(s string) string {
	clean := cleanLabel(s)
	if clean == "" || nick.Valid(clean) {
		return clean
	}
	return ""
}

// shortID 取 clientID 前 6 位，给界面在昵称重名时做区分用。
func shortID(cid string) string {
	if len(cid) <= 6 {
		return cid
	}
	return cid[:6]
}

// sessRecheckEvery 是"房主会话复查"的间隔。
//
// 📌 30 秒是拍的：管理员点了"强制下线"之后，最迟半分钟那条连接就断 ✓
//
//	再短没必要（每房每 30 秒查一次账号表，开销可忽略但不是零）✓
const sessRecheckEvery = 30 * time.Second

// recheckHostSession 周期复查房主的登录令牌还作不作数。
//
// 🩸 复查的就是**令牌**（VerifyToken）—— 它一次覆盖了三种失效：
// 被强制下线 / 令牌过期 / 账号被停用 ✓ 不用分别去查 ✓
//
// 失效就 `cl.Close()`：房主那边会看到连接断开 ⇒ 走**原有收尾路径**
// （通知房里的人、删掉房间）✓ 不另写一套清理 ✗
//
// 📌 **为什么不用 `Recv(30s)` 那种超时来做周期检查**：
//
//	signal.Client 里面是 `bufio.Scanner` —— 它一旦报 timeout 就**永久失效**，
//	后面再也读不出东西 ✗ 那等于每隔 30 秒自己把连接掐死一次 ✗
//	（Scannel 出错后 Err() 一直非 nil，后续 Scan 直接返回 false）
//
// 反证方法：把这个函数体改成直接 return ⇒ TestHostSessionRecheck*
// 那几条关于"失效会调 Close"的断言立刻红 ✓
func recheckHostSession(cl *signal.Client, room, token string, stop <-chan struct{}, every time.Duration) {
	if token == "" {
		return // 本地免验证跑起来的（没有令牌）⇒ 没什么可复查的 ✓
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if !hostSessionUsable(token) {
				log.Printf("房间 %s：房主的登录已失效（被下线 / 过期 / 停用），断开这条连接", room)
				// 🩸 **先说一句再断** —— 端到端实测过：只 Close 的话，
				// 房主那边看到的是「与穿透服务器的连接断了，检查一下网络再重试」，
				// 那是**误导**（他的网络没问题，是被下线了）✗
				// 给一句话，他才知道该去重新登录 ✓
				cl.Send(proto.Msg{Type: proto.TypeError,
					Err: "你的登录已失效（被下线 / 过期 / 停用），请重新登录再开房"})
				// 半关（发 FIN）—— 跟"关房间"一个道理：直接 Close 会 RST，
				// 把刚写进去的那句话冲掉 ✗ 对面读到 EOF 就会自己收尾 ✓
				_ = cl.CloseWrite()
				// 但半关只保证"话说到了"，读的那半边还开着 ⇒ 兜一个真关，
				// 免得遇到不理会 EOF 的客户端时连接一直挂着 ✓
				time.AfterFunc(2*time.Second, func() { _ = cl.Close() })
				return
			}
		}
	}
}

// hostSessionUsable 查一次"这个令牌现在还作不作数"。
//
// 📌 抽出来是为了**能单测** —— 直接测那个循环的话得等一个 tick ✗
// 空令牌 = 本地免验证跑起来的（没有账号）⇒ 永远算"可用" ✓
func hostSessionUsable(token string) bool {
	if token == "" {
		return true
	}
	if st == nil {
		// 没配账号库（本地免验证）⇒ 没有会话可查，当可用 ✓
		// 🩸 不能因为"我们这边没配置"就把房主踢掉 ✗
		return true
	}
	_, ok := st.VerifyToken(token, time.Now())
	return ok
}
