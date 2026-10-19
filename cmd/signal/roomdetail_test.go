// 房间详情 / 踢人 / 游戏名清洗的测试。
//
// 🩸 这个文件里最要紧的两条：
//
//	① 详情里**绝不能出现房间密码本身**（只回"有没有设"）——
//	   面板上没有它的用途，多回一次只是多一处泄露面 ✗
//	② 踢人要**真的把连接关掉** —— 房主踢人那条只是发一条通知、
//	   指望客户端自己退；那对改过的客户端不成立（他可以当没看见）✗
//	   管理员这层是服务器侧的动作，必须真断 ✓
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"p2plink/internal/auth"
	"p2plink/internal/proto"
	"p2plink/internal/signal"
)

// ── 详情 ───────────────────────────────────────────────────

func TestRoomDetailOfShape(t *testing.T) {
	r := mkRoom("T-DET001", "120.227.45.9", 4, "小明", 7, 90*time.Second)
	r.pass = "秘密口令"
	r.host.game = "我的世界"
	r.host.joinedAt = roomsNow.Add(-90 * time.Second)
	r.host.remoteIP = "120.227.45.9"
	r.joins = []*member{{
		id: "j1", cid: "j1", label: "小红", seat: 1,
		remoteIP: "39.144.10.7", game: "泰拉瑞亚",
		joinedAt: roomsNow.Add(-30 * time.Second),
	}}
	r.bans["someone"] = &ban{cid: "someone"}

	d := roomDetailOf(r, roomsNow)

	if d.Room != "T-DET001" || d.Game != "我的世界" {
		t.Errorf("房间号/游戏不对：%q / %q", d.Room, d.Game)
	}
	if !d.HasPass {
		t.Error("设了密码，HasPass 该是 true ✗")
	}
	if d.BanCount != 1 {
		t.Errorf("黑名单条数该是 1，实得 %d", d.BanCount)
	}
	if d.OpenSeconds != 90 {
		t.Errorf("开了多久该是 90 秒，实得 %d", d.OpenSeconds)
	}
	if len(d.Members) != 2 {
		t.Fatalf("该有房主 + 1 个朋友 = 2 行，实得 %d", len(d.Members))
	}
	h, j := d.Members[0], d.Members[1]
	if !h.Host || h.Seat != 0 || h.JoinSeconds != 90 {
		t.Errorf("房主那行不对：%+v", h)
	}
	if j.Host || j.Seat != 1 || j.JoinSeconds != 30 || j.Game != "泰拉瑞亚" {
		t.Errorf("朋友那行不对：%+v", j)
	}

	// ⭐⭐ 来源地址必须是**打码**的（跟列表一个口径）——
	// 这条红了就意味着"后台详情页能看到完整公网 IP"✗
	for _, m := range d.Members {
		if strings.Contains(m.From, "120.227.45.9") || strings.Contains(m.From, "39.144.10.7") {
			t.Errorf("成员来源地址没打码：%q ✗", m.From)
		}
	}
	if strings.Contains(d.From, "120.227.45.9") {
		t.Errorf("房主来源地址没打码：%q ✗", d.From)
	}
}

// ⭐ 详情里**绝不能出现密码本身**（只回"有没有设"）。
//
// 反证方法：把 roomDetail 里的 HasPass 换成直接塞 r.pass ⇒ 立刻红 ✓
func TestRoomDetailNeverLeaksPass(t *testing.T) {
	r := mkRoom("T-PASS01", "1.2.3.4", 4, "", 0, time.Minute)
	r.pass = "hunter2-very-secret"
	raw, err := json.Marshal(roomDetailOf(r, roomsNow))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2-very-secret") {
		t.Fatal("详情里出现了房间密码本身 ✗ —— 面板只需要知道\"有没有设\"，" +
			"回密码等于多开一处泄露面")
	}
}

// ── 踢人 ───────────────────────────────────────────────────

// 造一条"能被读"的假连接：net.Pipe 是**无缓冲**的，没人读的时候
// Send 会一直堵着 ⇒ 后面起个 goroutine 不停地读（真实 TCP 有发送缓冲，
// 不会这样；这是测试脚手架的脾气，不是产品的问题）✓
type pipeGot struct {
	msg proto.Msg
	err error
}

