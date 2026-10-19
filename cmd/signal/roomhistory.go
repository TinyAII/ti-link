// 房间历史：谁开过房、谁什么时候进来、谁什么时候走的。
//
// 三条口径（老板 2026-10-17 定，别自己改）：
//  1. 记到**成员进出**这一级（不只是房间级）
//  2. 服务器上**只留 30 天**
//  3. 后台要能列表 / 搜索 / 导出
//
// 🩸🩸 为什么这个功能必须在**信令服务**里做，不能放客户端：
// 房间是这里的**内存态**（rooms 表），面板只能通过 local.sock 问这边；
// 而玩家本机上的文件服务器根本碰不到 ✗
// （2026-10-17 我第一次做错了地方，做进了客户端，被老板当场叫停）
//
// ⚠️ 这是**留存**，不是实时元数据。客户端「关于」页和使用说明里那句
// 「服务器能看到：你的邮箱、账号状态，以及某个房间正开着、有几个人」
// ——"正开着"三个字意味着不留存，加了这份记录之后那句话就不准了，
// 必须跟着改（提醒过老板了）✓
//
// 🔒 存什么、不存什么：
//
//	· 记 **账号 ID（int64）**，**不记明文邮箱** —— 邮箱在账号库里，面板本来就有
//	  ID→邮箱 的映射（api_admin.go 里那个 byID），要显示时再解析即可 ✓
//	· 不记 IP、不记房间密码、不记任何游戏内容（游戏数据压根不经过服务器）✓
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// histRetention 是保留窗口。30 天是老板定的：够查"上周那个房间怎么回事"，
	// 又不至于把磁盘吃满（这台机器上还跑着别的项目）✓
	histRetention = 30 * 24 * time.Hour

	// histMaxEvents 是个**兜底阀门**，不是常规约束。
	// 正常情况下 30 天也就几千到几万条；真顶到这个数说明出了异常（刷房之类），
	// 那时宁可丢最旧的，也别让内存无限长 ✗
	histMaxEvents = 200000

	// histFile 放在数据目录里（跟账号库、local.sock 一起）
	histFile = "room-history.jsonl"

	// 事件类型
	histOpen  = "open"
	histClose = "close"
	histJoin  = "join"
	histLeave = "leave"

	// histPruneEvery 两次清理之间至少隔这么久（记录时顺手检查，不起定时器）
	histPruneEvery = 6 * time.Hour
)

// histEvent 是流水账里的一行。字段名故意都短（这个文件会存几千行）。
type histEvent struct {
	T     int64  `json:"t"` // Unix 秒
	Kind  string `json:"k"` // open / close / join / leave
	Room  string `json:"r"`
	Game  string `json:"g,omitempty"`
	Acct  int64  `json:"a,omitempty"` // 账号 ID：open=房主，join=进来的人
	CID   string `json:"c,omitempty"` // 成员的客户端持久 ID（拉黑认的那个）
	Label string `json:"l,omitempty"` // 昵称快照
	Seat  int    `json:"s,omitempty"`
	Host  bool   `json:"h,omitempty"`
	Why   string `json:"w,omitempty"` // leave 的原因：left / kicked / banned / gone
}

// histMember 是聚合出来的"某人在某次房间里的这一段在线"。
// 📌 走了又回来会有**两条**（LeftAt 各自不同）—— 那样才看得出中间断过 ✓
type histMember struct {
	// Acct 是这个人**登录的账号 ID**（0 = 没记到，多半是没登录/老记录）。
	// 🩸 昵称随时能改，只有账号才是身份 —— 后台查"这个人来过没"要靠它 ✓
	Acct     int64  `json:"acct,omitempty"`
	CID      string `json:"cid"`
	Label    string `json:"label"`
	Seat     int    `json:"seat"`
	Host     bool   `json:"host"`
	JoinedAt int64  `json:"joinedAt"`
	LeftAt   int64  `json:"leftAt,omitempty"`
	Why      string `json:"why,omitempty"` // left / kicked / banned / gone
}

// histRoom 是聚合出来的一个"房间"，后台直接渲染它。
type histRoom struct {
	Code      string `json:"code"`
	Game      string `json:"game"`
	Acct      int64  `json:"acct"` // 房主账号 ID（面板自己映射成邮箱）
	OpenedAt  int64  `json:"openedAt"`
	ClosedAt  int64  `json:"closedAt,omitempty"` // 0 = 还开着 / 没正常收尾
	MaxOnline int    `json:"maxOnline"`
	// Open 表示"这一局现在还开着"（信令的 rooms 表里还有它）。
	// 🩸 只靠 ClosedAt==0 区分不了两种情况：**正在开** 和 **上次没正常收尾** ✗
	// 前者成员该显示"还在房间里"，后者该显示"没记到离开时间"，两句话不能混 ✓
	Open bool `json:"open,omitempty"`
	// LastAt 是这一局**最后一条记录**的时刻（open/join/leave/close 里最晚的那个）。
	//
	// 🩸 给"没有结束时间"的房间用（进程被杀、服务器重启 ⇒ close 事件根本没写）。
	// 这种房间的时长**不能拿"现在"去减开始时间** —— 那样每秒都在涨，
	// 看着像房间还开着 ✗（老板 2026-10-17：「时长 35 分钟（未正常结束）」而且一直涨）
	LastAt  int64        `json:"lastAt,omitempty"`
	Members []histMember `json:"members"`
}

