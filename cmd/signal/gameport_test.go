package main

import (
	"testing"

	"p2plink/internal/proto"
)

// 服务端对「房主报上来的游戏端口」的校验。
//
// 🩸 先说清这道校验的**性质**：它是"提高门槛"，**不是安全边界** ✗
// 客户端改一下就能报别的数字，服务器**没法验证**（它看不到那个端口 ✓）
// ⇒ 档的是"不知情 / 懒得改"的人 ✓
//
// 📌 那为什么还要做：客户端那道是"防君子"（改本地一个常量就绕了），
// 这道至少要求"改客户端" ⇒ 门槛高一点 ✓ 详见 proto.Msg.GamePort 的说明 ✓
func TestValidateRegisterGamePort(t *testing.T) {
	base := func() proto.Msg {
		return proto.Msg{
			Type:     proto.TypeRegister,
			Room:     "TI123456AB",
			ClientID: "client-abc",
			GamePort: 25565,
		}
	}

	// ① 合法端口 ⇒ 过 ✓（我们的游戏都在 1024 以上 ✓）
	for _, p := range []int{25565, 7777, 24642, 19132, 54321} {
		m := base()
		m.GamePort = p
		if got := validateRegister(m); got != "" {
			t.Errorf("端口 %d 该放行，却被拒：%s", p, got)
		}
	}

	// ② 敏感端口 ⇒ 拒 ✓
	for _, p := range []int{80, 443, 22, 3389, 3306, 6379, 27017, 1080} {
		m := base()
		m.GamePort = p
		if got := validateRegister(m); got == "" {
			t.Errorf("端口 %d 是敏感端口，该被拒 ✗", p)
		}
	}

	// ③ **0 = 没报** ⇒ 不校验 ✓
	// （老式客户端 / 玩家侧不发这个字段 ✓ 不能因此把人挡在外面 ✗）
	m := base()
	m.GamePort = 0
	if got := validateRegister(m); got != "" {
		t.Errorf("没报端口（0）该放行，却被拒：%s", got)
	}
}

// 📌 别把这条校验**塞到不该管的地方**去：
// 玩家侧（role=join）压根不需要报端口 —— 他连的是自己机器上的回环地址，
// 那个"端口"是我们自己开的隧道，不是他游戏在听的东西 ✓
//
// 这条只是把"现在允许报 0"钉住，免得以后有人改成"必填"把老客户端挡了 ✗
func TestJoinRoleDoesNotNeedGamePort(t *testing.T) {
	m := proto.Msg{
		Type: proto.TypeRegister,
		Role: "join", Game: "我的世界",
		Room:     "TI123456AB",
		ClientID: "client-xyz",
		// 玩家侧不发 GamePort ✓
	}
	if got := validateRegister(m); got != "" {
		t.Errorf("玩家侧不报端口该放行，却被拒：%s", got)
	}
}

// ⭐ 第二路端口（GamePort2）必须走**同一条**校验。
//
// 🩸 这条测试是给"以后加端口字段"的人看的：
//
//	新加的端口字段如果没接 CheckPort，就等于**开历史倒车** ——
//	房主报个 3389 就能把隧道打向远程桌面 ✗
//	反证：把 validate.go 里 GamePort2 那个 case 删掉 ⇒ 立刻红 ✓
func TestValidateRegisterGamePort2(t *testing.T) {
	base := func() proto.Msg {
		return proto.Msg{
			Type:      proto.TypeRegister,
			Room:      "TI123456AB",
			ClientID:  "client-abc",
			Game:      "饥荒联机版", // 白名单里的游戏
			GamePort:  10999,
			GamePort2: 10998, // 饥荒：主世界 10999 + 洞穴 10998
		}
	}

	// ① 合法 ⇒ 过
	if msg := validateRegister(base()); msg != "" {
		t.Fatalf("合法端口被拒：%s", msg)
	}

	// ② 0 = 没报 ⇒ 不校验（老客户端 / 只有一路的游戏）
	m := base()
	m.GamePort2 = 0
	if msg := validateRegister(m); msg != "" {
		t.Fatalf("0 应该表示没报、不校验，却被拒：%s", msg)
	}

	// ③ 🩸 敏感端口一律拒 —— 这是"不能开倒车"的底线
	for _, p := range []int{3389, 3306, 5432, 22, 80, 443, 6379} {
		m := base()
		m.GamePort2 = p
		if msg := validateRegister(m); msg == "" {
			t.Errorf("第二路端口 %d 竟然通过了 ✗ 隧道能被打向系统服务", p)
		}
	}
}

