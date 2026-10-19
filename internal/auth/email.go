// 邮箱规范化与域名白名单。
//
// 白名单的用途只有一个：**让人没法用临时邮箱批量注册**。
// 它的强度上限必须说清楚（见下面的注释），别拿它当防线宣传。
package auth

import (
	"strconv"
	"strings"
)

// DefaultEmailDomains 是默认放行的邮箱域名。
//
// 挑的是"一个号对应一个真人、批量注册有成本"的国内主流邮箱：
//
//	QQ / Foxmail —— 都要 QQ 号，一个人很难开几十个
//	163 / 126 / yeah / 188 —— 网易，注册要手机号
//	sina / sohu / 139 / aliyun —— 同理
//
// ⚠️ 这个表**只是默认值**，真正的来源是服务器配置（见 docs/账号与邀请码.md）。
// 别在代码里假设它不会变 —— 老板明确要求过"每个功能都不要写死，要留空间"。
var DefaultEmailDomains = []string{
	"qq.com",
	"foxmail.com",
	"163.com",
	"126.com",
	"yeah.net",
	"188.com",
	"sina.com",
	"sina.cn",
	"sohu.com",
	"139.com",
	"aliyun.com",
}

// NormalizeEmail 把邮箱收成统一形式：去空白 + 全小写。
//
// 为什么要小写：邮箱的本地部分（@ 前面那截）按 RFC 是**区分大小写**的，
// 但现实里主流服务商一律不区分 ⇒ 不统一的话，"Bob@qq.com" 和 "bob@qq.com"
// 会变成两个账号（一个人白嫖两次注册），而且用户自己永远搞不清哪个是哪个。
// 按"现实惯例"统一，是这里唯一说得通的做法。
func NormalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// EmailDomain 取 @ 后面那截（没有 @ 就返回空串）。
//
// 📌 用 **LastIndex** 而不是 Index：本地部分按 RFC 允许出现引号包裹的 @
// （`"a@b"@qq.com`），取最后一个 @ 才是域名。虽然白名单里没有这种邮箱，
// 但"取分隔符"这种事一开始就取对，比以后来补要省事。
func EmailDomain(email string) string {
	i := strings.LastIndex(email, "@")
	if i < 0 {
		return ""
	}
	return email[i+1:]
}

// EmailAllowed 判断域名是否在白名单里。
//
// 🩸🩸 **必须是整体相等，不能是 HasSuffix。** 这是个教科书级的坑：
// 写成 `strings.HasSuffix(email, "qq.com")` 的话，`任意@qq.com.evil.net`
// 一样能过 —— 攻击者只要注册一个自己控制的域名，想造多少账号造多少。
// 下面这一行有对应的反证测试（email_test.go 的 TestEmailAllowed_NoSuffixBypass）。
func EmailAllowed(email string, domains []string) bool {
	d := EmailDomain(email)
	if d == "" {
		return false
	}
	for _, want := range domains {
		if d == strings.ToLower(strings.TrimSpace(want)) {
			return true
		}
	}
	return false
}

// CheckEmail 校验邮箱，返回**能直接给用户看的中文**（空串 = 通过）。
//
// ⚠️ 别把这里当安全边界：客户端那份校验是给守规矩的人看的，改了就能绕。
// 真正管用的是服务器也跑同一个函数（两边共用这一份，不是各写一份）。
func CheckEmail(email string, domains []string) string {
	if email == "" {
		return "请填邮箱"
	}
	if len(email) > MaxEmailLen {
		return "邮箱太长了"
	}
	// 控制字符一律拒：它会进日志、进 CSV、进后台页面，是注入的常见入口。
	for _, r := range email {
		if r < 0x20 || r == 0x7f {
			return "邮箱里有不能用的字符"
		}
	}
	if strings.ContainsAny(email, " \t") {
		return "邮箱里不能有空格"
	}
	at := strings.Index(email, "@")
	if at <= 0 {
		return "邮箱格式不对"
	}
	if at != strings.LastIndex(email, "@") {
		return "邮箱格式不对"
	}
	// 域名单独再收一遍形状：非空、带点、且点不在两头。
	// 不收的话 "@.com" / "a@qq." 这类会一路走到白名单那儿，
	// 用户拿到的提示就变成"只支持这些邮箱"，其实他错的是格式。
	dom := EmailDomain(email)
	if dom == "" || !strings.Contains(dom, ".") ||
		strings.HasPrefix(dom, ".") || strings.HasSuffix(dom, ".") {
		return "邮箱格式不对"
	}
	if !EmailAllowed(email, domains) {
		// 把允许的域名报出来 —— 不报的话用户会以为是"格式不对"，
		// 然后反复改格式，永远猜不到是"域名不让用"。
		return "暂时只支持这些邮箱：" + strings.Join(domains, "、")
	}
	return ""
}

// MaskEmail 把邮箱打码成 `a***@qq.com`，用于日志和给用户看的列表。
//
// 📌 和日志里 IP 打码（netutil.MaskAddr）是同一件事、同一个理由：
// 排障只需要看出"是不是同一个"，不需要知道具体是谁。
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return "***"
	}
	local, domain := email[:at], email[at:]
	if len(local) <= 1 {
		return "*" + domain
	}
	// 按**字符**取首字母，别按字节 —— 字节切会把多字节字符劈成乱码。
	rs := []rune(local)
	return string(rs[:1]) + "***@" + strings.TrimPrefix(domain, "@")
}

// MaskSecret 把任意密钥/口令打码，只留长度信息。
// 日志里绝不能出现 token 原文或密码原文，打印前先过这个。
func MaskSecret(s string) string {
	if s == "" {
		return "(空)"
	}
	return "(长度 " + strconv.Itoa(len(s)) + ")"
}
