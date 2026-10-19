package netutil

import "testing"

// ⭐ 最要紧的一条：**我们的游戏端口一个都不能被误伤** ✓
//
// 这条要是红了，正常玩家就建不了房 —— 而他们完全不知道自己做错了什么 ✗
// （2026-10-15：差点把 8080 也列进高危表 ⇒ 有些游戏的 mod 服会用它的 ✓
//
//	宁可少禁几个，也不能误伤 ✓）
func TestOurGamePortsAllAllowed(t *testing.T) {
	allowed := []struct {
		port int
		why  string
	}{
		{25565, "我的世界 Java 版默认"},
		{7777, "泰拉瑞亚默认"},
		{24642, "星露谷物语默认"},
		{25565, "生存战争（手动填端口，默认也走这个）"},
		{19132, "我的世界基岩版（以后可能会加）"},
		{27015, "泰拉瑞亚查询端口 / Steam"},
		{1024, "1–1023 之外的**下边界**"},
		{65535, "最大合法端口"},
		{50000, "我的世界「对局域网开放」的随机端口就在这一带"},
		{54321, "随机高端口的另一个样"},
	}
	for _, c := range allowed {
		if msg := CheckPort(c.port); msg != "" {
			t.Errorf("端口 %d（%s）该放行，却被拒了 ✗\n  理由：%s", c.port, c.why, msg)
		}
	}
}

func TestLowPortsRejected(t *testing.T) {
	// 1–1023 一律不放行 ✓
	for _, p := range []int{1, 21, 22, 23, 25, 53, 80, 110, 143, 443, 465, 993, 995, 1023} {
		if msg := CheckPort(p); msg == "" {
			t.Errorf("端口 %d 在 1–1023 里，该被拒 ✗", p)
		}
	}
}

func TestRiskyPortsRejected(t *testing.T) {
	// 这几个都在 1024 以上，得靠**高危表**才拦得住 ✓
	// 🩸 别以为"禁了 1–1023 就完事" —— RDP 3389、MySQL 3306 都是高端口 ✗
	for _, p := range []int{3389, 5900, 5901, 445, 139, 2049,
		1433, 3306, 5432, 6379, 27017, 9200, 11211, 1080, 3128, 2375, 2376} {
		if msg := CheckPort(p); msg == "" {
			t.Errorf("端口 %d 在高危表里，该被拒 ✗", p)
		}
	}
}

func TestIllegalPorts(t *testing.T) {
	for _, p := range []int{0, -1, 65536, 99999} {
		if msg := CheckPort(p); msg == "" {
			t.Errorf("端口 %d 根本不合法，该被拒 ✗", p)
		}
	}
}

// 拒掉的理由得**说人话** —— 用户看到"端口 80 是 HTTP 的默认端口"才会明白 ✓
func TestRejectReasonMentionsTheService(t *testing.T) {
	for _, c := range []struct {
		port int
		want string
	}{
		{80, "HTTP"},
		{443, "HTTPS"},
		{22, "SSH"},
		{3389, "远程桌面"},
		{3306, "MySQL"},
	} {
		msg := CheckPort(c.port)
		if !containsStr(msg, c.want) {
			t.Errorf("端口 %d 的拒绝理由该提到 %q，实得：%s", c.port, c.want, msg)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// PortOf 是取端口的**唯一**实现（game.AddrPort 也转给它 ✓）
func TestPortOf(t *testing.T) {
	ok := []struct {
		in   string
		want int
	}{
		{"127.0.0.1:24642", 24642},
		{"0.0.0.0:7777", 7777},
		{"[::1]:25565", 25565},
		{"example.com:19132", 19132},
	}
	for _, c := range ok {
		got, err := PortOf(c.in)
		if err != nil || got != c.want {
			t.Errorf("PortOf(%q) = %d, %v；该是 %d", c.in, got, err, c.want)
		}
	}

	for _, bad := range []string{"", "127.0.0.1", "127.0.0.1:", "127.0.0.1:abc",
		"127.0.0.1:0", "127.0.0.1:70000", "127.0.0.1:-1"} {
		if p, err := PortOf(bad); err == nil {
			t.Errorf("PortOf(%q) 该报错，却给了 %d ✗", bad, p)
		}
	}
}

// 📌 反过来的：**这些"看着像高风险但其实不是"的端口得放行** ✓
// 免得以后有人"顺手多禁几个"把正常游戏也禁了 ✗
func TestNotOverzealous(t *testing.T) {
	// 🩸 8080 不在这里 —— 2026-10-17 改成拉黑了（Web 面板太常用），
	//    见 ports.go 的说明和 TestWebPanelPortsBlocked ✓
	for _, p := range []int{8443, 5000, 3000, 8000} {
		if msg := CheckPort(p); msg != "" {
			t.Errorf("端口 %d 不该被禁（有些开发/游戏场景会用）✗ 理由：%s", p, msg)
		}
	}
}

// ⭐ 我们支持的每个游戏的主端口，都必须能过黑名单。
//
// 🩸 这条是给"以后往 riskyPorts 里加东西"的人看的：
//
//	如果哪天手滑把 8211 / 10999 这类加进去，房主就**建不了房**，而且报错很不直观 ✗
//	加游戏时顺手把新端口加到这个列表里 ✓
func TestSupportedGamePortsPass(t *testing.T) {
	for _, p := range []int{
		25565, // 我的世界
		7777,  // 泰拉瑞亚
		24642, // 星露谷物语
		4234,  // 生存战争（TCP）
		28887, // 生存战争（UDP）
		10999, // 饥荒主世界
		10998, // 饥荒洞穴
		8211,  // 幻兽帕鲁
		5123,  // 铁锈战争
		7778,  // 方舟（Raw UDP，7777+1）
		6567,  // 像素工厂
		34197, // 异星工厂
		26900, // 七日杀主端口
		26901, // 七日杀辅助（第二路）
		27015, // 夜族崛起主端口
		27016, // 夜族崛起查询端口
	} {
		if msg := CheckPort(p); msg != "" {
			t.Errorf("端口 %d 被拦了：%s ✗ 这是我们要支持的游戏端口", p, msg)
		}
	}
}

// ⭐ Web 管理面板那三个端口必须拉黑。
//
// 🩸 8080-8082 曾经是"刻意放过"的（有些 mod 服会用），2026-10-17 改成拉黑：
//
//	理由是我们**管不了 mod 服这么宽** —— 那是极少数、且能自己换端口；
//	而 8080/8081 是 Web 面板最常用的号，放开等于把隧道的用途打开了口子 ✗
func TestWebPanelPortsBlocked(t *testing.T) {
	for _, p := range []int{8080, 8081, 8082, 25575, 27020, 9878} {
		if msg := CheckPort(p); msg == "" {
			t.Errorf("端口 %d 该被拦（Web 面板常用），却放行了 ✗", p)
		}
	}
	// 这几个必须**仍然放行**（我们的游戏端口，别误伤）
	for _, p := range []int{25565, 7777, 24642, 4234, 8211, 5123, 6567, 34197, 26900} {
		if msg := CheckPort(p); msg != "" {
			t.Errorf("游戏端口 %d 被误拦了：%s ✗", p, msg)
		}
	}
}
