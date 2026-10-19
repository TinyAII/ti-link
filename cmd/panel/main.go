// 管理面板：注册 / 登录 / 后台。
//
// 它是**唯一**写账号库的进程（单写者约定，见 internal/auth 的包注释）。
//
// 起它：
//
//	# 线上（证书由 certbot 签）
//	p2plink-panel -listen :8443 -data /root/p2plink-data \
//	    -tls-cert /etc/letsencrypt/live/<域名>/fullchain.pem \
//	    -tls-key  /etc/letsencrypt/live/<域名>/privkey.pem
//
//	# 本地看界面（临时自签证书，浏览器会警告，正常）
//	p2plink-panel -listen 127.0.0.1:8443 -data ./tmp/panel-data -dev-tls
//
// 建管理员（**不走邀请码**：服务器主人没有"谁给他发码"这回事）：
//
//	p2plink-panel admin-add you@qq.com -data ./tmp/panel-data
//	# 它会打一个随机密码出来，只打这一次
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"p2plink/internal/auth"
	"p2plink/internal/tlsconf"
)

func main() {
	// 子命令：建管理员。得放在 flag.Parse 之前，否则 -data 之类会被当成未知参数。
	if len(os.Args) > 1 && os.Args[1] == "admin-add" {
		os.Exit(adminAdd(os.Args[2:]))
	}

	listen := flag.String("listen", "127.0.0.1:8443", "监听地址")
	dataDir := flag.String("data", "", "数据目录（账号库放在这儿）——**必填**")
	tlsCert := flag.String("tls-cert", "", "TLS 证书链（fullchain.pem）；要和 -tls-key 一起给")
	tlsKey := flag.String("tls-key", "", "TLS 私钥（privkey.pem）")
	devTLS := flag.Bool("dev-tls", false, "本地看界面用：临时自签证书（**别在线上用**）")
	signalURL := flag.String("signal", "",
		"穿透服务器的本机只读端点（取房间列表和指标用）——**必填**；"+
			"unix socket 路径，或 http:// URL")
	domains := flag.String("email-domains", strings.Join(auth.DefaultEmailDomains, ","),
		"允许注册的邮箱域名，逗号分隔")

	// ---- 发信（登录验证码用）----
	// 🩸 **口令只从环境变量来**：命令行参数会被同机的别人用 `ps` 看到 ✗
	// 所以 -smtp-pass 留着但**不推荐**用，P2P_SMTP_PASS 才是正经路子
	// （部署时写进 systemd 的 EnvironmentFile，文件 0600）。
	smtpHost := flag.String("smtp-host", envOr("P2P_SMTP_HOST", ""), "发信服务器（留空 = 不发信，验证码登录关闭）")
	smtpPort := flag.Int("smtp-port", envOrInt("P2P_SMTP_PORT", 465), "发信端口：465=隐式 TLS（推荐），587=STARTTLS")
	smtpUser := flag.String("smtp-user", envOr("P2P_SMTP_USER", ""), "发信账号")
	smtpPass := flag.String("smtp-pass", "", "发信口令/授权码（⚠️ 用 P2P_SMTP_PASS 环境变量，别用这个参数）")
	smtpFrom := flag.String("smtp-from", envOr("P2P_SMTP_FROM", ""), "发件人地址（一般不填，默认跟账号一样）")
	flag.Parse()

	// 环境变量优先（覆盖命令行）
	if v := os.Getenv("P2P_SMTP_PASS"); v != "" {
		*smtpPass = v
	}
	if *smtpFrom == "" {
		*smtpFrom = *smtpUser // 腾讯/网易这类都是"账号即发件人"✓
	}

	// 🩸 -signal **故意没有默认值**。
	//
	// 原来默认是 `http://127.0.0.1:8080` —— 那是台服务器上**别的服务**常用的号
	// （2026-10-17 就踩过：8080 被老板自己的 blog 占着）。
	// 默认值指错地方时，面板**照常启动、日志也正常**，只是取不到数据或者取到别人的 ✗
	// 这是"静默错误"，比"起不来"更难查得多 ✗
	//
	// 所以这里跟 -data 一样：**不给默认值，逼你显式想一下该连哪儿** ✓
	if *signalURL == "" {
		log.Fatal("必须给 -signal（穿透服务器的本机端点）—— 故意不给默认值：" +
			"默认值一旦指错地方，面板照常启动但取不到数据，特别难查 ✗ " +
			"生产用 <数据目录>/local.sock；本地调试用 http://127.0.0.1:8081")
	}

	if *dataDir == "" {
		log.Fatal("必须给 -data（账号库放哪儿）—— 故意不给默认值：" +
			"默认值一写，哪天忘了带参数就会在新目录里起一套**空账号库**，" +
			"表现是「所有人都登不上、后台看不见任何账号」，特别吓人 ✗")
	}

	st, err := auth.Open(*dataDir, true)
	if err != nil {
		log.Fatalf("打开账号库失败: %v", err)
	}
	key, err := loadOrCreateCSRFKey(*dataDir)
	if err != nil {
		log.Fatalf("准备 CSRF 密钥失败: %v", err)
	}

	domainList := splitDomains(*domains)
	if len(domainList) == 0 {
		log.Fatal("-email-domains 解析出来是空的 —— 那等于谁都注册不了")
	}

	tlsCfg, err := tlsconf.New(*tlsCert, *tlsKey)
	if err != nil {
		log.Fatalf("TLS 配置失败: %v", err)
	}
	if *devTLS {
		if tlsCfg != nil {
			log.Fatal("-dev-tls 和 -tls-cert/-tls-key 只能选一个")
		}
		if tlsCfg, err = tlsconf.NewDev(); err != nil {
			log.Fatalf("造临时证书失败: %v", err)
		}
	}

	srv := newServer(st, domainList, key, *signalURL)
	srv.mail = smtpConf{
		Host: *smtpHost, Port: *smtpPort,
		User: *smtpUser, Pass: *smtpPass, From: *smtpFrom,
	}
	if srv.mail.ready() {
		log.Printf("发信：%s:%d，发件人 %s（验证码登录可用）—— 口令不打印 ✓",
			srv.mail.Host, srv.mail.Port, srv.mail.From)
		if srv.mail.User != "" && srv.mail.Pass == "" {
			log.Printf("⚠️ 配了发信账号却没给口令（P2P_SMTP_PASS）—— 发信会认证失败")
		}
	} else {
		log.Printf("没配发信 ⇒ 「验证码登录」不可用，只能密码登录（要用就设 P2P_SMTP_HOST/P2P_SMTP_PASS）")
	}

	httpSrv := &http.Server{
		Addr:    *listen,
		Handler: srv.Handler(),
		// 🩸 这几个超时一个都不能省：没有它们，一个"连上就不发数据"的连接
		// 能一直占着 goroutine —— 而这是个**公网**端口 ✔
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	if tlsCfg != nil {
		httpSrv.TLSConfig = tlsCfg
	}

	// 限流表要定期清理，不然"每个来过的 IP 都留一条"就是慢性内存泄漏。
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for now := range t.C {
			idle := 2 * time.Hour
			n := srv.rlLoginIP.cleanup(now, idle) + srv.rlLoginEmail.cleanup(now, idle) +
				srv.rlSignupIP.cleanup(now, idle) + srv.rlAdminIP.cleanup(now, idle) +
				srv.rlSendCodeIP.cleanup(now, idle) + srv.mailCodes.cleanup(now)
			if n > 0 {
				log.Printf("面板：清掉了 %d 条过期的限流记录", n)
			}
		}
	}()

	scheme := "http"
	if tlsCfg != nil {
		scheme = "https"
	}
	log.Printf("管理面板已启动：%s://%s/admin/", scheme, *listen)
	log.Printf("账号库：%s（角色：唯一写者）", st.Dir())
	log.Printf("允许的邮箱域名：%s", strings.Join(domainList, "、"))
	if tlsCfg == nil {
		// ⚠️ 这句以前写死成「浏览器不会存 cookie」—— 但线上是**反代终结 TLS** 的，
		// 面板这头本来就是明文，那句话在线上天天刷、还会把人往错的方向带 ✗
		// 现在把两种情形都写清楚：自己起 TLS，或者前面有反代 ✓
		log.Printf("没开 TLS ⇒ 会话 cookie 带 __Host- 前缀，浏览器只在 https 上才存它。" +
			"两种正确用法：① 前面有反代（nginx 终结 TLS，记得带 X-Forwarded-Proto）；" +
			"② 本地看界面加 -dev-tls。直接拿 http 访问的话表现是「登录点了没反应」")
	}
	if *devTLS {
		log.Printf("⚠️ 用的是**临时自签证书**（只认 localhost）：浏览器报警告是正常的，别在线上这么跑")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Printf("收到退出信号，正在关……")
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(sh)
	}()

	var serveErr error
	if tlsCfg != nil {
		serveErr = httpSrv.ListenAndServeTLS("", "") // 证书走 TLSConfig
	} else {
		serveErr = httpSrv.ListenAndServe()
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Fatalf("监听 %s 失败: %v", *listen, serveErr)
	}
	log.Printf("已停止")
}

