// 服务端字段校验的测试。
//
// 这些校验的价值全在"挡不住改过的客户端"上 —— 所以测试也照着**恶意输入**写，
// 而不是照着正常客户端会发什么写。
package main

import (
	"strings"
	"testing"

	"p2plink/internal/proto"
)

func TestValidateRegister(t *testing.T) {
	ok := proto.Msg{
		Type: proto.TypeRegister, Role: "host", Room: "TI123456DY",
		ClientID: "0123456789abcdef", Label: "小明", Avatar: 3, MaxSize: 4,
		Cands: []string{"192.168.1.5:25565", "1.2.3.4:25565"},
	}
	if err := validateRegister(ok); err != "" {
		t.Fatalf("正常的注册消息被判非法：%s", err)
	}

	// 旧格式的房间 ID（6 位随机字符）也得放行，否则还没更新的客户端全被挡在门外
	old := ok
	old.Room = "K7M2XP"
	if err := validateRegister(old); err != "" {
		t.Errorf("旧格式房间 ID 该放行，却被判：%s", err)
	}

	bad := []struct {
		name string
		mod  func(*proto.Msg)
	}{
		{"房间 ID 太短", func(m *proto.Msg) { m.Room = "AB" }},
		{"房间 ID 太长（想撑内存）", func(m *proto.Msg) { m.Room = strings.Repeat("A", 100000) }},
		{"房间 ID 带小写", func(m *proto.Msg) { m.Room = "ti123456dy" }},
		{"房间 ID 带奇怪字符", func(m *proto.Msg) { m.Room = "../etc/passwd" }},
		{"房间 ID 带换行", func(m *proto.Msg) { m.Room = "TI12\n3456" }},
		{"密码过长", func(m *proto.Msg) { m.RoomPass = strings.Repeat("x", maxPassLen+1) }},
		{"clientID 过长", func(m *proto.Msg) { m.ClientID = strings.Repeat("a", maxClientIDLen+1) }},
		{"头像下标越界（负）", func(m *proto.Msg) { m.Avatar = -1 }},
		{"头像下标越界（大）", func(m *proto.Msg) { m.Avatar = maxAvatarIdx + 1 }},
		{"候选地址过多", func(m *proto.Msg) {
			m.Cands = make([]string, maxCands+1)
			for i := range m.Cands {
				m.Cands[i] = "1.2.3.4:25565"
			}
		}},
		{"单条候选地址过长", func(m *proto.Msg) { m.Cands = []string{strings.Repeat("a", maxCandLen+1)} }},
	}
	for _, c := range bad {
		m := ok
		m.Cands = append([]string(nil), ok.Cands...)
		c.mod(&m)
		if err := validateRegister(m); err == "" {
			t.Errorf("%s：该被拒绝，却放行了", c.name)
		}
	}
}

// 边界上刚好合法的那一侧也要测 —— 别把上限写成"少一位才合法"。
func TestValidateRegisterBoundary(t *testing.T) {
	m := proto.Msg{
		Type: proto.TypeRegister, Role: "join", Room: "TI123456DY",
		ClientID: strings.Repeat("a", maxClientIDLen),
		RoomPass: strings.Repeat("x", maxPassLen),
		Avatar:   maxAvatarIdx,
		Cands:    make([]string, maxCands),
	}
	for i := range m.Cands {
		m.Cands[i] = "1.2.3.4:25565"
	}
	if err := validateRegister(m); err != "" {
		t.Errorf("刚好卡在上限的合法值被判非法：%s", err)
	}
}

func TestPassEqual(t *testing.T) {
	if !passEqual("Abc123Xy", "Abc123Xy") {
		t.Error("相同口令该判相等")
	}
	if passEqual("Abc123Xy", "Ts24d4") {
		t.Error("不同口令判成相等了")
	}
	if passEqual("Abc123Xy", "Abc123Xyx") {
		t.Error("长度不同也判成相等了")
	}
	if !passEqual("", "") {
		t.Error("两个空口令该判相等")
	}
}

// 🩸 IPv6 候选必须收得下（2026-10-18 上 IPv6 时补的）。
//
// 原来 `maxCandLen = 45`，注释写的是"IPv4:port 约 21 字符，给域名留一倍余量"。
// 但 v6 地址最长 39 字符，加方括号和端口就是 **47** ——
// 实测 `[240e:3b0:1234:5678:9abc:def0:1234:5678]:27154` = 46 字符 ⇒ 超限。
//
// 后果不是"少一条候选"，而是**整个注册消息被拒** ⇒ 房主发不出候选 ⇒
// 打洞压根不会开始，现象上像"卡在等待"，最难查 ✗
func TestV6CandidatesAccepted(t *testing.T) {
	base := func(cands []string) proto.Msg {
		return proto.Msg{
			Type: proto.TypeRegister, Role: "host", Room: "TI123456DY",
			ClientID: "0123456789abcdef", Label: "小明", Avatar: 3, MaxSize: 4,
			Cands: cands,
		}
	}

	v6cands := []string{
		"[240e:3b0:1234:5678:9abc:def0:1234:5678]:27154",
		"[2001:db8:abcd:1234:5678:9abc:def0:1234]:27154",
	}
	for _, c := range v6cands {
		// 先钉住"这些用例确实比老上限长" —— 否则这条测试是空转的
		if len(c) <= 45 {
			t.Fatalf("用例没意义：%q 只有 %d 字符，老上限 45 也能过", c, len(c))
		}
		if err := validateRegister(base([]string{c})); err != "" {
			t.Errorf("v6 候选 %q（%d 字符）被拒了：%s\n"+
				"（一条被拒 = 整个注册消息作废，房主发不出候选）", c, len(c), err)
		}
	}

	// 放宽不等于不设限：超长输入照样要挡
	tooLong := "[" + strings.Repeat("a", maxCandLen) + "]:27154"
	if err := validateRegister(base([]string{tooLong})); err == "" {
		t.Error("超过上限的候选该被拒，却放行了")
	}
}

// validateCands 是"重发候选"那条新路的闸门（2026-10-18）。
//
// 🩸 不卡的话，客户端可以一次塞几百条上来 —— 服务器原样转给对面，
// 对面照着几十上百个地址发包，我们等于帮着放大流量 ✗
func TestValidateCands(t *testing.T) {
	if msg := validateCands([]string{"1.2.3.4:5000", "[2409::1]:5000"}); msg != "" {
		t.Errorf("正常候选被拒了：%s", msg)
	}

	tooMany := make([]string, maxCands+1)
	for i := range tooMany {
		tooMany[i] = "1.2.3.4:5000"
	}
	if msg := validateCands(tooMany); msg == "" {
		t.Error("超过条数上限该被拒")
	}

	if msg := validateCands([]string{strings.Repeat("a", maxCandLen+1)}); msg == "" {
		t.Error("单条超长该被拒")
	}

	// 空列表放行：调用方另有逻辑（没候选就别转发）
	if msg := validateCands(nil); msg != "" {
		t.Errorf("空列表不该报错，实际：%s", msg)
	}
}
