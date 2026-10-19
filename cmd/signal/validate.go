// 服务端对客户端发来的字段做校验。
//
// 🩸 为什么服务端必须自己校验一遍：客户端那份校验（cmd/gui/backend.go 的
// checkPassLen 之类）是**给守规矩的用户看的**，挡不住改过的客户端。
// 更要紧的是——访问口令是能从客户端二进制里提取出来的，
// 所以"能连上"不等于"信得过"。服务端不看长度，就会被拿去撑内存 / 逼 CPU：
// 一条超大房间名 × 每 IP 20 个连接，就能把进程顶爆。
package main

import (
	"crypto/subtle"
	"regexp"
	"strings"

	"p2plink/internal/game"
	"p2plink/internal/netutil"
	"p2plink/internal/proto"
)

const (
	// maxRoomNameLen 房间 ID 上限。现在形状是 TI+6位数字+2位字母 = 10 位，
	// 留点余量给以后的格式。
	maxRoomNameLen = 16
	// maxPassLen 房间密码上限，跟客户端 maxRoomPassLen 对齐。
	maxPassLen = 32
	// maxClientIDLen 设备 ID 上限（客户端实际发 16 位十六进制）。
	maxClientIDLen = 64
	// maxCands 候选地址条数上限。正常客户端只给几个本机地址 + 一个公网地址。
	maxCands = 10
	// maxCandLen 单条候选地址上限。
	//
	// 🩸 原来写 45（IPv4:port 约 21 字符，给域名留一倍余量）——
	//    上了 IPv6 就不够：v6 地址最长 39 字符（8 组 × 4 位 + 7 个冒号），
	//    加方括号和端口就是 **47**，45 会把大部分真实 v6 地址判成"过长"整条拒掉 ✗
	//    实测：`[240e:3b0:1234:5678:9abc:def0:1234:5678]:27154` = 46 字符
	//    （2026-10-18，IPv6 支持的第一步）
	//    60 = 47 + 余量；放宽限制对老客户端无影响 ✓
	maxCandLen = 60
	// maxAvatarIdx 头像下标上限（客户端图库只有十几张，255 已经很宽松）。
	maxAvatarIdx = 255
	// pubKeyLen X25519 公钥固定 32 字节。
	pubKeyLen = 32
)

// roomNameRe 房间 ID 的允许形状：大写字母、数字、连字符，4~16 位。
//
// 卡形状比卡长度更管用 —— 房间 ID 会进日志、当 map key，
// 允许任意字符只会给自己添麻烦。真正要挡的是「超长」（撑内存）、
// 「控制字符/换行」（日志注入）和「../ 之类」（以后万一被当文件名用）。
// 连字符一条都挡不住，所以放开它 —— 换来的是名字能写得好读。
// 下界留 4 位是为了兼容旧格式（6 位随机字符），别把还没更新的客户端整个挡在门外。
var roomNameRe = regexp.MustCompile(`^[A-Z0-9-]{4,16}$`)

// validateRegister 校验注册消息，返回错误文案（空 = 通过）。
func validateRegister(m proto.Msg) string {
	switch {
	case !roomNameRe.MatchString(m.Room):
		return "房间 ID 不合法"
	case len([]rune(m.RoomPass)) > maxPassLen:
		return "房间密码过长"
	case len(m.ClientID) > maxClientIDLen:
		return "clientID 过长"
	case m.Avatar < 0 || m.Avatar > maxAvatarIdx:
		return "头像编号越界"
	case validateCands(m.Cands) != "":
		return validateCands(m.Cands)
	case len(m.PubKey) != 0 && len(m.PubKey) != pubKeyLen:
		// 空是允许的（老客户端不带），但带了就必须是合法长度 ——
		// 不合法的话对面 NewPublicKey 会失败，隧道永远建不起来
		return "公钥长度不合法"
	case m.GamePort2 != 0 && !game.IsDualPort(m.Game):
		// 🩸 白名单：只有**确实占两个端口**的游戏（目前只有《饥荒》）才能报第二路。
		//    任意游戏都能开双路的话，等于白送一个"多一条隧道"的口子 ✗
		//    📌 游戏名对不上（老客户端 / 乱填）也走这条 ⇒ 拒 ✓ 宁可少开，不多开 ✓
		return "这款游戏不支持第二路端口"
	case m.GamePort2 != 0 && m.GamePort2 == m.GamePort:
		// 🩸 两路端口**不能一样**：一样的话两条管子连的是同一个游戏端口，
		// 白开一路、白占一个本地监听 —— 而且要开的那个"第二路"根本没有意义 ✓
		return "第二路端口不能和主端口相同"
	case m.GamePort2 != 0 && netutil.CheckPort(m.GamePort2) != "":
		return netutil.CheckPort(m.GamePort2)
	case m.GamePort != 0 && netutil.CheckPort(m.GamePort) != "":
		// 房主报上来的游戏端口 —— 拦一下敏感端口 ✓（1–1023 / RDP / 数据库那些）
		//
		// 🩸 **这只是"提高门槛"，不是安全边界** ✗
		// 客户端改一下就能报别的数字，服务器**没法验证**（它看不到那个端口 ✓）
		// ⇒ 它挡的是"不知情 / 懒得改"的人 ✗ 详见 proto.Msg.GamePort 的说明 ✓
		//
		// 📌 0 = 没报（老式客户端）⇒ 不校验 ✓
		return netutil.CheckPort(m.GamePort)
	}
	for _, c := range m.Cands {
		if len(c) > maxCandLen {
			return "候选地址过长"
		}
	}
	return ""
}

// validRoomPass 单独校验房间密码长度（房主改密码时用）。
func validRoomPass(pass string) bool {
	return len([]rune(pass)) <= maxPassLen
}

// passEqual 常数时间比较口令。
//
// 逐个字节比较会在"第一个不同的字节"处提前返回，理论上能被用来一位一位地试出
// 口令前缀。隔着网络 + JSON 解析的噪声，实际上很难利用，但改起来就一行 ——
// 没理由留着这个尾巴。
func passEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// sanitizeGame 清洗客户端上报的游戏名。
//
// 🩸 它是**用户可控的字符串**，而面板会把它显示出来 ⇒ 必须在这儿收口：
//
//	· 截断到 24 个字符（客户端清单里的名字最长 5 个汉字）
//	· 去掉控制字符（换行 / 制表 / ESC 之类）—— 那东西进了表格会很难看，
//	  进了日志还会伪造出一行假的日志 ✗
//
// 📌 **不校验"是不是我们清单里的游戏"**：清单在客户端，加一款游戏不该改信令 ✗
//
//	（真要白名单就得两边各维护一份，迟早分叉）
//
// ⚠️ 清完之后**能是什么就是什么** —— 用户填"我是你爸爸"也照样显示。
//
//	这是展示字段，不是身份 ✗（性质见 proto.Msg.Game 那段）
//
// 空串 = 没上报（老客户端）✓
func sanitizeGame(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	out := make([]rune, 0, 24)
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
		if len(out) >= 24 {
			break
		}
	}
	return string(out)
}

// validateCands 校验候选地址列表（注册和"重发候选"共用）。
//
// 🩸 条数/长度不卡的话，客户端可以塞几百条上来 —— 服务器原样转发，
// 对面照着发几百个探测包，等于被当成放大器 ✗
func validateCands(cands []string) string {
	if len(cands) > maxCands {
		return "候选地址过多"
	}
	for _, c := range cands {
		if len(c) > maxCandLen {
			return "候选地址过长"
		}
	}
	return ""
}
