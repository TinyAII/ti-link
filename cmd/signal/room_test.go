// 信令服务器的端到端测试：起一个真监听器，用真的客户端连上去走一遍。
//
// 覆盖的是这一批新加/修好的东西：
//
//	· 房间密码（设了要填对、改密码后旧的立刻失效）
//	· 昵称（客户端填什么，名单里就是什么；重名自动加序号）
//	· 黑名单（拉黑后换个连接进来照样拦住 —— 认的是设备 ID；
//	  以及解除之后能重新进来）
//
// 之所以要真连：这几件事全是「两边各写一半」的活（客户端写的键、服务器查的键），
// 单看某一侧都挑不出毛病，必须两头对上才看得出对不对。
package main

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"p2plink/internal/keyex"
	"p2plink/internal/proto"
	"p2plink/internal/signal"
)

func startTestServer(t *testing.T) string {
	t.Helper()
	// 🩸 这些测试验的是**房间逻辑**（房主/名单/黑名单/密码），不是"谁能连" ⇒
	// 显式关掉登录校验。不关的话每条连接都会先被"请先在客户端里登录"挡掉，
	// 而报错长成"等 room_ok 时读失败: EOF"，看着像房间逻辑坏了，其实不是 ✗
	//
	// 「登录令牌管不管用」是另一件事，由 authgate_test.go 拿**真账号库**单独测 ✓
	// （两件事各自测各自的那一段，别混在一个测试里 —— 混着测的话，
	//  一个挂了根本分不清是哪一段坏的。）
	old := insecureNoAuth
	insecureNoAuth = true
	t.Cleanup(func() { insecureNoAuth = old })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起测试监听器失败: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
	return ln.Addr().String()
}

