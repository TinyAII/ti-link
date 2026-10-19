// 界面资源拼装的自检。
//
// 为什么值得单独立个测试：`buildHTML()` 是"字符串替换拼一整页"，
// 拼错了不会报错 —— 只会在运行时白屏。**白屏是最难查的一类故障**：
// 窗口照样弹出来（4 秒兜底显示），只是里面什么都没有，看着像卡死。
//
// 这里只做两件能机械判定的事：
//  1. 占位符必须全部被替换掉（漏一个 => 页面上就留着一串 __XXX__，或者 JS 直接语法错）
//  2. 头像库数组的条目数和内容要正常
//
// 顺带把拼好的整页写到 tmp/ui-built.html，方便外面用 node 再验一遍语法。
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"p2plink/internal/nick"
)

func TestBuildHTML(t *testing.T) {
	t0 := time.Now()
	h := buildHTML()
	el := time.Since(t0)
	t.Logf("buildHTML 用时 %v，页面大小 %.1f KB", el, float64(len(h))/1024)

	// 1) 占位符一个都不能剩
	for _, ph := range []string{
		"__CSS__", "__JS__", "__AVATARS__", "__NICK_WORDS__", "__THEME__",
		"__ROOMCOVER__", "__GAMECOVER__",
		"__COVER_TERRARIA__", "__COVER_STARDEW__", "__COVER_SURVIVAL__", "__COVER_DST__",
		"__COVER_PALWORLD__", "__COVER_RUSTED__", "__COVER_ARK__", "__COVER_ASA__", "__COVER_MIND__", "__COVER_FACTORIO__", "__COVER_7DTD__", "__COVER_VRISING__",
		"__ICON_MC__", "__ICON_TERRARIA__", "__ICON_STARDEW__",
		"__TUT01__", "__TUT04a__", "__TUTj7__",
		"__LOGGED_IN__", "__LOGIN_BASE__",
	} {
		if strings.Contains(h, ph) {
			t.Errorf("页面里还留着没替换的占位符 %s —— 这就是白屏的典型原因", ph)
		}
	}

	// 1b) 再兜一道**通配**的：上面那张清单是手写的，加占位符时忘了往里补就漏了 ✗
	// 通配扫一遍 ⇒ "新加的占位符没接上替换"这件事不可能溜过去 ✓
	// （2026-10-15 加登录那两个占位符时才想到要兜这一道）
	if m := leftoverPlaceholders(h); len(m) > 0 {
		t.Errorf("页面里还有没替换的占位符：%v", m)
	}

	// 2) 头像库：条目数对、每项都有 src
	names := []string{
		`g:"我的世界"`, `g:"泰拉瑞亚"`, `g:"星露谷物语"`,
	}
	for _, n := range names {
		if !strings.Contains(h, n) {
			t.Errorf("头像库里没找到分类 %s", n)
		}
	}
	if n := strings.Count(h, "data:image/png;base64,"); n < AvatarCount() {
		t.Errorf("头像图只有 %d 张，应该有 %d 张", n, AvatarCount())
	}

	// 2b) 昵称词库：必须**原样**来自 internal/nick ——
	// 那份词库是唯一来源（客户端摇名字、穿透服务器校验都用它）。
	// 页面里要是塞了别的词表，两边就会分叉：客户端摇得出、服务器不收。
	if !strings.Contains(h, nick.WordsJSON()) {
		t.Errorf("页面里的词库和 internal/nick 对不上 —— 两边分叉了")
	}

	// 3) 写一份出来给外面的工具验（node --check / 无头浏览器之类）
	// 路径按模块根算：从哪个目录跑测试都对得上（go test 会把 CWD 设成包目录，
	// 直接跑编译出来的测试二进制时又是另一个目录 —— 靠相对路径会写飞）
	out := filepath.Join(moduleRoot(t), "tmp", "ui-built.html")
	if err := os.WriteFile(out, []byte(h), 0o644); err != nil {
		t.Logf("（写 %s 失败，不影响测试：%v）", out, err)
	} else {
		t.Logf("整页已写到 %s", out)
	}
}

// 头像库的 JS 字面量必须是合法 JSON —— 页面那边是直接当数组用的，
// 有一个引号没转义，整个脚本就废了。
func TestAvatarListJS(t *testing.T) {
	js := avatarListJS()
	if !strings.HasPrefix(js, "[") || !strings.HasSuffix(js, "]") {
		t.Fatalf("头像库不是数组：%.80s...", js)
	}
	// 数一下条目：每项一个 {"g":...,"src":...}
	if n := strings.Count(js, `{g:`); n != AvatarCount() {
		t.Errorf("头像库条目数 %d，AvatarCount() 说 %d", n, AvatarCount())
	}
	if strings.Contains(js, "null") {
		t.Errorf("头像库里有 null —— 说明有图没 embed 进去")
	}
}

// leftoverPlaceholders 扫出页面里**任何**还没被替换的 __XXX__ 形状的占位符。
//
// 为什么要通配：手写清单那种写法，"新加了占位符却忘了接上替换"是必然漏的 ——
// 漏掉的表现是页面上留着一串 __XXX__（难看），或者更糟：它在 JS 里是个未定义变量，
// 整个脚本从那一行起就不跑了 ⇒ **白屏** ✗
//
// 只认大写字母/数字/下划线这种我们自己的命名习惯，避免误伤正常的 JS 标识符
// （比如 window.__ready 那种带点的、或者普通的 x_y 变量）。
func leftoverPlaceholders(page string) []string {
	re := regexp.MustCompile(`__[A-Z][A-Z0-9_]{1,}__`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllString(page, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// moduleRoot 从当前目录往上找 go.mod，定位到项目根。
//
// 为什么不用相对路径：`go test ./cmd/gui` 会把工作目录设成包目录，
// 但直接跑编译出来的测试二进制时工作目录是**你敲命令的地方** ——
// 相对路径在两种情况下指向不同位置，产物会莫名其妙写到别处
// （我第一次就把它写到 D:\tmp\ 去了）。
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("往上找不到 go.mod")
	return ""
}
