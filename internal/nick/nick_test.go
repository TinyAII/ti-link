package nick

import (
	"encoding/json"
	"strings"
	"testing"
)

// 摇出来的名字上界（12 字），跟 cmd/signal 的 maxLabelLen 是同一个数。
const maxLabelRunes = 12

func maxRunes(list []string) int {
	mx := 0
	for _, w := range list {
		if n := len([]rune(w)); n > mx {
			mx = n
		}
	}
	return mx
}

// 词库的基本体检：每张表都得够用、不重复、且不能混进「的」。
func TestWordListsAreSane(t *testing.T) {
	lists := []struct {
		key  string
		list []string
	}{
		{"mood", mood}, {"deed", deed}, {"role", role}, {"name", name},
	}
	for _, l := range lists {
		if len(l.list) < 50 {
			t.Errorf("%s 只有 %d 个词（少于 50）—— 摇起来会翻来覆去就那几个", l.key, len(l.list))
		}
		seen := map[string]bool{}
		for _, w := range l.list {
			switch {
			case w == "":
				t.Errorf("%s 里有空词", l.key)
			case strings.Contains(w, separator):
				// Valid() 靠「的」切分；词里混进「的」会让合法名字被判非法
				t.Errorf("%s 里的 %q 含「%s」，会把 Valid 的切分点搞乱", l.key, w, separator)
			case seen[w]:
				t.Errorf("%s 里的 %q 重复了", l.key, w)
			}
			seen[w] = true
		}
	}
}

// 最长组合必须卡在昵称上限以内 —— 这条是"往某个槽里塞长词"最容易顶穿的。
func TestLongestNameFitsLimit(t *testing.T) {
	longest := maxRunes(mood) + maxRunes(deed) + len([]rune(separator)) + maxRunes(role) + maxRunes(name)
	if longest > maxLabelRunes {
		t.Errorf("最长名字 %d 字，超过上限 %d —— 哪个槽里塞了更长的词？", longest, maxLabelRunes)
	}
	t.Logf("最长组合 %d 字（上限 %d，余量 %d）", longest, maxLabelRunes, maxLabelRunes-longest)
}

func TestValid(t *testing.T) {
	cases := []struct {
		label string
		want  bool
		why   string
	}{
		{mood[0] + deed[0] + separator + role[0] + name[0], true, "拿每张表第一个词拼的"},
		{"最爱追星的门客豆沙包", true, "聊天里出现过的那个（四个词都取自词库）"},
		{"喜欢捣蛋的雇员沐恩", true, "注释里用了很久的样例"},
		{"", false, "空串（调用方先判空，不当名字看）"},
		{"张三", false, "没有「的」，拼不出来"},
		{"小明", false, "自由输入的名字"},
		{"喜欢捣蛋的雇员", false, "缺了名字那一截"},
		{"喜欢捣蛋", false, "只有前半截"},
		{"喜欢捣蛋的雇员沐恩沐恩", false, "尾巴不是库里的词"},
		{"喜欢捣蛋的雇员沐恩的", false, "多了一个「的」"},
		{"喜欢 捣蛋的雇员沐恩", false, "词里混了空格"},
		{"喜欢捣蛋的雇员沐", false, "尾巴少一个字"},
		{"美国捣蛋的雇员沐恩", false, "态度那一截词库里没有"},
	}
	for _, c := range cases {
		if got := Valid(c.label); got != c.want {
			t.Errorf("Valid(%q) = %v，该是 %v（%s）", c.label, got, c.want, c.why)
		}
	}
}

// 反向：随便从四张表里各取一个词拼出来的名字，必须**都**判合法。
//
// 这条防的是 Valid 写得太紧 —— 把正常名字也挡在外面才是真灾难：
// 用户会发现自己摇的名字进不去房间，而且完全不知道自己哪里做错了 ✗
func TestEveryRolledNameIsValid(t *testing.T) {
	for i := 0; i < 200; i++ {
		l := mood[i%len(mood)] + deed[(i*7)%len(deed)] + separator +
			role[(i*13)%len(role)] + name[(i*29)%len(name)]
		if !Valid(l) {
			t.Fatalf("词库里拼出来的名字被判非法：%q", l)
		}
	}
}

func TestWordsJSONRoundTrip(t *testing.T) {
	var w Words
	if err := json.Unmarshal([]byte(WordsJSON()), &w); err != nil {
		t.Fatalf("词库 JSON 解不回来：%v", err)
	}
	if len(w.Mood) != len(mood) || len(w.Deed) != len(deed) ||
		len(w.Role) != len(role) || len(w.Name) != len(name) {
		t.Errorf("JSON 里的词数和 Go 侧对不上：%d/%d/%d/%d",
			len(w.Mood), len(w.Deed), len(w.Role), len(w.Name))
	}
	// 前端是拿它当**对象字面量**直接塞进页面的（不是 JSON.parse），
	// 所以键名必须是裸的 mood/deed/role/name —— 改了前端就摇不出名字。
	for _, key := range []string{`"mood"`, `"deed"`, `"role"`, `"name"`} {
		if !strings.Contains(WordsJSON(), key) {
			t.Errorf("词库 JSON 里缺 %s 这个键 —— 前端 NICK_WORDS 就靠它", key)
		}
	}
}

// Sample 自己必须过 Valid —— 它要是拼不出合法名字，探针和测试就全乱套了
// （两边用的是同一套拼法，这条就是钉它们没跑偏）。
func TestSampleIsValid(t *testing.T) {
	if s := Sample(); !Valid(s) {
		t.Errorf("Sample() = %q，却过不了 Valid —— 两边逻辑对不上了", s)
	}
}