// dialT 连上测试服务器。顺手把 IP 限流表清空 —— 测试会在同一秒里
// 开好几条连到 127.0.0.1 的连接，不清的话会被每秒 5 条的上限挡住。
func dialT(t *testing.T, addr string) *signal.Client {
	t.Helper()
	ipMu.Lock()
	ipLimits = map[string]*ipLimit{}
	ipMu.Unlock()

	c, err := signal.Dial(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("连测试服务器失败: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func send(t *testing.T, c *signal.Client, m proto.Msg) {
	t.Helper()
	if err := c.Send(m); err != nil {
		t.Fatalf("发送 %s 失败: %v", m.Type, err)
	}
}

// recvUntil 一直读到想要的类型为止（中间那些名单/黑名单推送直接跳过）。
func recvUntil(t *testing.T, c *signal.Client, want string) proto.Msg {
	t.Helper()
	for i := 0; i < 20; i++ {
		m, err := c.Recv(2 * time.Second)
		if err != nil {
			t.Fatalf("等 %s 时读失败: %v", want, err)
		}
		if m.Type == want {
			return m
		}
	}
	t.Fatalf("读了 20 条都没等到 %s", want)
	return proto.Msg{}
}

// findMember 按「给人看的名字」找一条 —— 用 DisplayName 拼出来的那个，
// 也就是界面上真正显示的字。不直接比 Label：Label 是纯昵称，
// 没起名时是空串，拿它当判据会漏掉「玩家 N」这类兜底名。
func findMember(ms []proto.Member, display string) (proto.Member, bool) {
	for _, m := range ms {
		if m.DisplayName() == display {
			return m, true
		}
	}
	return proto.Member{}, false
}

// 测试里用的昵称一律**取自词库** —— 服务端会把"词库拼不出来"的名字当成
// 没起名清洗掉（见 sanitizeLabel）。随手写个「小明」的话，名单里会变成
// 「玩家 N」，断言全部对不上；而失败信息只会说"没找到「房主 小明」"，
// 很容易把人往错的方向带（去查 DisplayName 或前端，其实根因在这儿）。
//
// 名字是从 internal/nick 的四张表里拼的，改词库时跟着改一下。
// 真忘了改也不怕 —— 测试会红，而红的位置就是这儿。
const (
	nickA = "喜欢捣蛋的雇员沐恩" // 房主
	nickB = "沉迷摸鱼的学徒阿狸" // 朋友甲
	nickC = "最爱钓鱼的术士小满" // 朋友乙
	nickD = "擅长搬砖的矿工青豆" // 朋友丙
)

func hostRegister(t *testing.T, c *signal.Client, room, roomPass, label string) {
	t.Helper()
	// 协议现在要求带临时公钥（隧道密钥靠它协商）。用真的 X25519 ——
	// 糊一段 32 字节虽然也能过校验，但那样就把"密钥到底对不对"这件事漏掉了。
	send(t, c, proto.Msg{
		Type: proto.TypeRegister, Role: "host", Game: "我的世界", Room: room,
		RoomPass: roomPass, ClientID: "cid-host", Label: label, MaxSize: 8,
		PubKey: newPub(t),
	})
}

func joinRegister(t *testing.T, c *signal.Client, room, roomPass, cid, label string) {
	t.Helper()
	send(t, c, proto.Msg{
		Type: proto.TypeRegister, Role: "join", Game: "我的世界", Room: room,
		RoomPass: roomPass, ClientID: cid, Label: label,
		Cands:  []string{"10.0.0.1:5000"},
		PubKey: newPub(t),
	})
}

func TestRoomPassLabelAndBan(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-PASS-1"

	// ---- 建房：设密码 abcd，昵称「小明」----
	host := dialT(t, addr)
	hostRegister(t, host, room, "abcd", nickA)
	ok := recvUntil(t, host, proto.TypeRoomOK)
	if ok.RoomPass != "abcd" {
		t.Errorf("RoomOK 该把房间密码回给客户端，实得 %q", ok.RoomPass)
	}

	// ---- ① 密码不对：进不来，而且报的是「密码」----
	bad := dialT(t, addr)
	joinRegister(t, bad, room, "zzzz", "cid-guest", nickB)
	if em := recvUntil(t, bad, proto.TypeError); !strings.Contains(em.Err, "密码") {
		t.Errorf("密码错该给密码相关的提示，实得 %q", em.Err)
	}

	// ---- ② 密码对：进得来，房主名单里出现昵称，房主条目带 host 标志 ----
	j := dialT(t, addr)
	joinRegister(t, j, room, "abcd", "cid-guest", nickB)
	// 建房时服务器已经先推过一份"只有房主"的名单了（见 TestHostGetsOwnRowOnCreate），
	// 所以不能只读一条就断言 —— 得读到"阿强进来了"那一份为止。
	ms := waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 "+nickB)
		return ok
	})
	if _, found := findMember(ms, "玩家 "+nickB); !found {
		t.Errorf("名单里该有「阿强」，实得 %+v", ms)
	}
	if hm, found := findMember(ms, "房主 "+nickA); !found || !hm.Host {
		t.Errorf("房主那条该带 host 标志，实得 %+v", ms)
	}
	pair := recvUntil(t, host, proto.TypePair)
	if pair.Label != nickB {
		t.Errorf("pair 里该带昵称「阿强」，实得 %q", pair.Label)
	}

	// ---- ③ 拉黑：黑名单键必须是**纯 clientID**（不是带序号的连接 ID）----
	send(t, host, proto.Msg{Type: proto.TypeBan, TargetID: pair.TargetID})
	bl := recvUntil(t, host, proto.TypeBanList)
	if len(bl.Bans) != 1 || bl.Bans[0].Label != nickB {
		t.Fatalf("拉黑后黑名单该有「阿强」一条，实得 %+v", bl.Bans)
	}
	key := bl.Bans[0].Key
	if key != "cid-guest" {
		t.Errorf("黑名单的键该是纯 clientID(cid-guest)，实得 %q"+
			"（写成带序号的连接 ID 就会出现「拉黑了却还能进来」）", key)
	}
	j.Close()

	// ---- ④ 被拉黑的人换一条新连接、同 clientID、密码也对 —— 还是得拦在外面 ----
	// （这就是那个既有 bug：写黑名单用带序号的 ID、查黑名单用纯 clientID，两边对不上）
	again := dialT(t, addr)
	joinRegister(t, again, room, "abcd", "cid-guest", nickB)
	if bm := recvUntil(t, again, proto.TypeBanned); !strings.Contains(bm.Notice, "拉黑") {
		t.Errorf("被拉黑的人该收到拉黑提示，实得 %q", bm.Notice)
	}
	again.Close()

	// ---- ⑤ 解除拉黑之后，同一个人能重新进来 ----
	send(t, host, proto.Msg{Type: proto.TypeUnban, BanKey: key})
	if bl2 := recvUntil(t, host, proto.TypeBanList); len(bl2.Bans) != 0 {
		t.Fatalf("解除后黑名单该空了，实得 %+v", bl2.Bans)
	}
	back := dialT(t, addr)
	joinRegister(t, back, room, "abcd", "cid-guest", nickB)
	// 能收到名单就说明进去了（进不来只会收到 error / banned）
	if ms2 := recvUntil(t, back, proto.TypeMembers); len(ms2.Members) < 2 {
		t.Errorf("解除拉黑后该能进去，名单实得 %+v", ms2.Members)
	}

	// ---- ⑥ 改密码：旧密码立刻失效，新密码可用 ----
	send(t, host, proto.Msg{Type: proto.TypeSetPass, RoomPass: "efgh"})

	old := dialT(t, addr)
	joinRegister(t, old, room, "abcd", "cid-old", nickC)
	if em := recvUntil(t, old, proto.TypeError); !strings.Contains(em.Err, "密码") {
		t.Errorf("改完密码后旧密码该被拒，实得 %q", em.Err)
	}
	old.Close()

	fresh := dialT(t, addr)
	joinRegister(t, fresh, room, "efgh", "cid-new", nickD)
	recvUntil(t, fresh, proto.TypeMembers)
}

