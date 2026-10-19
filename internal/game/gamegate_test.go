package game

import (
	"net"
	"os"
	"testing"
)

// 「状态灯」那条判断 —— 走 `VerdictFor`。
//
// 🩸 它是**显示**用的，不是准入 —— 曾经有过的"建房前拦一下"已经去掉了，
// 理由见 cmd/gui/backend.go 里 `CreateRoom` 上方那段 ✓
//
// 📌 玩法：拿**测试进程自己**当"占着端口的那个程序" ✓
func TestVerdictForRejectsOtherProgram(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// ① 端口上**不是**泰拉瑞亚（是我们这个测试进程）⇒ 该**确定地**说"不是它" ✓
	v := VerdictFor(port, "泰拉瑞亚")
	if !v.Known {
		t.Fatal("进程名读得到，该能给确定结论")
	}
	if v.Game != "" {
		t.Errorf("测试进程不该被认成泰拉瑞亚，实得 %q", v.Game)
	}

	// ② 没配进程名的游戏（我们不知道它该叫啥）⇒ **说不准** ⇒ 上层会放行 ✓
	// 🩸 不能因为"我们没资料"就拦人 —— 那是我们的问题，不是用户的 ✗
	v = VerdictFor(port, "生存战争")
	if v.Known {
		t.Error("没配进程名的游戏该是「说不准」，不该下结论 ✗")
	}

	// ③ 压根不存在的游戏名 ⇒ 同样说不准（没资格判断）✓
	v = VerdictFor(port, "根本没这个游戏")
	if v.Known {
		t.Error("不存在的游戏该是「说不准」✗")
	}

	// ④ 端口上**没人** ⇒ 确定"不是游戏"（游戏根本没开）✓
	ln.Close()
	v = VerdictFor(port, "泰拉瑞亚")
	if !v.Known || v.Game != "" {
		t.Errorf("端口上没人时该是 确定/不是游戏，实得 Known=%v Game=%q", v.Known, v.Game)
	}

	// ⑤ 端口号不合法 ⇒ 说不准（这没法判成"不是游戏"）✓
	if v := VerdictFor(0, "泰拉瑞亚"); v.Known {
		t.Error("端口号非法时该是「说不准」✗")
	}
}

// 📌 进程名归一化：带空格 / 全路径 / 下划线都得归一成同一个 key ✓
// 不然会出现"灯认得出、校验认不出"的怪事 ✗
func TestNameKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Stardew Valley.exe", "stardewvalley"},
		{"stardewvalley", "stardewvalley"},
		{`C:\Games\Terraria.exe`, "terraria"},
		{"/usr/bin/terraria", "terraria"},
		{"SomeMod_Loader.exe", "somemodloader"},
		{"Stardew Valley (modded).exe", "stardewvalley(modded)"},
	}
	for _, c := range cases {
		if got := NameKey(c.in); got != c.want {
			t.Errorf("NameKey(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}
}

// 关键词匹配要**宽松**（能对上多种写法），但不能太松（不该命中无关进程）✓
func TestNameMatchesAny(t *testing.T) {
	// "stardew" 该同时对上这几种写法 ✓
	for _, p := range []string{
		"Stardew Valley.exe", "StardewValley.exe", "STARDEW VALLEY.EXE",
		`D:\Steam\steamapps\common\Stardew Valley\Stardew Valley.exe`,
	} {
		if !NameMatchesAny(p, []string{"stardew"}) {
			t.Errorf("%q 该被 stardew 命中 ✗", p)
		}
	}
	// 无关进程不该命中 ✓
	for _, p := range []string{"nginx.exe", "javaw.exe", "explorer.exe"} {
		if NameMatchesAny(p, []string{"stardew", "terraria"}) {
			t.Errorf("%q 不该被命中 ✗", p)
		}
	}
}

// 清单本身的完整性 ✓
func TestGameList(t *testing.T) {
	if len(All) == 0 {
		t.Fatal("清单空的")
	}
	seen := map[string]bool{}
	for _, g := range All {
		if g.Name == "" {
			t.Error("有个游戏没写名字 ✗")
		}
		if seen[g.Name] {
			t.Errorf("游戏名重复：%q ✗", g.Name)
		}
		seen[g.Name] = true

		// 关键词归一化之后不能是空的 —— 空关键词会让匹配形同虚设 ✗
		for _, m := range g.ProcMatch {
			if NameKey(m) == "" {
				t.Errorf("%s 的关键词 %q 归一化之后是空的 ✗", g.Name, m)
			}
		}
	}
}

// 用 os.Executable 确认 processPathsOnPort 真的能读到进程名 ✓
// （读不到就说明权限或 API 有问题，上面那些测试都会因此失去意义 ✓）
func TestProcessPathsOnPortReadsName(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	paths, readNames, err := processPathsOnPort(port)
	if err != nil {
		t.Fatalf("查端口占用失败: %v", err)
	}
	if len(paths) == 0 || readNames == 0 {
		t.Fatal("端口明明在听，却一个进程名都没读出来")
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("取自己的路径失败: %v", err)
	}
	selfKey := NameKey(self)
	if selfKey == "" {
		t.Fatal("自己的进程名关键词是空的")
	}
	found := false
	for _, p := range paths {
		if NameMatchesAny(p, []string{selfKey}) {
			found = true
		}
	}
	if !found {
		t.Errorf("该认出是自己占着这个端口（关键词 %q），实得 %v", selfKey, paths)
	}
}