// histRoomKey 定位"房间号的某一次开启"（同一个号可以被重复使用）
type histRoomKey struct {
	room string
	t    int64
}

type roomHistory struct {
	mu        sync.Mutex
	path      string
	events    []histEvent
	lastPrune time.Time
}

var (
	histMu sync.Mutex
	hist   *roomHistory
)

// histInit 打开（或新建）历史文件，并顺手把超过 30 天的删掉。
//
// path 为空 ⇒ 只记内存、不落盘（本地调试/测试用）。
// 📌 文件坏了**不清空**：改名成 .bad 留着，从空的开始 ——
// 这份记录是排查用的证据，不能一声不响地扔掉 ✓
func histInit(dir string) {
	h := &roomHistory{}
	if dir != "" {
		h.path = dir + string(os.PathSeparator) + histFile
		h.load()
	}
	h.mu.Lock()
	h.pruneLocked(time.Now())
	h.mu.Unlock()

	histMu.Lock()
	hist = h
	histMu.Unlock()
}

// histRecord 记一条。任何失败都**静默** —— 记不下历史不该影响联机 ✓
func histRecord(ev histEvent) {
	histMu.Lock()
	h := hist
	histMu.Unlock()
	if h == nil {
		return
	}
	ev.T = time.Now().Unix()

	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.events) >= histMaxEvents {
		// 超了就从最旧的砍掉 10%，一次砍够，免得每来一条都砍
		cut := len(h.events) / 10
		h.events = append(h.events[:0], h.events[cut:]...)
	}
	h.events = append(h.events, ev)
	h.appendLocked(ev)

	// 清理不是每条都做（那是 O(n) 重写文件），隔 6 小时顺手看一眼
	if time.Since(h.lastPrune) > histPruneEvery {
		h.pruneLocked(time.Now())
	}
}

// histRooms 把流水账拼成"房间"给后台看。最近的在前。
//
// 📌 聚合放在查询时做，而不是维护一份房间表：
// 房间 ID 可以被重复使用（关了再开同一个号），只有"开着的那一次"才唯一，
// 维护增量房间表很容易在"同一个号开第二次"上出错 ✗
// 每次查询扫一遍事件（最多 20 万条）对后台来说完全够快 ✓
func histRooms(q string, since int64) []histRoom {
	histMu.Lock()
	h := hist
	histMu.Unlock()
	if h == nil {
		return nil
	}

	q = strings.ToLower(strings.TrimSpace(q))

	h.mu.Lock()
	events := append([]histEvent(nil), h.events...)
	h.mu.Unlock()

	order := make([]histRoomKey, 0, 64)
	byKey := map[histRoomKey]*histRoom{}

	for _, ev := range events {
		if since > 0 && ev.T < since {
			continue
		}
		switch ev.Kind {
		case histOpen:
			k := histRoomKey{ev.Room, ev.T}
			order = append(order, k)
			byKey[k] = &histRoom{Code: ev.Room, Game: ev.Game, Acct: ev.Acct,
				OpenedAt: ev.T, LastAt: ev.T}
		case histClose, histJoin, histLeave:
			k := findOpen(order, byKey, ev.Room)
			if k == nil {
				// 找不到对应的 open（历史被裁过 / 服务重启时房间已经开着）⇒ 丢掉这条
				continue
			}
			r := byKey[*k]
			if ev.T > r.LastAt {
				r.LastAt = ev.T
			}
			switch ev.Kind {
			case histClose:
				r.ClosedAt = ev.T
			case histJoin:
				r.Members = append(r.Members, histMember{
					Acct: ev.Acct, CID: ev.CID, Label: ev.Label, Seat: ev.Seat,
					Host: ev.Host, JoinedAt: ev.T,
				})
				if n := onlineCount(r); n > r.MaxOnline {
					r.MaxOnline = n
				}
			case histLeave:
				if m := lastOnline(r, ev.CID); m != nil {
					m.LeftAt = ev.T
					m.Why = ev.Why
				}
			}
		}
	}

	out := make([]histRoom, 0, len(order))
	for i := len(order) - 1; i >= 0; i-- { // 最近的在前
		r := byKey[order[i]]
		out = append(out, *r)
	}
	if q == "" {
		return out
	}
	filtered := out[:0]
	for _, r := range out {
		if strings.Contains(strings.ToLower(r.Code), q) ||
			strings.Contains(strings.ToLower(r.Game), q) {
			filtered = append(filtered, r)
			continue
		}
		for _, m := range r.Members {
			if strings.Contains(strings.ToLower(m.Label), q) {
				filtered = append(filtered, r)
				break
			}
		}
	}
	return filtered
}