// 名字：不填昵称要有兜底名（玩家 N），重名要自动加序号 ——
// 不然名单里两行一模一样的「小明」，房主都不知道该踢哪个。
func TestLabelFallbackAndDedup(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-NICK-1"

	host := dialT(t, addr)
	hostRegister(t, host, room, "", "")
	recvUntil(t, host, proto.TypeRoomOK)

	// 不填昵称 -> Label 留空、靠座位号兜底成「玩家 1」
	anon := dialT(t, addr)
	joinRegister(t, anon, room, "", "cid-anon", "")
	ms := waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 1")
		return ok
	})
	am, found := findMember(ms, "玩家 1")
	if !found {
		t.Errorf("不填昵称该兜底成「玩家 1」，实得 %+v", ms)
	} else {
		if am.Label != "" {
			t.Errorf("没起名的人 Label 该是空串（前缀由界面拼），实得 %q", am.Label)
		}
		if am.Seat != 1 {
			t.Errorf("第一个玩家的座位号该是 1，实得 %d", am.Seat)
		}
	}

	// 两个人同名 -> 后来那个加序号
	a := dialT(t, addr)
	joinRegister(t, a, room, "", "cid-a", nickA)
	waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 "+nickA)
		return ok
	})

	b := dialT(t, addr)
	joinRegister(t, b, room, "", "cid-b", nickA)
	ms3 := waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 "+nickA+"2")
		return ok
	})
	if _, found := findMember(ms3, "玩家 "+nickA); !found {
		t.Errorf("该有一个「玩家 小明」，实得 %+v", ms3)
	}
	if _, found := findMember(ms3, "玩家 "+nickA+"2"); !found {
		t.Errorf("重名的第二个该变成「玩家 小明2」，实得 %+v", ms3)
	}

	// 词库外的名字 -> 当成没起名（见 sanitizeLabel）。
	// 这里顺带把"拿超长字符串灌服务器"也验了：一个 50+50 字的自由名字，
	// 既过不了词库校验，也不该有任何一截出现在名单里。
	long := dialT(t, addr)
	joinRegister(t, long, room, "", "cid-long", strings.Repeat("长", 50)+"的"+strings.Repeat("名", 50))
	ms4 := waitMembers(t, host, func(ms []proto.Member) bool { return len(ms) >= 5 })
	for _, m := range ms4 {
		if len([]rune(m.Label)) > maxLabelLen {
			t.Errorf("名单里的昵称该在 %d 字以内，实得 %q", maxLabelLen, m.Label)
		}
	}
	// 房里前面已有 4 条（房主 + 没起名的那位 + 两个同名的），这条排到 4 号座
	if bm, ok := findMember(ms4, "玩家 4"); !ok {
		t.Errorf("词库外的名字该当成没起名（「玩家 4」），实得 %+v", ms4)
	} else if bm.Label != "" {
		t.Errorf("被清洗的名字 Label 该是空串，实得 %q", bm.Label)
	}
}

