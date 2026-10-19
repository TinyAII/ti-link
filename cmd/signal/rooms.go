// 当前房间的快照 —— 给管理面板那一页用。
//
// 🩸 这里是**唯一**会把"房间里的事"亮出来的地方，所以每条信息的边界都要写清楚：
//
//	来源地址   → **打码**（120.227.*.*）。够看出"这几个房间是不是同一个人开的"，
//	             但收不到具体是谁 —— 跟日志打码是同一件事、同一个理由 ✓
//	房主昵称   → 本来就是房间里公开的（同一局的人都看得到），不算新暴露 ✓
//	账号号     → 只给服务器主人看，他要靠它追责 ✓
//
// 📌 **不落盘、不进日志**：房间本来就是内存态，这份快照也是每次现算的 ✓
// 后台页面刷新一次算一次，服务器这边不留痕迹。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"p2plink/internal/netutil"
	"p2plink/internal/proto"
)

type roomInfo struct {
	Room        string `json:"room"`
	Members     int    `json:"members"`
	MaxSize     int    `json:"maxSize"`
	Host        string `json:"host"` // 「房主 某某」/「房主」（没起名时）
	OpenSeconds int    `json:"openSeconds"`
	From        string `json:"from"`      // **打码**的来源地址
	AccountID   int64  `json:"accountId"` // 房主登录的账号号；0 = 没有账号（本地 -insecure-no-auth 跑的）

	// Game 是**房主上报**的游戏名（清洗过）。空 = 没上报（老客户端）✓
	//
	// 🩸 展示字段，**不是防线** —— 客户端改一下就能报别的（见 proto.Msg.Game）✗
	// 界面上要按"客户端自报"对待，别拿它做任何拦截判断 ✓
	Game string `json:"game"`
}

// memberInfo / roomDetail 是**后台详情页**用的：比列表多得多 ——
// 成员一个一个列出来（座位 / 昵称 / 来源 / 加入多久 / 信令流量）✓
//
// 🩸 这里**不给**成员的设备 ID 和公钥：后台不需要它们，
//
//	给了只是多一处泄露面。要认人用座位号就够 ✓
type memberInfo struct {
	Seat        int    `json:"seat"`
	Label       string `json:"label"` // 纯名字；空 = 没起名（界面用座位拼「玩家 N」）
	Host        bool   `json:"host"`
	Avatar      int    `json:"avatar"`
	From        string `json:"from"` // **打码**的来源地址（跟列表一个口径）
	Game        string `json:"game"` // 这位上报的游戏（可能跟房主不一样，界面据此提示）
	JoinSeconds int    `json:"joinSeconds"`
	RxBytes     int64  `json:"rxBytes"` // 跟服务器的**信令**流量（不是游戏数据）
	TxBytes     int64  `json:"txBytes"`
}

type roomDetail struct {
	Room        string       `json:"room"`
	Game        string       `json:"game"`
	Members     []memberInfo `json:"members"`
	MaxSize     int          `json:"maxSize"`
	HasPass     bool         `json:"hasPass"` // 有没有设房间密码。**绝不回密码本身** ✗
	OpenSeconds int          `json:"openSeconds"`
	From        string       `json:"from"`
	AccountID   int64        `json:"accountId"`
	BanCount    int          `json:"banCount"` // 黑名单条数（名单本身不往这儿摊）
}

// roomsSnapshot 把当前房间摊成一份快照。
//
// 锁的顺序：先 roomsMu 把房间列表拷出来、**放手**，再逐个拿 r.mu。
// 🩸 别写成"攥着 roomsMu 去拿 r.mu" —— 两把锁嵌套就有死锁的可能，
// 而这个文件本来完全可以不嵌套 ✓
func roomsSnapshot(now time.Time) []roomInfo {
	roomsMu.Lock()
	list := make([]*room, 0, len(rooms))
	for _, r := range rooms {
		list = append(list, r)
	}
	roomsMu.Unlock()
	return roomsSnapshotOf(list, now)
}