/* ---------- 内部 ---------- */

// findOpen 找这个房间号**当前开着的那一次**。
// 从后往前找：同一个号开过好几次时，要归到最近那次 ✓
func findOpen(order []histRoomKey, byKey map[histRoomKey]*histRoom, room string) *histRoomKey {
	for i := len(order) - 1; i >= 0; i-- {
		if order[i].room == room {
			if byKey[order[i]].ClosedAt == 0 {
				k := order[i]
				return &k
			}
			return nil // 最近那次已经关了 ⇒ 这条事件没有归属
		}
	}
	return nil
}

func onlineCount(r *histRoom) int {
	n := 0
	for i := range r.Members {
		if r.Members[i].LeftAt == 0 {
			n++
		}
	}
	return n
}

// lastOnline 找这个人**还没走的那条**记录（走了又回来会有多条）
func lastOnline(r *histRoom, cid string) *histMember {
	for i := len(r.Members) - 1; i >= 0; i-- {
		if r.Members[i].CID == cid && r.Members[i].LeftAt == 0 {
			return &r.Members[i]
		}
	}
	return nil
}

func (h *roomHistory) load() {
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	bad := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev histEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			bad++ // 半行 / 断电留下的残行：跳过就好，不影响其它行 ✓
			continue
		}
		h.events = append(h.events, ev)
	}
	sort.SliceStable(h.events, func(i, j int) bool { return h.events[i].T < h.events[j].T })
	if bad > 0 {
		// 只在日志里说一句，不刷屏
		log.Printf("房间历史：跳过 %d 行解析不了的记录（多半是上次写盘被中断）", bad)
	}
}

// appendLocked 追加一行。
// 用 O_APPEND 单次 Write —— 一行一条，崩了最多坏最后一行，前面的都还在 ✓
// （比"整个数组重新序列化"安全得多：那种写法写一半就全没了）
func (h *roomHistory) appendLocked(ev histEvent) {
	if h.path == "" {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b = append(b, '\n')
	f, err := os.OpenFile(h.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(b)
}

// pruneLocked 丢掉超过 30 天的，并把文件重写成剩下的部分。
//
// 📌 重写走"临时文件 + 改名"：直接截断重写的话，中途崩了整份历史就没了 ✗
func (h *roomHistory) pruneLocked(now time.Time) {
	h.lastPrune = now
	cutoff := now.Add(-histRetention).Unix()

	keep := h.events[:0]
	for _, ev := range h.events {
		if ev.T >= cutoff {
			keep = append(keep, ev)
		}
	}
	dropped := len(h.events) - len(keep)
	h.events = keep
	if dropped == 0 || h.path == "" {
		return
	}
	h.rewriteLocked()
	log.Printf("房间历史：清掉 %d 条超过 %d 天的记录", dropped, int(histRetention.Hours()/24))
}

func (h *roomHistory) rewriteLocked() {
	tmp := h.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	for _, ev := range h.events {
		b, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	w.Flush()
	f.Sync()
	f.Close()
	os.Rename(tmp, h.path)
}

/* ---------- 给后台的端点 ---------- */

// historyHandler 是后台「历史房间」页的数据源（GET，挂在本机 socket 上）。
//
//	?q=关键词        房间号 / 游戏 / 成员昵称（大小写不敏感）
//	?days=N          看最近几天，默认 30、最大 30（再往上也没有，落盘就只留 30 天）
//
// 形状包一层 {"rooms":[...]}，跟 /rooms 一致 —— 以后要加"总数"之类不用改形状 ✓
// 📌 没数据时返回**空数组**不是 null：前端少一个判空分支 ✓
func historyHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeLocalJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只支持 GET"})
		return
	}
	q := r.URL.Query().Get("q")

	days := int(histRetention.Hours() / 24)
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < days {
			days = n
		}
	}
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour).Unix()

	// 🩸 变量名别叫 rooms —— 那个名字是**全局的房间表**（map[string]*room）。
	// 叫 rooms 的话会把它遮住，于是下面"遍历 rooms"遍历到的是**历史结果自己**，
	// 每个房间都被标成"还开着" ⇒ 页面把已经关掉的房间显示成"还在房间里" ✗
	// （2026-10-17 上线后从真实数据里发现的：/rooms 是空的，/history 却说 open:true）
	list := histRooms(q, since)

	// 把"现在还在的房间号"标上去 —— 只有信令这边知道房间表，
	// 聚合函数本身不该去碰它（那样单测就得造整个房间表）✓
	roomsMu.Lock()
	live := make(map[string]bool, len(rooms))
	for code := range rooms {
		live[code] = true
	}
	roomsMu.Unlock()
	for i := range list {
		if list[i].ClosedAt == 0 && live[list[i].Code] {
			list[i].Open = true
		}
	}

	out := list
	if out == nil {
		out = []histRoom{}
	}
	writeLocalJSON(w, http.StatusOK, map[string]any{"rooms": out, "days": days})
}