// 昵称白名单：词库外的名字一律当成"没起名"送进房间。
//
// 这是这条防线**唯一**的落点 —— 前端撤掉输入框只挡得住普通人
// （改一下本机 settings.json 就能绕），真正兜底的是这儿。
//
// 所以这儿的名字**故意**用词库外的（张三 / 李四），别改成 nickA 那组。
func TestLabelOutsideWordlistIsCleaned(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-CLEAN-1"

	// 房主自己的名字就不在词库里 -> 别人看到的该只是「房主」，没有名字那一截
	host := dialT(t, addr)
	hostRegister(t, host, room, "", "张三")
	recvUntil(t, host, proto.TypeRoomOK)

	ms := waitMembers(t, host, func(ms []proto.Member) bool { return len(ms) == 1 })
	if m, ok := findMember(ms, "房主"); !ok {
		t.Errorf("房主名字不在词库里时，名单里该只显示「房主」，实得 %+v", ms)
	} else if m.Label != "" {
		t.Errorf("被清洗的名字 Label 该是空串，实得 %q", m.Label)
	}

	// 朋友的名字也不在词库里 -> 他该变成「玩家 1」
	guest := dialT(t, addr)
	joinRegister(t, guest, room, "", "cid-clean", "李四")
	ms2 := waitMembers(t, host, func(ms []proto.Member) bool { return len(ms) >= 2 })

	for _, m := range ms2 {
		if strings.ContainsAny(m.Label, "张李") {
			t.Errorf("词库外的名字混进名单了：%q（这条防线漏了）", m.Label)
		}
	}
	if gm, ok := findMember(ms2, "玩家 1"); !ok {
		t.Errorf("词库外的名字该当成没起名（「玩家 1」），实得 %+v", ms2)
	} else if gm.Label != "" {
		t.Errorf("被清洗的名字 Label 该是空串，实得 %q", gm.Label)
	}
}