func pipePair(t *testing.T, tag string) (*countingConn, *signal.Client, chan pipeGot) {
	t.Helper()
	a, b := net.Pipe()
	cc := newCountingConn(a)
	readCl := signal.New(b)
	done := make(chan pipeGot, 8)
	go func() {
		for {
			m, err := readCl.Recv(3 * time.Second)
			done <- pipeGot{m, err}
			if err != nil {
				return
			}
		}
	}()
	return cc, readCl, done
}

func TestKickMemberRefusesHost(t *testing.T) {
	r := mkRoom("T-KICK00", "1.2.3.4", 4, "", 0, time.Minute)
	roomsMu.Lock()
	rooms[r.code] = r
	roomsMu.Unlock()
	t.Cleanup(func() {
		roomsMu.Lock()
		delete(rooms, r.code)
		roomsMu.Unlock()
	})

	if err := kickMember(r.code, 0); err == nil {
		t.Error("踢 0 号座位（房主）该被拒 —— 要停就关掉整个房间 ✗")
	}
}

// ⭐ 踢朋友：名单里要摘掉、**而且连接真的要断**。
//
// 反证方法：把 kickMember 末尾那句 m.cl.CloseWrite() 删掉 ⇒ 立刻红 ✓
func TestKickMemberClosesConn(t *testing.T) {
	hostCC, _, _ := pipePair(t, "host")
	jCC, jRead, jDone := pipePair(t, "join")

	r := &room{code: "T-KICK01", maxSize: 4, bans: map[string]*ban{},
		hostIP: "1.2.3.4", openedAt: time.Now()}
	r.host = &member{id: "h", cid: "h", seat: 0, cl: signal.New(hostCC),
		remoteIP: "1.2.3.4", counting: hostCC}
	r.joins = []*member{{id: "j1", cid: "j1", seat: 1, cl: signal.New(jCC),
		remoteIP: "5.6.7.8", counting: jCC}}

	roomsMu.Lock()
	rooms[r.code] = r
	roomsMu.Unlock()
	t.Cleanup(func() {
		roomsMu.Lock()
		delete(rooms, r.code)
		roomsMu.Unlock()
	})

	if err := kickMember(r.code, 1); err != nil {
		t.Fatalf("踢朋友该成功，实得 %v", err)
	}

	r.mu.Lock()
	n := len(r.joins)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("踢完名单里该没这个人了，实得还剩 %d 个 ✗", n)
	}

	// 被踢的那位会**先收到一句通知**，然后连接断掉（EOF）✓
	// 📌 顺序不能反 —— 先断的话那句通知会被 RST 冲掉（关房间时踩过这个坑）
	//
	// 🩸 判断要**同时看"收到的是哪条消息"和"有没有报错"**：
	// 光看"有没有报错"分不清"收到了 TypeKicked"和"读超时了" ——
	// 那两种在这里都是 err != nil / err == nil 的组合，很容易写反 ✗
	// （第一版就是写反的：把"成功收到消息"当成了失败）
	var got pipeGot
	select {
	case got = <-jDone:
	case <-time.After(3 * time.Second):
		t.Fatal("被踢的人那边一直没动静 ✗")
	}
	if got.err != nil {
		t.Fatalf("第一条该是通知、不该报错，实得 err=%v ✗", got.err)
	}
	if got.msg.Type != proto.TypeKicked {
		t.Errorf("第一条该是 TypeKicked（客户端只认它），实得 %q", got.msg.Type)
	}
	if !strings.Contains(got.msg.Notice, "管理员") {
		t.Errorf("提示该说清是管理员干的，实得 %q ✗", got.msg.Notice)
	}

	// 再确认：连接**真的**断了（不是只发了条通知就没下文）。
	// 🩸 只发通知是不够的 —— 改过的客户端可以不理会 ✗
	select {
	case got2 := <-jDone:
		if got2.err == nil {
			t.Fatal("通知之后连接还开着（又收到一条消息）—— 那对改过的客户端不成立 ✗")
		}
		if !errors.Is(got2.err, io.EOF) {
			t.Errorf("断开该是 EOF，实得 %v —— 超时的话说明连接还开着 ✗", got2.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("通知发完之后连接一直没断 ✗")
	}
	_ = jRead
}

// 别人（不存在的座位）要能给出人话的错误，而不是悄悄成功
func TestKickMemberMissingSeat(t *testing.T) {
	r := mkRoom("T-KICK02", "1.2.3.4", 4, "", 0, time.Minute)
	roomsMu.Lock()
	rooms[r.code] = r
	roomsMu.Unlock()
	t.Cleanup(func() {
		roomsMu.Lock()
		delete(rooms, r.code)
		roomsMu.Unlock()
	})

	if err := kickMember(r.code, 3); err == nil {
		t.Error("房间⾥没这个座位，该报错而不是静默成功 ✗")
	}
	if err := kickMember("T-没有这个", 1); err == nil {
		t.Error("房间不存在，该报错 ✗")
	}
}

// ── 游戏名清洗 ─────────────────────────────────────────────

func TestSanitizeGame(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"我的世界", "我的世界"},
		{"  泰拉瑞亚  ", "泰拉瑞亚"},
		// 🩸 控制字符必须去掉：进了表格很难看，进了日志还能伪造出一行假日志 ✗
		{"我的世界\n2026/01/01 假装是日志", "我的世界2026/01/01 假装是日志"},
		{"a\tb", "ab"},
		// 截断到 24 个字符（清单里最长的名字是 5 个汉字）
		{strings.Repeat("x", 40), strings.Repeat("x", 24)},
	}
	for _, c := range cases {
		if got := sanitizeGame(c.in); got != c.want {
			t.Errorf("sanitizeGame(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}
	// 换个说法再钉一次：清完的东西里不许有换行/制表（哪怕输入是那样）
	if got := sanitizeGame("a\nb"); strings.ContainsAny(got, "\n\r\t") {
		t.Errorf("清完之后还有控制字符：%q ✗", got)
	}
	_ = proto.TypeKicked // 让这个文件跟协议有关联，别让 import 被当成没用
}

// ── 游戏对不上 / 没报游戏 ⇒ **拒绝加入**（2026-10-16 晚老板改的要求）──
//
// 🩸 这道拦的依据是**客户端自报的游戏名** ⇒ 挡的是"手滑选错游戏"，不是恶意 ✗
//   真正基于**事实**的那道在客户端（internal/node 的 sniffGame 看握手数据）✓
//
// 🩸🩸 而且**没报也拒** —— 不为"老客户端"留口子：
//   这个项目在测试阶段、**没有兼容负担**，让用户更新就行 ✗
//   （留一条"没报也能进"的路，就等于给所有人开了一条随便进的路 ✗）

// ⭐ 两边报的不一样 ⇒ 拒；理由要说清两边各是什么、以及下一步怎么做
//
// 反证方法：让 refuseJoinGame 永远返回 "" ⇒ 立刻红 ✓
func TestGameRefuseOnMismatch(t *testing.T) {
	why := refuseJoinGame("我的世界", "泰拉瑞亚")
	if why == "" {
		t.Fatal("两边报的游戏不一样，该拒绝加入 ✗")
	}
	// 要说清"那个房间是玩哪款的" + 下一步动作
	if !strings.Contains(why, "我的世界") {
		t.Errorf("理由里没说是哪款游戏的房间，玩家不知道该切哪个：%s", why)
	}
	if !strings.Contains(why, "切到对应的游戏再加入") {
		t.Errorf("理由得给出下一步动作：%s", why)
	}
	// ⭐ 刻意**不**提用户自己报的游戏名 —— 他自己选的，他知道 ✓
	// （反证方法：把 joinerGame 拼进文案 ⇒ 立刻红）
	if strings.Contains(why, "泰拉瑞亚") {
		t.Errorf("不该把用户自己报的游戏名再说一遍（啰嗦）：%s", why)
	}
}

// ⭐⭐ 没报游戏名 ⇒ **一样拒**（这是"不留口子"那条的钉子）。
//
// 反证方法：在 refuseJoinGame 开头加一句 `if joinerGame == "" { return "" }` ⇒
// 立刻红 ✓
func TestGameRefuseWhenNotReported(t *testing.T) {
	// 加入者没报
	if why := refuseJoinGame("我的世界", ""); why == "" {
		t.Error("加入者没报游戏名，该拒 ✗（不为老客户端留口子）")
	}
	// 房主没报（现在建不出这种房，是防御性的一支）
	if why := refuseJoinGame("", "我的世界"); why == "" {
		t.Error("房主没报游戏名，该拒 ✗")
	}
	// 都没报
	if why := refuseJoinGame("", ""); why == "" {
		t.Error("两边都没报，该拒 ✗")
	}
	// 两边都报了且一样 ⇒ 正常放行 ✓（别把正常情况也拦了）
	if why := refuseJoinGame("我的世界", "我的世界"); why != "" {
		t.Errorf("两边一样时不该拒，实得 %q ✗", why)
	}
}

// ⭐ 建房也要报游戏 —— 源头堵住（没有游戏名的房间谁都能进）✗
//
// 反证方法：让 refuseHostGame 永远返回 "" ⇒ 立刻红 ✓
func TestRefuseHostGameRequiresGame(t *testing.T) {
	if why := refuseHostGame(""); why == "" {
		t.Error("没说开哪款游戏就建房，该拒 ✗（那种房间谁都能进）")
	}
	if why := refuseHostGame("我的世界"); why != "" {
		t.Errorf("报了游戏的建房不该被拦，实得 %q ✗", why)
	}
	// 那句提示得能照着做（让用户知道要去更新 + 选游戏）
	if why := refuseHostGame(""); !strings.Contains(why, "更新") {
		t.Errorf("拒绝理由该告诉用户怎么办（更新客户端 + 选游戏）：%s", why)
	}
}

// ── P1：房主会话复查（M3）────────────────────────────────────

// ⭐ 空令牌 / 没配账号库 ⇒ 一律算"可用"。
//
// 🩸 不能因为"我们这边没配置"就把房主踢掉 ✗（本地免验证跑的场景）
func TestHostSessionUsableWhenNoToken(t *testing.T) {
	old := st
	st = nil
	defer func() { st = old }()

	if !hostSessionUsable("") {
		t.Error("空令牌该算可用（本地免验证跑的场景）✗")
	}
	if !hostSessionUsable("随便一串") {
		t.Error("没配账号库时该算可用 —— 没有会话可查时不能把人踢掉 ✗")
	}
}

// ⭐⭐ 核心：令牌失效 ⇒ 复查循环要**先说一句人话，再把连接关掉**。
//
// 用 net.Pipe 造一条连接，跑一个 10ms 一轮的复查，喂一个无效令牌 ⇒
// 对面该**先收到**「登录已失效」那句，然后读到 EOF ✓
//
// 🩸 **顺序不能反**：先关后说的话，那句话会被 RST 冲掉 ✗
//
//	（实测踩过：只关不说的话，房主看到的是"网络断了，检查网络"—— 误导 ✗）
//
// 反证方法：把 `cl.Send(...)` 那句删掉 ⇒ 第一条就变成 EOF ⇒ 立刻红 ✓
func TestHostSessionRecheckClosesOnInvalid(t *testing.T) {
	dir := t.TempDir()
	saved, err := auth.Open(dir, false)
	if err != nil {
		t.Fatalf("建临时账号库失败: %v", err)
	}
	old := st
	st = saved
	defer func() { st = old }()

	serverSide, peer := net.Pipe()
	defer peer.Close()
	cl := signal.New(serverSide)

	// 📌 必须**并发读**：net.Pipe 是同步的，写完之前对面不读就会卡住 ✗
	got := make(chan pipeGot, 8)
	go func() {
		readCl := signal.New(peer)
		for {
			m, err := readCl.Recv(3 * time.Second)
			got <- pipeGot{m, err}
			if err != nil {
				return
			}
		}
	}()

	stop := make(chan struct{})
	defer close(stop)
	// 令牌是编的 ⇒ VerifyToken 一定不认 ✓
	go recheckHostSession(cl, "T-RECHK1", "编的令牌", stop, 10*time.Millisecond)

	// ① 第一条：那句人话
	var first pipeGot
	select {
	case first = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("复查没动静 —— 该先说一句再断 ✗")
	}
	if first.err != nil {
		t.Fatalf("第一条该是那句话、不该是错误，实得 %v ✗（是不是先断后说了？）", first.err)
	}
	if first.msg.Type != proto.TypeError {
		t.Errorf("该用 TypeError 送这句话（客户端只认它），实得 %q", first.msg.Type)
	}
	for _, want := range []string{"登录", "重新登录"} {
		if !strings.Contains(first.msg.Err, want) {
			t.Errorf("提示里没提 %q，玩家不知道该干什么：%s", want, first.msg.Err)
		}
	}

	// ② 第二条：断开（EOF）—— 它得**真的断**，不能只发一句话就完
	select {
	case second := <-got:
		if second.err == nil {
			t.Fatal("说完那句之后连接还开着（又收到一条消息）✗")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("说完那句之后一直没断 ✗（time.AfterFunc 那个兜底也没生效？）")
	}
}

// ⭐ 反过来：复查不能**误杀**（本地免验证跑起来的房主，没令牌 ⇒ 不许关）✓
func TestHostSessionRecheckKeepsTokenlessHost(t *testing.T) {
	serverSide, peer := net.Pipe()
	defer peer.Close()
	defer serverSide.Close()
	cl := signal.New(serverSide)

	stop := make(chan struct{})
	go recheckHostSession(cl, "T-RECHK2", "", stop, 10*time.Millisecond)
	time.Sleep(80 * time.Millisecond) // 跑好几轮

	// 没令牌 ⇒ 复查该直接返回、**不碰连接**。
	//
	// 🩸 判据要准：`net.Pipe` 的写会**阻塞到有人读**，而复查循环根本不读
	// ⇒ 只要连接还开着，写就必然 timeout ✗ 所以不能拿"写成功"当"没被关" ✗
	//   · 连接被关 ⇒ `io.ErrClosedPipe`（立刻返回）
	//   · 连接还开着 ⇒ 超时 ✓
	_ = peer.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
	_, err := peer.Write([]byte("x"))
	switch {
	case errors.Is(err, io.ErrClosedPipe):
		t.Fatal("没令牌的房主被误杀了 ✗（连接已被关）")
	case errors.Is(err, os.ErrDeadlineExceeded):
		// ✓ 期望就是这条：连接还开着，只是没人读
	default:
		t.Fatalf("意外的错误：%v", err)
	}
	close(stop)
}

// ── P1：全局并发上限（M2）────────────────────────────────────

// ⭐⭐ 全局连接数顶满 ⇒ **任何来源**都不放进来。
//
// 🩸 这条是"最后一道兜底"：单 IP 的限制靠"来源 IP 够多"就能绕，
//
//	而没有全局上限的话，连接洪水能直接打满 FD 和内存 ✗
//	（allowConn 跑在"读 register、**校验令牌之前**"⇒ 没账号的人也能打）
//
// 反证方法：把 allowConn 里 `if statConns.Load() >= maxConnsTotal` 那段删掉 ⇒ 立刻红 ✓
func TestAllowConnHitsGlobalCap(t *testing.T) {
	saved := statConns.Load()
	defer statConns.Store(saved) // 全局状态，务必还原 ✓

	// 顶满全局计数
	statConns.Store(maxConnsTotal)

	ok, why := allowConn("203.0.113.9") // 一个从没来过的 IP
	if ok {
		t.Fatal("全局已满还放进来了 ✗（少了一道兜底）")
	}
	if why == "" {
		t.Error("拒绝该给个原因（日志要能分清是全局满了还是单个 IP 太猛）✗")
	}
	if !strings.Contains(why, "全局") {
		t.Errorf("原因该说清是全局满，实得 %q ✗", why)
	}

	// 还原之后该恢复正常（别把"顶满"留在那儿污染别的测试）✓
	statConns.Store(0)
	if ok, _ := allowConn("203.0.113.10"); !ok {
		t.Fatal("计数还原之后该放行 ✗")
	}
	releaseConn("203.0.113.10")
}
