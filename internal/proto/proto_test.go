// proto 的测试。
//
// 两条主线：
//
//	① 显示名规则（日志/黑名单都靠它，几处各拼一遍迟早拼岔 ✓）
//	② 🩸 **协议向后兼容**：加字段一律 omitempty、老的那端只是忽略不会报错 ——
//	   这是铁律，得有个测试守着，不然哪天顺手去掉 omitempty 就把老客户端打死了 ✗
package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDisplayName(t *testing.T) {
	cases := []struct {
		label string
		seat  int
		host  bool
		want  string
	}{
		{"小明", 0, true, "房主 小明"},
		{"", 0, true, "房主"}, // 房主没起名：就是「房主」，不该是「房主 0」
		{"阿强", 1, false, "玩家 阿强"},
		{"", 1, false, "玩家 1"}, // 没起名用座位号兜底
		{"", 0, false, "玩家"},   // 座位 0 但不是房主：没有号可拼，就只给角色
	}
	for _, c := range cases {
		if got := DisplayName(c.label, c.seat, c.host); got != c.want {
			t.Errorf("DisplayName(%q,%d,%v) = %q，该是 %q", c.label, c.seat, c.host, got, c.want)
		}
	}
}

// 老客户端发来的 JSON（没有新字段）必须能正常读出来，而且是零值。
func TestOldJSONWithoutNewFieldsStillParses(t *testing.T) {
	old := `{"type":"register","role":"host","room":"TI123456DY","clientID":"abc","label":"小明"}`
	var m Msg
	if err := json.Unmarshal([]byte(old), &m); err != nil {
		t.Fatalf("老客户端的 JSON 读不了: %v —— 协议不兼容了", err)
	}
	if m.Room != "TI123456DY" || m.Label != "小明" {
		t.Errorf("字段没读对：%+v", m)
	}
	if m.Left {
		t.Error("老 JSON 里没有 left，该是零值")
	}
	if m.PubKey != nil {
		t.Error("老 JSON 里没有 pubKey，该是 nil")
	}
}

// 零值字段**不许写出去** —— 这就是 omitempty 的用武之处：
// 老客户端收到不认识的名字顶多是忽略，但把一堆 false/0 塞过去纯属噪音 ✓
func TestZeroFieldsAreOmitted(t *testing.T) {
	b, err := json.Marshal(Msg{Type: TypeRegister, Room: "TI123456DY"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, k := range []string{"left", "pubKey", "peer", "cands", "bans", "members"} {
		if strings.Contains(s, `"`+k+`"`) {
			t.Errorf("零值字段 %q 不该被写进 JSON：%s", k, s)
		}
	}
	if !strings.Contains(s, `"room":"TI123456DY"`) {
		t.Errorf("有值的字段该写出去：%s", s)
	}
}

// 带新字段的消息要能原样往返（服务器中转公钥/退出原因就靠这个）。
func TestNewFieldsRoundTrip(t *testing.T) {
	in := Msg{
		Type:   TypePair,
		PubKey: []byte("0123456789abcdef0123456789abcdef"), // 32 字节 X25519 公钥
		Left:   true,
		Peer:   []string{"1.2.3.4:25565"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Msg
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if string(out.PubKey) != string(in.PubKey) {
		t.Errorf("公钥没往返成功：%v", out.PubKey)
	}
	if !out.Left {
		t.Error("Left 没往返成功")
	}
	if len(out.Peer) != 1 || out.Peer[0] != "1.2.3.4:25565" {
		t.Errorf("候选地址没往返成功：%v", out.Peer)
	}
}

// 成员版的显示名要跟函数版一致（别一个改了一个没改）。
func TestMemberDisplayName(t *testing.T) {
	m := Member{Label: "阿强", Seat: 2}
	if got, want := m.DisplayName(), "玩家 阿强"; got != want {
		t.Errorf("Member.DisplayName() = %q，该是 %q", got, want)
	}
	h := Member{Seat: 0, Host: true}
	if got, want := h.DisplayName(), "房主"; got != want {
		t.Errorf("房主那条 = %q，该是 %q", got, want)
	}
}
