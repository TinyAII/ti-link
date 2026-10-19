package game

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// 🩸 **Go 的清单和前端 JS 的必须一致** —— 这条测试就是盯着它们的。
//
// 为什么要有它：进程名现在写在两个地方
//
//	Go   的 internal/game/game.go   ← 真正干活的那个（GUI 和命令行都用它）
//	前端 的 cmd/gui/ui/js/01-core.js ← 界面显示用
//
// 分叉的表现很隐蔽：**"GUI 上的灯认得出、命令行认不出"**，或者反过来 ✗
// 用户看到的是"有时候能建、有时候不能"，根本猜不到原因。
// （跟昵称词库那次一个道理：分叉的表现是"客户端摇得出、服务器不收" ✓）
//
// 📌 所以加了游戏之后，两边都得改；只改一边这条会红 ✓
func TestGameListMatchesFrontend(t *testing.T) {
	jsPath := "../../cmd/gui/ui/js/01-core.js"
	raw, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("读前端 GAMES 失败（%s）：%v", jsPath, err)
	}
	js := string(raw)

	// 从 JS 里把 "name: 'xx' ... procMatch: ['a','b']" 抽出来。
	// 用正则够用 —— 这儿只需要"能发现差异"，不需要完整解析 JS ✓
	nameRe := regexp.MustCompile(`name:\s*'([^']+)'`)
	procRe := regexp.MustCompile(`procMatch:\s*\[([^\]]*)\]`)

	for i, g := range All {
		// ── 这个名字在 JS 里得存在 ──
		idx := strings.Index(js, "name: '"+g.Name+"'")
		if idx < 0 {
			t.Errorf("Go 里有「%s」，前端 JS 里没有 ✗（两边得一起改）", g.Name)
			continue
		}

		// ── 取这个名字**之后**到下一个 name 之前的那一段，找 procMatch ──
		rest := js[idx:]
		if next := nameRe.FindStringIndex(rest[len("name: '"):]); next != nil {
			off := len("name: '") + next[0]
			if off > 0 && off < len(rest) {
				rest = rest[:off]
			}
		}

		m := procRe.FindStringSubmatch(rest)
		var jsKeys []string
		if m != nil {
			for _, part := range strings.Split(m[1], ",") {
				part = strings.TrimSpace(part)
				part = strings.Trim(part, `'"`)
				if part != "" {
					jsKeys = append(jsKeys, part)
				}
			}
		}

		if !sameKeys(g.ProcMatch, jsKeys) {
			t.Errorf("「%s」的进程名对不上 ✗\n  Go  ：%v\n  前端：%v\n"+
				"  ⇒ 分叉的话会出现「一个认得出、一个认不出」，两边得一起改 ✓",
				g.Name, g.ProcMatch, jsKeys)
		}
		_ = i
	}

	// ── 反过来：JS 里的每个游戏，Go 里也得有 ──
	for _, m := range nameRe.FindAllStringSubmatch(js, -1) {
		name := m[1]
		if Find(name) == nil {
			t.Errorf("前端有「%s」，Go 的清单里没有 ✗（加游戏时两边都得加）", name)
		}
	}
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}