// ⭐ 两路端口不能相同 —— 相同的话第二条管子连的是同一个游戏端口，纯属白开。
func TestValidateRegisterGamePort2SameAsMain(t *testing.T) {
	m := proto.Msg{
		Type:      proto.TypeRegister,
		Room:      "TI123456AB",
		ClientID:  "client-abc",
		Game:      "饥荒联机版",
		GamePort:  10999,
		GamePort2: 10999, // 跟主端口一样 ⇒ 该拒
	}
	if msg := validateRegister(m); msg == "" {
		t.Fatal("两路端口相同竟然通过了 ✗ 会白开一路")
	}
}

// ⭐⭐ 白名单：**只有确实占两个端口的游戏**才能报第二路。
//
// 🩸 为什么要有这条：不加的话，任何游戏都能报个 GamePort2 多开一路 ——
//
//	等于白送一个"多一条隧道"的口子 ✗
//
// 反证：把 validate.go 里 !game.IsDualPort(m.Game) 那个 case 删掉 ⇒ 立刻红 ✓
func TestValidateRegisterGamePort2NeedsWhitelist(t *testing.T) {
	// 白名单里的 ✅
	m := proto.Msg{
		Type: proto.TypeRegister, Room: "TI123456AB", ClientID: "c1",
		Game: "饥荒联机版", GamePort: 10999, GamePort2: 10998,
	}
	if msg := validateRegister(m); msg != "" {
		t.Fatalf("饥荒该能报第二路，却被拒：%s", msg)
	}

	// 白名单里的第二个：方舟（7777 + 7778）✅
	m2 := proto.Msg{
		Type: proto.TypeRegister, Room: "TI123456AB", ClientID: "c3",
		Game: "方舟：生存进化", GamePort: 7777, GamePort2: 7778,
	}
	if msg := validateRegister(m2); msg != "" {
		t.Fatalf("方舟该能报第二路，却被拒：%s", msg)
	}

	// 白名单里的第三个：方舟飞升（也是 7777 + 7778）✅
	m3 := proto.Msg{
		Type: proto.TypeRegister, Room: "TI123456AB", ClientID: "c4",
		Game: "方舟：生存飞升", GamePort: 7777, GamePort2: 7778,
	}
	if msg := validateRegister(m3); msg != "" {
		t.Fatalf("方舟飞升该能报第二路，却被拒：%s", msg)
	}

	// 白名单里的第四个：七日杀（26900 + 26901）✅
	m4 := proto.Msg{
		Type: proto.TypeRegister, Room: "TI123456AB", ClientID: "c5",
		Game: "七日杀", GamePort: 26900, GamePort2: 26901,
	}
	if msg := validateRegister(m4); msg != "" {
		t.Fatalf("七日杀该能报第二路，却被拒：%s", msg)
	}

	// 白名单里的第五个：夜族崛起（27015 + 27016）✅
	m5 := proto.Msg{
		Type: proto.TypeRegister, Room: "TI123456AB", ClientID: "c6",
		Game: "夜族崛起", GamePort: 27015, GamePort2: 27016,
	}
	if msg := validateRegister(m5); msg != "" {
		t.Fatalf("夜族崛起该能报第二路，却被拒：%s", msg)
	}

	// 不在白名单的 ❌（这几款都只占一个端口）
	for _, name := range []string{"我的世界", "泰拉瑞亚", "星露谷物语", "生存战争", "", "随便编一个"} {
		m := proto.Msg{
			Type: proto.TypeRegister, Room: "TI123456AB", ClientID: "c2",
			Game: name, GamePort: 10999, GamePort2: 10998,
		}
		if msg := validateRegister(m); msg == "" {
			t.Errorf("游戏 %q 不是双端口游戏，却报通了第二路 ✗", name)
		}
	}
}
