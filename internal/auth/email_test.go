package auth

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Bob@QQ.com", "bob@qq.com"},
		{"  bob@qq.com  ", "bob@qq.com"},
		{"BOB@QQ.COM", "bob@qq.com"},
	}
	for _, c := range cases {
		if got := NormalizeEmail(c.in); got != c.want {
			t.Errorf("NormalizeEmail(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}
}

// 📌 取**最后一个** @ 才是域名 —— RFC 允许本地部分出现引号包裹的 @。
func TestEmailDomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"bob@qq.com", "qq.com"},
		{`"a@b"@qq.com`, "qq.com"},
		{"bob@mail.qq.com", "mail.qq.com"},
		{"bob", ""},
		{"bob@", ""},
	}
	for _, c := range cases {
		if got := EmailDomain(c.in); got != c.want {
			t.Errorf("EmailDomain(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}
}

// 🩸🩸 反证测试：白名单**必须是整体相等，不能是后缀匹配**。
//
// 写成 HasSuffix 的话，`任意@qq.com.evil.net` 一样能过 ——
// 攻击者只要注册一个自己控制的域名，想造多少账号造多少 ✗
// （这条把 bug 改回去就会红：把 EmailAllowed 里的 `d == want` 改成
// `strings.HasSuffix(d, want)`）
func TestEmailAllowed_NoSuffixBypass(t *testing.T) {
	domains := []string{"qq.com", "163.com"}

	bad := []string{
		"evil@qq.com.evil.net",    // 后缀匹配能被这个绕过去
		"evil@notqq.com",          // 后缀里有 "qq.com" 但域名不是一个
		"evil@qq.com.cn",          // 另一个真域名，但不在白名单里
		"evil@xqq.com",            // 尾巴像
		"evil@qq.com.",            // 结尾多个点（DNS 绝对形式）—— 也不该放行
		"evil@sub.qq.com",         // 子域名不是同一个域名
		"evil@163.com.attacker.t", // 同理
	}
	for _, e := range bad {
		if EmailAllowed(e, domains) {
			t.Errorf("%q 不该被放行（域名必须整体相等）✗", e)
		}
	}

	good := []string{"a@qq.com", "a@QQ.com", "a@163.com"}
	for _, e := range good {
		if !EmailAllowed(NormalizeEmail(e), domains) {
			t.Errorf("%q 该被放行 ✗", e)
		}
	}
}

func TestCheckEmail(t *testing.T) {
	domains := DefaultEmailDomains

	ok := []string{"a@qq.com", "a.b+tag@163.com", "x@foxmail.com", "y@139.com"}
	for _, e := range ok {
		if msg := CheckEmail(NormalizeEmail(e), domains); msg != "" {
			t.Errorf("%q 该通过，却被拒：%s", e, msg)
		}
	}

	bad := map[string]string{
		"":                  "空的",
		"没有at":              "没有 @",
		"@qq.com":           "本地部分空",
		"a@@qq.com":         "两个 @",
		"a@qq":              "域名没有点",
		"a@.qq.com":         "域名以点开头",
		"a@qq.com.":         "域名以点结尾",
		"a @qq.com":         "有空格",
		"a\t@qq.com":        "有制表符",
		"a@qq.com\x00":      "有控制字符",
		"a@gmail.com":       "域名不在白名单",
		"a@qq.com.evil.net": "后缀绕过",
	}
	for in, why := range bad {
		if msg := CheckEmail(NormalizeEmail(in), domains); msg == "" {
			t.Errorf("%q（%s）该被拒，却通过了 ✗", in, why)
		}
	}

	// 太长的
	long := strings.Repeat("a", 300) + "@qq.com"
	if msg := CheckEmail(NormalizeEmail(long), domains); msg == "" {
		t.Error("超长邮箱该被拒 ✗")
	}
}

// 不在白名单时的提示得**把允许的域名说出来** ——
// 只说"格式不对"的话，用户会一直改格式，永远猜不到是域名不让用。
func TestCheckEmail_WhitelistReasonMentionsDomains(t *testing.T) {
	msg := CheckEmail("a@gmail.com", []string{"qq.com", "163.com"})
	if !strings.Contains(msg, "qq.com") || !strings.Contains(msg, "163.com") {
		t.Errorf("白名单拒绝的文案该列出允许的域名，实得：%s", msg)
	}
}

func TestMaskEmail(t *testing.T) {
	cases := []struct{ in, want string }{
		{"bob@qq.com", "b***@qq.com"},
		{"a@qq.com", "*@qq.com"},
		{"", "***"},
		{"没有at", "***"},
	}
	for _, c := range cases {
		if got := MaskEmail(c.in); got != c.want {
			t.Errorf("MaskEmail(%q) = %q，该是 %q", c.in, got, c.want)
		}
	}
	// 打码之后不该还能看到原邮箱的完整本地部分
	if strings.Contains(MaskEmail("secretname@qq.com"), "secretname") {
		t.Error("打码没打干净 ✗")
	}
}

func TestCheckPassword(t *testing.T) {
	if msg := CheckPassword("1234567"); msg == "" {
		t.Error("7 位该被拒 ✗")
	}
	if msg := CheckPassword("12345678"); msg != "" {
		t.Errorf("8 位该通过，却被拒：%s", msg)
	}
	// 72 字节以上：bcrypt 会静默截断 ⇒ 必须拒，否则用户以为设了长密码
	if msg := CheckPassword(strings.Repeat("a", 73)); msg == "" {
		t.Error("超过 72 字节该被拒 ✗（bcrypt 只取前 72 字节）")
	}
	if msg := CheckPassword(strings.Repeat("a", 72)); msg != "" {
		t.Errorf("正好 72 字节该通过，却被拒：%s", msg)
	}
	// 汉字算 3 字节：25 个汉字 = 75 字节 ⇒ 该拒
	if msg := CheckPassword(strings.Repeat("密", 25)); msg == "" {
		t.Error("25 个汉字（75 字节）该被拒 ✗")
	}
	// 24 个汉字 = 72 字节 ⇒ 该过
	if msg := CheckPassword(strings.Repeat("密", 24)); msg != "" {
		t.Errorf("24 个汉字（72 字节）该通过，却被拒：%s", msg)
	}
}
