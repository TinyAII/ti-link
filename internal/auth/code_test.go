package auth

import (
	"strings"
	"testing"
)

// 🩸🩸 字母表的形状是**跨端约定**（码要念给人听、手抄到客户端），
// 改一个字，已经发出去的码就全部对不上了。
//
// 这条测试就是拦"顺手往字母表里加个字符"的人。
func TestAlphabetShape(t *testing.T) {
	if len(codeAlphabet) != 32 {
		t.Fatalf("字母表必须是 32 个字符（32 整除 256 ⇒ 取随机字节不会有取模偏置），实得 %d", len(codeAlphabet))
	}
	seen := map[rune]bool{}
	for _, r := range codeAlphabet {
		if seen[r] {
			t.Errorf("字母表里 %q 重复了", r)
		}
		seen[r] = true
	}
	// Crockford 特意去掉的四个：I L O U
	// 前三个是"跟 1、0 看错"，U 是"能拼出脏话"
	for _, bad := range []rune{'I', 'L', 'O', 'U'} {
		if seen[bad] {
			t.Errorf("字母表里不该有 %q（Crockford Base32 特意去掉的）✗", bad)
		}
	}
	// 也不该有小写或别的
	for _, r := range codeAlphabet {
		if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z') {
			t.Errorf("字母表里有非法字符 %q", r)
		}
	}
}

func TestNewCode(t *testing.T) {
	for i := 0; i < 200; i++ {
		c, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != CodeLen {
			t.Fatalf("码长度该是 %d，实得 %d：%q", CodeLen, len(c), c)
		}
		for _, r := range c {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("码里出现了字母表外的字符 %q：%q", r, c)
			}
		}
	}
}

// 重复率：2^50 的空间里抽 5000 个，撞上才是见鬼了。
// 这条红了基本只有一个原因 —— 生成器退化成了可预测的东西（比如用了 math/rand 且种子固定）。
func TestNewCode_NoDuplicates(t *testing.T) {
	const n = 5000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		c, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if seen[c] {
			t.Fatalf("第 %d 个码就撞了：%q", i, c)
		}
		seen[c] = true
	}
}

func TestNormalizeCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abcd efgh", "ABCDEFGH"},
		{"ABCDE-FGHJK", "ABCDEFGHJK"},
		{"  abcde-fghjk  ", "ABCDEFGHJK"},
		// Crockford 的纠错映射：O→0、I/L→1
		{"OOOOOIIIII", "0000011111"},
		{"LLL", "111"},
		// 别的字符一律丢掉
		{"AB！@#CD", "ABCD"},
		{"abc中文def", "ABCDEF"},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeCode(c.in); got != c.want {
			t.Errorf("NormalizeCode(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}
}

// ⭐ 纠错映射必须**不会让两个不同的码撞到一起**。
// 前提是规范码里根本没有 O/I/L —— 所以随便挑两个规范码，
// 把它们「用看起来像的样子打出来」（把 0 写成 O、1 写成 I 或 L），
// 规范化之后必须还回原码。
func TestNormalizeCode_RoundTripFromLipography(t *testing.T) {
	for i := 0; i < 300; i++ {
		c, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		// 模拟"看错了照着打"：0 打成 O、1 打成 I 或 L；再全小写、加空格和连字符
		typo := strings.Map(func(r rune) rune {
			switch r {
			case '0':
				return 'O'
			case '1':
				return 'l'
			}
			return r
		}, c)
		typo = strings.ToLower(typo)
		var sb strings.Builder
		for j, r := range typo {
			if j > 0 && j%5 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteRune(r)
		}
		if got := NormalizeCode(sb.String()); got != c {
			t.Fatalf("把 %q 按易混写法打成 %q 之后，该还原成 %q，实得 %q",
				c, sb.String(), c, got)
		}
	}
}

func TestFormatCode(t *testing.T) {
	if got := FormatCode("ABCDEFGHJK"); got != "ABCDE-FGHJK" {
		t.Errorf("FormatCode = %q", got)
	}
	// 长度不对就原样返回，别乱插连字符
	if got := FormatCode("ABC"); got != "ABC" {
		t.Errorf("FormatCode(短) = %q", got)
	}
}

// 📌 给人看的形式和存的形式要能互相还原 —— 后台导出的是带连字符的，
// 用户再粘回来必须能对上。
func TestFormatCode_RoundTrip(t *testing.T) {
	for i := 0; i < 200; i++ {
		c, _ := NewCode()
		pretty := FormatCode(c)
		if NormalizeCode(pretty) != c {
			t.Fatalf("%q → 显示成 %q → 规范化回来对不上", c, pretty)
		}
	}
}