// loadOrCreateCSRFKey 读（没有就生成）CSRF 用的服务端密钥。
//
// 🩸 存盘不是可选项：密钥每次重启都换的话，所有已经打开的页面
// 手上的 CSRF 令牌立刻作废 —— 表现是"点哪个按钮都提示刷新页面重试" ✗
func loadOrCreateCSRFKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, "panel.key")
	b, err := os.ReadFile(path)
	if err == nil && len(b) >= 32 {
		return b, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	out := []byte(hex.EncodeToString(buf))
	// 0600：这是服务端密钥，同机别的用户不该读到
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return nil, err
	}
	log.Printf("生成了新的 CSRF 密钥：%s", path)
	return out, nil
}

func splitDomains(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// adminAdd 建一个管理员账号，密码随机生成、**只打印这一次**。
func adminAdd(args []string) int {
	fs := flag.NewFlagSet("admin-add", flag.ExitOnError)
	dataDir := fs.String("data", "", "数据目录（必填）")
	email := fs.String("email", "", "管理员的邮箱（必填）")
	domains := fs.String("email-domains", strings.Join(auth.DefaultEmailDomains, ","),
		"允许的邮箱域名，逗号分隔")
	_ = fs.Parse(args)

	// 🩸 邮箱做成 `-email` 而不是位置参数：Go 的 flag 遇到**第一个非 flag 参数
	// 就停止解析**，写成 `admin-add 我@qq.com -data ...` 的话 `-data` 会被
	// 当成多余的位置参数吃掉，报的错还是"必须给 -data"—— 特别误导 ✗
	// 全用 flag 就跟顺序无关了 ✓（2026-10-15 自己踩到过。）
	if *email == "" || *dataDir == "" {
		fmt.Fprintln(os.Stderr, "用法：p2plink-panel admin-add -data <数据目录> -email <邮箱>")
		return 2
	}
	st, err := auth.Open(*dataDir, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开账号库失败: %v\n", err)
		return 1
	}
	pass, err := randomPassword()
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成密码失败: %v\n", err)
		return 1
	}
	a, err := st.AddAdmin(*email, pass, splitDomains(*domains), time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "建管理员失败: %v\n", err)
		return 1
	}
	fmt.Printf("\n管理员已建好：%s（账号号 %d）\n", a.Email, a.ID)
	fmt.Printf("密码（**只显示这一次**，现在抄下来）：\n\n    %s\n\n", pass)
	fmt.Println("登进去之后建议自己改一个记得住的（客户端那边有改密码接口，后台页面还没做）。")
	return 0
}

// randomPassword 生成一个够强、又能手抄的密码。
//
// 32 个字符里含大小写和数字，16 位 ≈ 80 bit ⇒ 猜不出来 ✓
// 不用符号是故意的：这东西要手打一遍，符号经常被输入法或终端吃掉。
func randomPassword() (string, error) {
	const abc = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, x := range b {
		// 58 不整除 256 ⇒ 有一点点取模偏置。这里**无所谓**：
		// 偏的是"密码用哪个字符"，不影响它作为随机密码的强度（仍有 ~93 bit）✓
		sb.WriteByte(abc[int(x)%len(abc)])
	}
	return sb.String(), nil
}

// envOr / envOrInt：环境变量优先，没有就用默认值。
//
// 📌 为什么发信这类敏感配置走环境变量：进程的命令行**同机所有人都能看见**
// （ps aux）⇒ 口令写在参数里等于贴在墙上 ✗
// systemd 的 EnvironmentFile 是 0600 的，只有 root 读得到 ✓
func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envOrInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("⚠️ %s=%q 不是数字，用默认值 %d", k, v, def)
	}
	return def
}