// cleanLabel 自己的两件事：去空白、超长截断。
//
// 📌 为什么单独测它：sanitizeLabel 让它截断之后才交给词库校验，而**词库最长的
// 名字只有 11 字**（上限 12）⇒ 截断这一步在正常路径上永远触发不到，网络测试
// 也就覆盖不着它了。可它是挡"超长字符串灌服务器"的第一道，不能不测。
func TestCleanLabelTruncates(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"   " + nickA + "  ", nickA},
		{"", ""},
		{strings.Repeat("字", maxLabelLen), strings.Repeat("字", maxLabelLen)}, // 正好到上限，不该动
		{strings.Repeat("长", 50), strings.Repeat("长", maxLabelLen)},
	}
	for _, c := range cases {
		if got := cleanLabel(c.in); got != c.want {
			t.Errorf("cleanLabel(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}
}

// waitMembers 反复读名单，直到满足条件为止。
//
// 为什么要循环：加入和断开都是**异步**推送的，一次连接动作之后，
// 队列里可能还压着上一条旧名单（比如"某人刚断开"那条）。只读一条就断言，
// 十有八九是在拿旧快照下结论。
func waitMembers(t *testing.T, c *signal.Client, ok func([]proto.Member) bool) []proto.Member {
	t.Helper()
	var last []proto.Member
	for i := 0; i < 12; i++ {
		last = recvUntil(t, c, proto.TypeMembers).Members
		if ok(last) {
			return last
		}
	}
	t.Fatalf("读了 12 条名单都没等到预期状态，最后一条是 %+v", last)
	return nil
}

// 座位号必须是「当前第几个玩家」，不能只增不减。
//
// 🩸 这是老板实际撞到的：房里只有 1 个玩家，界面却显示「玩家3」——
// 因为老写法用 `seq++`（每成功加入一次就 +1），退出过的人会把号一路顶上去。
func TestSeatNumbering(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-SEAT-1"

	host := dialT(t, addr)
	hostRegister(t, host, room, "", "")
	recvUntil(t, host, proto.TypeRoomOK)

	// 第一位（起了名，占掉 1 号座）进来又走
	first := dialT(t, addr)
	joinRegister(t, first, room, "", "cid-first", nickB)
	waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 "+nickB)
		return ok
	})
	first.Close()
	// 等服务器把断开处理干净（名单里只剩房主）
	waitMembers(t, host, func(ms []proto.Member) bool { return len(ms) == 1 })

	// 第二位没起名：1 号座随着上一位离开已经空出来，该拿回来 → 「玩家 1」。
	// 老写法这会儿会算出「玩家2」（seq 已经涨到 2），退过几次就是「玩家3」。
	second := dialT(t, addr)
	joinRegister(t, second, room, "", "cid-second", "")
	ms := waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 1")
		return ok
	})
	if _, found := findMember(ms, "玩家 3"); found {
		t.Errorf("只有两个玩家却出现「玩家 3」—— 正是那个只增不减的老 bug：%+v", ms)
	}

	// 再进一位（也没起名）：该拿 2 号，不能跟上面那位撞号
	third := dialT(t, addr)
	joinRegister(t, third, room, "", "cid-third", "")
	ms2 := waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 2")
		return ok
	})
	if _, found := findMember(ms2, "玩家 3"); found {
		t.Errorf("房里就两个玩家，不该出现「玩家 3」：%+v", ms2)
	}
}

// 头像：协议里跑的是**下标（一个数字）**，服务器要原样带回去。
//
// 中间任何一个环节把它丢了或改了，别人看到的就不是你挑的那张脸 ——
// 而这种事不会有任何报错，只会静悄悄地显示错人。
func TestAvatarRoundTrip(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-AV-1"

	host := dialT(t, addr)
	send(t, host, proto.Msg{
		Type: proto.TypeRegister, Role: "host", Game: "我的世界", Room: room,
		ClientID: "cid-host", Label: nickA, Avatar: 5, MaxSize: 8, PubKey: newPub(t),
	})
	recvUntil(t, host, proto.TypeRoomOK)

	guest := dialT(t, addr)
	send(t, guest, proto.Msg{
		Type: proto.TypeRegister, Role: "join", Game: "我的世界", Room: room,
		ClientID: "cid-g", Label: nickB, Avatar: 12, PubKey: newPub(t),
		Cands: []string{"10.0.0.1:5000"},
	})

	ms := waitMembers(t, host, func(ms []proto.Member) bool {
		_, ok := findMember(ms, "玩家 "+nickB)
		return ok
	})
	if m, _ := findMember(ms, "房主 "+nickA); m.Avatar != 5 {
		t.Errorf("房主的头像下标该是 5，实得 %d", m.Avatar)
	}
	if m, _ := findMember(ms, "玩家 "+nickB); m.Avatar != 12 {
		t.Errorf("朋友的头像下标该是 12，实得 %d", m.Avatar)
	}

	// 朋友侧收到的名单里也得带着下标（他那一页同样要画头像）
	gms := waitMembers(t, guest, func(ms []proto.Member) bool { return len(ms) >= 2 })
	for _, m := range gms {
		if m.Host && m.Avatar != 5 {
			t.Errorf("推给朋友的名单里，房主头像该是 5，实得 %d", m.Avatar)
		}
	}
}

