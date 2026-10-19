// 穿透服务器：维护房间（房主 / 成员名单 / 黑名单）。
// 只做牵线，不转发任何游戏数据。
//
// 其它几块在哪：
//
//	ratelimit.go —— 单 IP 并发 / 新建速率 / 房间总数上限
//	room.go      —— 房间数据结构 + 房主侧（建房、转发候选、踢人、拉黑）
//	join.go      —— 朋友侧（查黑名单与容量、加入、断开清理）
//	stats.go     —— 只读指标（**只有聚合数字**，不含"谁在连"）
//	localapi.go  —— 挂在 Unix socket 上的本机端点（/stats、/rooms、关闭房间，给管理面板用）
//
// 🩸 它还是账号库的**只读用户**：
//
//	账号表只有一个写者 —— 管理面板。这个进程一个字都不写，
//	所以它永远不会因为磁盘满 / 文件锁住 / 写了一半 这类事而不干活 ✓
//	而且这条不靠自觉：auth.Open(dir, false) 出来的库，每个写方法都直接报错 ✓
package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net"
	"time"

	"p2plink/internal/auth"
	"p2plink/internal/netutil"
	"p2plink/internal/proto"
	"p2plink/internal/signal"
	"p2plink/internal/tlsconf"
)

var (
	// st 是账号库的只读句柄（见文件头的注释）。
	st *auth.Store

	// insecureNoAuth 关掉登录校验。
	//
	// ⚠️ 它**只给本地测试用**（跑 e2e、手工试），名字里就写着不安全 ✓
	// 部署脚本**永远不带它**；带了的话等于回到"谁都能连"的旧世界 ✗
	insecureNoAuth bool
)

func main() {
	listen := flag.String("listen", ":39000", "监听地址")
	dataDir := flag.String("data", "", "账号库目录（**只读**打开）；不加 -insecure-no-auth 时必填")
	noAuth := flag.Bool("insecure-no-auth", false,
		"⚠️ 关掉登录校验，任何人都能连 —— **只在你自己电脑上测试时用**")
	tlsCert := flag.String("tls-cert", "", "TLS 证书链（fullchain.pem）；要和 -tls-key 一起给才启用")
	tlsKey := flag.String("tls-key", "", "TLS 私钥（privkey.pem）")
	metricsFile := flag.String("metrics", "",
		"指标采样落盘的 CSV 路径（如 /root/p2plink-metrics/metrics.csv）；留空只存内存。"+
			"⚠️ 别放进 -data 那个目录 —— 那个目录信令是只读打开的，而且是面板的地盘")
	localSock := flag.String("local", "",
		"本机端点的 Unix socket 路径（如 /root/p2plink-data/local.sock）；留空则不开。"+
			"权限会设成 0600 —— 那就是它的鉴权：只有 root（面板和信令自己）连得上")
	flag.Parse()
	insecureNoAuth = *noAuth

	if insecureNoAuth {
		log.Printf("⚠️⚠️ 以 -insecure-no-auth 启动：**不用登录谁都能连** —— " +
			"这个只该出现在你自己的电脑上，绝不要这样跑生产")
	} else {
		if *dataDir == "" {
			log.Fatal("必须给 -data（账号库在哪）—— 不给就没法校验登录。" +
				"只想本地测试的话显式加 -insecure-no-auth")
		}
		var err error
		// false = **只读**：这个进程永远不会碰账号库的写路径 ✓
		st, err = auth.Open(*dataDir, false)
		if err != nil {
			// 🩸 这里必须**直接起不来**，不能"读不出来就当空库继续跑" ——
			// 当空库的话是"所有人都被拒"，而日志上看起来一切正常，
			// 排查的人会往网络、客户端版本上一路找错方向 ✗
			log.Fatalf("打不开账号库: %v", err)
		}
		log.Printf("账号库：%s（角色：只读）", st.Dir())
	}

	// 房间历史（只留 30 天）。放这儿是为了赶在开始接客之前 init 完 ——
	// 晚初始化的话，第一批房间的历史会丢 ✗
	// dataDir 为空（-insecure-no-auth 本地测试）时只记内存、不落盘 ✓
	histInit(*dataDir)

	// 证书加载在 internal/tlsconf —— 面板也要一模一样的行为（续期后按需重读），
	// 共用一份，免得两边各写一遍然后分叉 ✓
	tlsCfg, err := tlsconf.New(*tlsCert, *tlsKey)
	if err != nil {
		log.Fatalf("TLS 配置失败: %v", err)
	}

	var ln net.Listener
	if tlsCfg != nil {
		ln, err = tls.Listen("tcp", *listen, tlsCfg)
	} else {
		ln, err = net.Listen("tcp", *listen)
	}
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	cipher := "明文（没配 -tls-cert/-tls-key）"
	if tlsCfg != nil {
		cipher = "TLS（证书会按需重读，续期后不用重启）"
	}
	if insecureNoAuth {
		log.Printf("穿透服务器已启动，监听 %s（**未校验登录**；%s）", *listen, cipher)
	} else {
		log.Printf("穿透服务器已启动，监听 %s（**每个连接都要校验登录令牌**；%s）", *listen, cipher)
	}
	log.Printf("限流：单 IP 并发 %d / 每秒 %d 新建；全局房间上限 %d；单 IP 房间上限 %d",
		maxConnsPerIP, connsPerSecond, maxRooms, maxRoomsPerIP)

	// 本机只读端点（可选）。放 goroutine 里 —— 它起不来也不该挡住联机 ✓
	go serveLocalAPI(*localSock)

	// 指标采样：内存里留 6 小时、同时追加落盘（-metrics 给路径；不给就只存内存）
	startMetrics(*metricsFile)

	go cleanIPLimits()

	for {
		c, err := ln.Accept()
		if err != nil {
			log.Printf("accept 失败: %v", err)
			continue
		}
		go handle(c)
	}
}