// roomsSnapshotOf 是"给一张房间列表，算出快照"的**纯函数**。
//
// 📌 特意跟读全局表那步分开：这样测试可以直接喂一份手搓的房间进去，
// 不用往全局 rooms 里塞东西、再想办法擦干净 ✓
// （往全局塞的测试有个坏毛病：它跟别的测试抢同一份状态，一并行就互相干扰 ✗）
func roomsSnapshotOf(list []*room, now time.Time) []roomInfo {
	out := make([]roomInfo, 0, len(list))
	for _, r := range list {
		r.mu.Lock()
		info := roomInfo{
			Room:      r.code,
			MaxSize:   r.maxSize,
			From:      netutil.MaskAddr(r.hostIP),
			AccountID: r.accountID,
		}
		if !r.openedAt.IsZero() {
			sec := int(now.Sub(r.openedAt).Seconds())
			if sec < 0 {
				sec = 0
			}
			info.OpenSeconds = sec
		}
		if r.host != nil {
			info.Host = dispName(r.host.label, r.host.seat)
			info.Members = 1 + len(r.joins)
			info.Game = r.host.game
		} else {
			info.Members = len(r.joins)
		}
		r.mu.Unlock()

		if info.Room == "" {
			continue
		}
		out = append(out, info)
	}
	// 按"开了多久"从小到大 ⇒ 新开的排前面（后台看到的第一眼是刚发生的事）
	sort.Slice(out, func(i, j int) bool {
		if out[i].OpenSeconds != out[j].OpenSeconds {
			return out[i].OpenSeconds < out[j].OpenSeconds
		}
		return out[i].Room < out[j].Room
	})
	return out
}

type roomsSnapshotResp struct {
	Rooms []roomInfo `json:"rooms"`
}

func roomsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// 形状特意包一层 {"rooms": [...]}：以后要加"总数""开启时间"之类的字段
	// 就不用改形状了（管理面板那边读的是 .rooms）✓
	_ = enc.Encode(roomsSnapshotResp{Rooms: roomsSnapshot(time.Now())})
}

// ── 房间详情（后台"点进去"那一页）──────────────────────────

// roomDetailOf 算出一个房间的详情。
//
// 📌 纯函数（喂一个 *room）+ 内部自己加锁：测试可以直接手搓一个房间喂进来，
// 不用往全局表里塞东西再擦干净 ✓（跟 roomsSnapshotOf 一个路子）
func roomDetailOf(r *room, now time.Time) roomDetail {
	r.mu.Lock()
	defer r.mu.Unlock()

	d := roomDetail{
		Room:      r.code,
		MaxSize:   r.maxSize,
		HasPass:   r.pass != "",
		From:      netutil.MaskAddr(r.hostIP),
		AccountID: r.accountID,
		BanCount:  len(r.bans),
	}
	if !r.openedAt.IsZero() {
		sec := int(now.Sub(r.openedAt).Seconds())
		if sec < 0 {
			sec = 0
		}
		d.OpenSeconds = sec
	}

	add := func(m *member, isHost bool) {
		if m == nil {
			return
		}
		mi := memberInfo{
			Seat:   m.seat,
			Label:  m.label,
			Host:   isHost,
			Avatar: m.avatar,
			From:   netutil.MaskAddr(m.remoteIP),
			Game:   m.game,
		}
		if !m.joinedAt.IsZero() {
			sec := int(now.Sub(m.joinedAt).Seconds())
			if sec < 0 {
				sec = 0
			}
			mi.JoinSeconds = sec
		}
		mi.RxBytes, mi.TxBytes = m.counting.connBytes()
		d.Members = append(d.Members, mi)
	}
	add(r.host, true)
	for _, j := range r.joins {
		add(j, false)
	}
	if r.host != nil {
		d.Game = r.host.game
	}
	return d
}

// ── 管理员踢人 ─────────────────────────────────────────────