// 房间不存在时得说人话，别让玩家对着「信令错误: xxx」猜。
func TestJoinUnknownRoom(t *testing.T) {
	addr := startTestServer(t)
	c := dialT(t, addr)
	joinRegister(t, c, "NO-SUCH-ROOM", "", "cid-x", "谁")
	em := recvUntil(t, c, proto.TypeError)
	if !strings.Contains(em.Err, "房间") {
		t.Errorf("房间不存在该给房间相关的提示，实得 %q", em.Err)
	}
}

// newPub 造一把临时公钥（只取公钥部分，测试里不关心私钥）。
func newPub(t *testing.T) []byte {
	t.Helper()
	kp, err := keyex.New()
	if err != nil {
		t.Fatal(err)
	}
	return kp.Public()
}

// 临时密钥协商走完整条路：两端各自的公钥经服务器转给对方，
// 最后**算出来的会话密钥必须一模一样** —— 这条不通，隧道就永远握不上手，
// 而表现只是"打洞成功但卡住不动"，极难排查。所以单独钉一条。
func TestKeyExchangeThroughServer(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-KEX-1"

	hostKey, err := keyex.New()
	if err != nil {
		t.Fatal(err)
	}
	guestKey, err := keyex.New()
	if err != nil {
		t.Fatal(err)
	}

	host := dialT(t, addr)
	send(t, host, proto.Msg{
		Type: proto.TypeRegister, Role: "host", Game: "我的世界", Room: room,
		ClientID: "cid-host", Label: nickA, MaxSize: 4, PubKey: hostKey.Public(),
	})
	recvUntil(t, host, proto.TypeRoomOK)

	guest := dialT(t, addr)
	send(t, guest, proto.Msg{
		Type: proto.TypeRegister, Role: "join", Game: "我的世界", Room: room,
		ClientID: "cid-guest", Label: nickB, Cands: []string{"10.0.0.1:5000"},
		PubKey: guestKey.Public(),
	})

	// 朋友那边应当收到房主的公钥
	hk := recvUntil(t, guest, proto.TypeHostKey)
	if !bytes.Equal(hk.PubKey, hostKey.Public()) {
		t.Fatalf("朋友收到的房主公钥不对：%x", hk.PubKey)
	}

	// 房主那边应当在 pair 里收到朋友的公钥
	var pairPub []byte
	for i := 0; i < 12 && pairPub == nil; i++ {
		m := recvUntil(t, host, proto.TypePair)
		pairPub = m.PubKey
	}
	if !bytes.Equal(pairPub, guestKey.Public()) {
		t.Fatalf("房主收到的朋友公钥不对：%x", pairPub)
	}

	// 关键一步：两边各自算，算出来必须一样
	hk1, err := hostKey.Shared(pairPub)
	if err != nil {
		t.Fatal(err)
	}
	hk2, err := guestKey.Shared(hk.PubKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hk1, hk2) {
		t.Fatal("两端算出的会话密钥不一致 —— 隧道会握不上手")
	}
}

// 老客户端（不带公钥）要拿到一句人话，而不是"打洞成功但卡住"。
func TestOldClientGetsClearError(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-OLD-1"

	host := dialT(t, addr)
	hostRegister(t, host, room, "", nickA)
	recvUntil(t, host, proto.TypeRoomOK)

	// 模拟一个老客户端：不带 PubKey
	old := dialT(t, addr)
	send(t, old, proto.Msg{
		Type: proto.TypeRegister, Role: "join", Game: "我的世界", Room: room,
		ClientID: "cid-old", Label: nickC,
		Cands: []string{"10.0.0.1:5000"},
	})
	m := recvUntil(t, old, proto.TypeError)
	if !strings.Contains(m.Err, "版本太旧") {
		t.Errorf("该提示版本太旧，实得 %q", m.Err)
	}
}