// handle 处理一条新连接：限流 → 校验注册消息（登录令牌 / clientID）→ 按角色分发。
func handle(c net.Conn) {
	defer c.Close()

	// 🩸 **每个连接兜一层 recover**（2026-10-17 补）。
	// 为什么：每条连接一个 goroutine，信令这边**没有全局 recover** ⇒
	// 任何一处空指针/越界 panic 都会让**整个进程崩掉**，所有房间一起消失 ✗
	// 而这里兜住的话，最多丢**这一条**连接 ✓
	//
	// 📌 现在代码里没找到客户端能触发的 panic 路径（审查时逐个查过）——
	//    这是"将来引入缺陷时的放大器"，先装上 ✓
	// 📌 地址也打码：panic 信息里可能带着对端地址 ✓
	defer func() {
		if v := recover(); v != nil {
			log.Printf("连接处理时 panic（已兜住，只影响这一条连接）：%v", v)
		}
	}()
	raw := c.RemoteAddr().String()
	remoteIP, _, _ := net.SplitHostPort(raw)

	// 🩸 日志里只出现**打码**的地址（120.227.*.*:1962）。
	// 公开分发之后，"日志里存了谁的 IP"属于收集个人信息；而排障只需要看出
	// "是不是同一个来源"就够 —— 打码两头都占（见 netutil.MaskAddr 的注释）。
	//
	// ⚠️ 名字必须带 masked：room.go / join.go 的日志同样用它，而有一条测试专门扫
	// "日志里不许出现没打码的地址变量"（cmd/signal/masklog_test.go）——
	// 这个名字就是在告诉那条测试"我是安全的"，别改回去 ✗
	//
	// ⚠️ remoteIP 保持**原样**：限流和黑名单是真靠它工作的，不是给人看的。
	// 另有一处例外 —— room.go 里踢人的日志用的是 member.remoteIP，那儿单独套一次。
	maskedRemote := netutil.MaskAddr(raw)

	if ok, why := allowConn(remoteIP); !ok {
		statRejectRate.Add(1)
		log.Printf("拒绝 %s：%s（单 IP 并发上限 %d、每秒 %d 新建；全局上限 %d）",
			maskedRemote, why, maxConnsPerIP, connsPerSecond, maxConnsTotal)
		return
	}
	defer releaseConn(remoteIP)

	// 从这儿起才算"在用"的一条连接 —— 被限流挡掉的那些不计入 ✓
	connOpened()
	defer connClosed()

	// 🩸 包一层再交给 Client：这样**我们自己**收发了多少字节才数得出来 ✓
	// （/proc 里没有"某个进程用了多少流量"这个数；而且数在这儿最准 ——
	//  也才是"我们的带宽"，跟这台机器上别的项目分得清）
	// ⚠️ countingConn 里必须一起转发 CloseWrite（半关能力），不然关房间时的
	//    那句通知会被 RST 冲掉 —— 见 metrics.go 里那段注释 ✗
	cc := newCountingConn(c)
	cl := signal.New(cc)

	m, err := cl.Recv(15 * time.Second)
	if err != nil {
		log.Printf("%s 读取注册消息失败: %v", maskedRemote, err)
		return
	}
	if m.Type != proto.TypeRegister {
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "期望 register"})
		return
	}

	// ⭐ **登录校验** —— 整套东西里唯一算数的鉴权点（它跑在服务器上，用户碰不着）。
	//
	// 失败要分成两句话回，因为用户能做的动作不一样：
	//   没带令牌   → 他多半是没登录 / 装了个老版本 → "请先在客户端里登录"
	//   令牌不认  → 被停用了、被强制下线了、或者过期了 → "登录已过期，请重新登录"
	// 合成一句的话，前面那种人会一直以为自己密码打错了 ✗
	var owner *auth.Account
	if !insecureNoAuth {
		if m.Token == "" {
			statNoToken.Add(1)
			log.Printf("%s 没带登录令牌，拒绝连接", maskedRemote)
			cl.Send(proto.Msg{Type: proto.TypeError, Err: "请先在客户端里登录再联机"})
			return
		}
		a, ok := st.VerifyToken(m.Token, time.Now())
		if !ok {
			statBadToken.Add(1)
			log.Printf("%s 登录令牌无效 / 已过期 / 账号已停用，拒绝连接", maskedRemote)
			cl.Send(proto.Msg{Type: proto.TypeError, Err: "登录已过期，请重新登录"})
			return
		}
		owner = a
	}

	if m.ClientID == "" {
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "缺少 clientID"})
		return
	}
	// 客户端发来的字段一律先验一遍 —— 能连上不等于信得过（见 validate.go）
	if err := validateRegister(m); err != "" {
		log.Printf("%s 注册字段不合法：%s", maskedRemote, err)
		cl.Send(proto.Msg{Type: proto.TypeError, Err: err})
		return
	}

	switch m.Role {
	case "host":
		serveHost(cl, cc, maskedRemote, remoteIP, m, owner)
	case "join":
		serveJoin(cl, cc, maskedRemote, remoteIP, m, owner)
	default:
		cl.Send(proto.Msg{Type: proto.TypeError, Err: "未知角色"})
	}
}