// kickMember 从后台把一个成员请出房间。
//
// 🩸 走的是**房主踢人那条一样的路**（removeMember：摘名单 / 广播新名单 / 通知本人）——
// 然后**多补一件事：真的把这条连接关掉** ✓
//
// 为什么非要补：房主踢人那条只发一条「你被踢了」的通知、指望客户端自己退 ✗
// 那对**改过的客户端**不成立（他可以当没看见）。管理员这层是服务器侧的动作，
// 得让它**真的断开** —— 这也是"唯一算数的是服务器侧判定"那条的落地 ✓
//
// seat 是界面上的座位号（房主 0、朋友从 1 起）。
// ⚠️ seat 0 **不许踢**：要停就关掉整个房间（那是另一个操作）✓
func kickMember(code string, seat int) error {
	roomsMu.Lock()
	r := rooms[code]
	roomsMu.Unlock()
	if r == nil {
		return fmt.Errorf("房间 %s 不在了（可能刚关掉）", code)
	}
	if seat == 0 {
		return fmt.Errorf("房主不能单独踢出 —— 要停就关掉整个房间")
	}

	r.mu.Lock()
	targetID := ""
	for _, j := range r.joins {
		if j.seat == seat {
			targetID = j.id
			break
		}
	}
	r.mu.Unlock()
	if targetID == "" {
		return fmt.Errorf("房间 %s 里没有 %d 号座位（可能刚退出）", code, seat)
	}

	m := removeMember(r, targetID, proto.TypeKicked, "管理员将你移出了这个房间", "管理员移出", "admin")
	if m == nil {
		return fmt.Errorf("那个人刚退出了")
	}
	// 真正断开（见上面那段：不能只发通知就算完）✓
	_ = m.cl.CloseWrite()
	return nil
}

// gameRequiredHint 是"没说明要玩哪款游戏"时给玩家的话 —— 得能照着做 ✓
const gameRequiredHint = "请更新到最新版客户端，并在界面上选好你要玩的游戏再来。"

// refuseHostGame 建房时用：**没说开哪款游戏就不许开房**。
//
// 🩸 为什么连建房也管：房间没有游戏名的话，别人就无从判断"我该不该进" ⇒
// 那等于给所有人开了一条"随便进"的路 ✗ 所以源头就得堵 ✓
func refuseHostGame(game string) string {
	if game == "" {
		return "没有说明要开哪款游戏 —— " + gameRequiredHint
	}
	return ""
}

// refuseJoinGame 加入时用：**两边都报了、而且必须一样**才放行。
//
// 🩸 这道拦的依据是**客户端自报的游戏名** ⇒ 挡的是"手滑选错游戏"，不是恶意 ✗
//
//	真正基于**事实**的那道在客户端：internal/node 的 sniffGame（看握手数据）✓
//
// 📌 三种情况各有各的人话（都得让玩家知道下一步做什么）：
//
//	· 自己没说 ⇒ 更新客户端、选好游戏
//	· 房主没说 ⇒ 让房主用最新版重开（房主没说的情况现在建不出房，这是**防御性**的一支）
//	· 说了但不一样 ⇒ 切成对应的游戏
//
// 反证方法：给第一条加个 `|| joinerGame == hostGame` 之类的"放行"分支 ⇒
// TestGameRefuseWhenNotReported 立刻红 ✓
func refuseJoinGame(hostGame, joinerGame string) string {
	if joinerGame == "" {
		return "你没有说明在玩哪款游戏 —— " + gameRequiredHint
	}
	if hostGame == "" {
		return "这个房间没有说明在玩哪款游戏（多半是旧版客户端开的）—— 让房主用最新版重开一个。"
	}
	if hostGame != joinerGame {
		// 📌 文案是老板定的：**从用户动作出发**（"你输入的房间ID…"），
		// 而且**不说"你报的是什么"** —— 他自己选的游戏，他自己知道，
		// 重复一遍只是啰嗦 ✗
		return "你输入的房间ID是「" + hostGame + "」的房间，请切到对应的游戏再加入。"
	}
	return ""
}