// 反过来：房主是老客户端时，朋友也该拿到人话。
func TestOldHostGetsClearError(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-OLD-2"

	host := dialT(t, addr)
	send(t, host, proto.Msg{
		Type: proto.TypeRegister, Role: "host", Game: "我的世界", Room: room,
		ClientID: "cid-host", Label: nickC, MaxSize: 4, // 故意不带 PubKey
	})
	recvUntil(t, host, proto.TypeRoomOK)

	guest := dialT(t, addr)
	joinRegister(t, guest, room, "", "cid-g", nickB)
	m := recvUntil(t, guest, proto.TypeError)
	if !strings.Contains(m.Err, "房主") || !strings.Contains(m.Err, "版本太旧") {
		t.Errorf("该提示房主版本太旧，实得 %q", m.Err)
	}
}

// 房主建房后要**立刻**拿到一份成员名单，而且里面得有他自己。
//
// 🩸 这个 bug 存在很久了：serveHost 原来只推了黑名单、没推名单，
// 于是房主一个人在自己房间里时成员列表是空的 —— 要等第一个朋友进来才补上。
// 而界面上那个「成员 (1 / 4)」是页面**写死的初始值**，
// 所以看着像"明明有一个人、却不显示"，特别容易被当成是前端渲染坏了。
func TestHostGetsOwnRowOnCreate(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-SELF-1"

	host := dialT(t, addr)
	hostRegister(t, host, room, "", nickA)
	recvUntil(t, host, proto.TypeRoomOK)

	// 等着收到名单 —— 而且是"只有房主自己"的那一份
	ms := waitMembers(t, host, func(ms []proto.Member) bool { return len(ms) == 1 })

	m, ok := findMember(ms, "房主 "+nickA)
	if !ok {
		t.Fatalf("建房后该立刻拿到含房主自己的名单，实得 %+v", ms)
	}
	if !m.Host {
		t.Errorf("这一条该带 Host 标志（界面靠它决定要不要显示「房主」前缀）")
	}
	if m.Seat != 0 {
		t.Errorf("房主的座位号该是 0，实得 %d", m.Seat)
	}
}

// 主动退出 vs 非正常退出：房主收到的两条通知要能区分开。
//
// 🩸 两种断开在 TCP 层面**一模一样**，服务器只能靠朋友断开前发的那条
// leave 来分辨。这条测试就是钉住这个约定 —— 少了 leave 判断，
// 「直接关掉程序」会被误报成「退出了房间」。
func TestLeaveVsDrop(t *testing.T) {
	addr := startTestServer(t)
	const room = "T-LEAVE-1"

	host := dialT(t, addr)
	hostRegister(t, host, room, "", nickA)
	recvUntil(t, host, proto.TypeRoomOK)

	// ① 主动退出：先发 leave，等它送出去再断开
	leaver := dialT(t, addr)
	joinRegister(t, leaver, room, "", "cid-leaver", nickB)
	recvUntil(t, host, proto.TypePair) // 房主先知道"有人来了"

	send(t, leaver, proto.Msg{Type: proto.TypeLeave})
	// 🩸 必须等一下再关：leave 是刚写进 TCP 的，立刻 Close 会被一起丢掉，
	// 服务器根本收不到 —— 那样测的是"断线"，不是"主动退出"。
	time.Sleep(100 * time.Millisecond)
	leaver.Close()

	left := recvUntil(t, host, proto.TypeLeave)
	if !left.Left {
		t.Errorf("发了 leave 的该判成主动退出（Left=true），实得 Left=false")
	}
	if got := proto.DisplayName(left.Label, left.Seat, false); got != "玩家 "+nickB {
		t.Errorf("通知里该带上那位的名字，实得 %q", got)
	}

	// ② 非正常退出：直接断开，不发 leave
	dropper := dialT(t, addr)
	joinRegister(t, dropper, room, "", "cid-dropper", nickD)
	recvUntil(t, host, proto.TypePair)
	dropper.Close()

	dropped := recvUntil(t, host, proto.TypeLeave)
	if dropped.Left {
		t.Errorf("没发 leave 的该判成非正常退出（Left=false），实得 Left=true")
	}
	if got := proto.DisplayName(dropped.Label, dropped.Seat, false); got != "玩家 "+nickD {
		t.Errorf("通知里该带上那位的名字，实得 %q", got)
	}
}
