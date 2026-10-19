// 邀请码的生成与规范化。
//
// 用 Crockford Base32：字母表里去掉了 I / L / O / U ——
// 前三个是"会跟数字 1、0 看错"，U 是"能拼出脏话"。发给朋友的码是要**口头念、
// 手抄**的（微信里手打），所以"念得准、抄不错"比"看起来随机"重要。
package auth

import (
	"crypto/rand"
	"strings"
)

// codeAlphabet 是 Crockford Base32 的 32 个字符。
//
// 🩸🩸 **别往里加字符、别改顺序** —— 改一个字，已经发出去的码就全部对不上了 ✗
// （虽然是测试阶段，但这件事本身要当成规矩：码是跨端约定，跟头像下标同性质。）
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// CodeLen 是邀请码的规范长度。32^10 ≈ 2^50 ≈ 1.1×10^15。
//
// 这个长度够不够，看的是"能不能猜"：猜中一个的概率是 1/2^50，
// 而且**每个错猜都要打一次服务器** —— 配合注册接口的限流，
// 想靠猜拿到一个有效码不现实 ✓
// （真正的门槛其实在别处：码是**人肉一个一个发**的，发码的人就是瓶颈。）
const CodeLen = 10

// NewCode 生成一个新邀请码（规范形式，10 位）。
func NewCode() (string, error) {
	b := make([]byte, CodeLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// 🩸 这里**不会**有取模偏置：32 正好整除 256，所以 `& 31` 每个值的
	// 概率完全一样 ✓ 换成别的字母表长度（比如 36）就必须写拒绝采样了。
	var sb strings.Builder
	for _, x := range b {
		sb.WriteByte(codeAlphabet[x&31])
	}
	return sb.String(), nil
}

// NormalizeCode 把用户手打的码收成规范形式。
//
// 干三件事，都是"照着人打字的习惯来"：
//
//	① 去掉所有非字母数字（连字符、空格 —— 用户会把 XXXXX-XXXXX 整个粘进来）
//	② 全大写
//	③ O→0、I→1、L→1 —— Crockford 规定的纠错映射。
//	   规范码里本来就没有 O/I/L，所以这个映射**不会让两个不同的码撞到一起** ✓
//
// 📌 用户输错一个字符就"码无效"，他自己根本看不出错在哪儿；能自动救回来的就救。
func NormalizeCode(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		switch r {
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		// 只有数字和大写字母留下；连字符、空格、标点、中文一律丢掉。
		if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// FormatCode 把规范码排成给人看的 `XXXXX-XXXXX`（导出、后台列表用）。
//
// ⚠️ 只用于**显示**。存的和比的都是 NormalizeCode 的结果，
// 别把带连字符的形式存进去 —— 那样"用户粘了整个码"就永远对不上。
func FormatCode(code string) string {
	if len(code) != CodeLen {
		return code
	}
	return code[:5] + "-" + code[5:]
}
