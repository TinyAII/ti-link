// 命令行版：host 走多人房主逻辑（internal/host），join 走朋友逻辑（internal/node）。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"p2plink/internal/host"
	"p2plink/internal/identity"
	"p2plink/internal/netutil"
	"p2plink/internal/node"
	"p2plink/internal/proto"
)

// ⛔ 这里曾经有个 `checkLocalGame()`：「本机这个端口上得是我们支持的某款游戏」。
// **已经去掉了** —— 理由见 cmd/gui/backend.go 里 CreateRoom 上方那段说明 ✓
//
// 简短版：改个 exe 文件名就能绕过去（`evil.exe` → `terraria.exe`），
// 挡不住有心的，只挡得住不会改文件名的，却给了一种"有防护"的错觉 ✗
//
// 📌 命令行版**就是给人自己用的** —— 他填哪个端口他自己清楚，不用我们替他判断 ✓

func main() {
	signalAddr := flag.String("signal", "", "穿透服务器地址 host:port（必填）")
	room := flag.String("room", "test", "房间 ID")
	mode := flag.String("mode", "host", "本端角色: host 或 join")
	stunServer := flag.String("stun", "", "STUN 服务器（逗号分隔），留空用默认")
	port := flag.Int("port", 0, "本地 UDP 端口，0 = 随机（仅 join）")
	nick := flag.String("nick", "", "昵称，显示在成员列表里")
	avatar := flag.Int("avatar", 0, "内置头像的下标（0 起）")
	timeout := flag.Duration("timeout", 25*time.Second, "打洞超时")
	wait := flag.Duration("wait", 180*time.Second, "等待房主的最长时间（仅 join）")
	local := flag.String("local", "127.0.0.1:25565", "本机 TCP 地址：host 为要连的服务，join 为要监听的地址")
	maxSize := flag.Int("max", 4, "房间人数上限（含房主，仅 host）")
	game := flag.String("game", "", "本端在开哪款游戏（可选）：填了后台看板能显示、也能按游戏筛选。🩸 自报字段，只是展示，不是防线")
	flag.Parse()

	// 🩸 游戏是**必填**的（2026-10-16 晚）：服务器按它判断"该不该让你进这个房间" ✓
	// 不填的话服务器会拒 —— 干脆在这儿就说清楚，别连上去再被拒 ✗
	if strings.TrimSpace(*game) == "" {
		fmt.Fprintln(os.Stderr, "用法: p2plink -signal <host:port> -room <ID> -mode host|join -game <游戏名>")
		fmt.Fprintln(os.Stderr, "      令牌：设环境变量 P2PLINK_TOKEN，或在 GUI 客户端里登录过一次（会存下来）")
		fmt.Fprintln(os.Stderr, "  -game 必填：你这边在玩哪款游戏（我的世界 / 泰拉瑞亚 / 星露谷物语 / 生存战争）")
		os.Exit(1)
	}

	// 🩸 **凭据不走命令行参数**（2026-10-17 改）：命令行对所有本地进程可见，
	// 还会进 shell 历史 ⇒ 令牌/密码从这儿漏出去 ✗
	// 令牌：环境变量优先，其次读客户端登录后存下的那份 ✓
	token := strings.TrimSpace(os.Getenv("P2PLINK_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(identity.Token())
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "没有登录令牌 —— 两种给法：")
		fmt.Fprintln(os.Stderr, "  ① 先在 GUI 客户端里登录一次（令牌会存下来，这儿自动读到）")
		fmt.Fprintln(os.Stderr, "  ② 设环境变量：set P2PLINK_TOKEN=你的令牌")
		os.Exit(1)
	}
	// 房间密码同样走环境变量 ✓（host 模式是"要设的密码"，join 模式是"房主给的密码"）
	roomPass := os.Getenv("P2PLINK_ROOMPASS")

	if *signalAddr == "" {
		fmt.Println("用法: p2plink -signal <host:port> -room <ID> -mode host|join")
		os.Exit(1)
	}

	logf := func(format string, a ...any) {
		fmt.Printf("[%-4s] %s\n", *mode, fmt.Sprintf(format, a...))
	}
	cid := identity.ClientID()
	if *mode == "host" {
		// ⭐ 端口先过一遍策略：1–1023 和几个高危端口不放行 ✓
		// 不是安全边界，只是"挡一下顺手" —— 详见 internal/netutil/ports.go ✓
		if p, err := netutil.PortOf(*local); err == nil {
			if msg := netutil.CheckPort(p); msg != "" {
				fmt.Fprintln(os.Stderr, msg)
				os.Exit(1)
			}
		}

		h := host.New(host.Config{
			SignalAddr:   *signalAddr,
			Room:         *room,
			Token:        token,
			RoomPass:     roomPass,
			Nick:         *nick,
			Avatar:       *avatar,
			ClientID:     cid,
			MaxSize:      *maxSize,
			Local:        *local,
			Game:         *game, // 游戏名报给服务器：后台看板显示/按游戏筛选（自报字段，只是展示）
			StunServers:  *stunServer,
			PunchTimeout: *timeout,
		})
		// CLI 是给技术用户在服务器上用的：显示带（打码）地址的版本，排障更有用
		h.OnLog = func(_, detail string) { logf("%s", detail) }
		h.OnState = func(s string) { logf("%s", s) }
		h.OnMembers = func(ms []host.Member) {
			names := make([]string, 0, len(ms))
			for _, m := range ms {
				names = append(names, proto.DisplayName(m.Label, m.Seat, m.Host))
			}
			logf("房间成员：%v", names)
		}
		h.OnBanList = func(bs []host.Ban) {
			names := make([]string, 0, len(bs))
			for _, b := range bs {
				names = append(names, b.Label)
			}
			logf("黑名单：%v", names)
		}

		go func() {
			c := make(chan os.Signal, 1)
			signal.Notify(c, os.Interrupt, syscall.SIGTERM)
			<-c
			logf("正在关闭房间…")
			h.Stop()
		}()

		if err := h.Run(); err != nil {
			logf("结束: %v", err)
			os.Exit(1)
		}
		return
	}

	r := node.New(node.Config{
		SignalAddr:   *signalAddr,
		Room:         *room,
		Token:        token,
		RoomPass:     roomPass,
		Nick:         *nick,
		Avatar:       *avatar,
		ClientID:     cid,
		Local:        *local,
		Game:         *game, // 同上：给后台看板用
		StunServers:  *stunServer,
		UDPPort:      *port,
		PunchTimeout: *timeout,
		WaitTimeout:  *wait,
	})
	r.OnLog = func(_, detail string) { logf("%s", detail) }
	r.OnState = func(_ node.State, msg string) { logf("%s", msg) }
	r.OnNotice = func(s string) { logf("提示：%s", s) }

	if err := r.Start(); err != nil {
		logf("结束: %v", err)
		os.Exit(1)
	}
}
