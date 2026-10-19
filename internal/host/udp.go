package host

import (
	"fmt"
	"net"
	"strconv"

	"p2plink/internal/tunnel"
)

// 这一路是给**游戏用 UDP** 的（EnableUDP 打开时才有）。
// 关掉的时候这个文件里的东西一个都不会被调用，TCP 那条路完全照旧 ✓
//
// 两头的差别：
//
//	房主侧：**主动连**本机游戏的 UDP 端口（DialUDP），隧道来的包转给它
//	玩家侧：**监听**本机端口等游戏连过来（ListenUDP），见 internal/node/udp.go
//
// 📌 为什么用 127.0.0.1：游戏和这个软件在同一台机器上，
// 走回环最省事、也最不容易被防火墙拦 ✓

// gameUDPAddr 是本机游戏在 UDP 上的地址。
//
// 跟 TCP 用**同一个端口号** —— 端口空间是独立的（TCP 25565 和 UDP 25565
// 互不冲突），而游戏开服时通常两个都监听同一个号 ✓
// 所以这儿不需要单独的配置，从 Local 里推就行 ✓
func (h *Host) gameUDPAddr() *net.UDPAddr {
	port := h.cfg.GamePort
	if port == 0 {
		if _, p, err := net.SplitHostPort(h.cfg.Local); err == nil {
			port, _ = strconv.Atoi(p)
		}
	}
	return h.gameUDPAddrOf(port)
}

// gameUDPAddrOf 按**指定端口**算本机游戏地址（第二路 UDP 用）✓
func (h *Host) gameUDPAddrOf(port int) *net.UDPAddr {
	if port <= 0 || port > 65535 {
		return nil
	}
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
}

// startGameUDP 房主侧：把隧道的那半 UDP 接到**本机游戏**上。
//
// fixedDest 传游戏地址 —— 房主这侧总是发给它（不像玩家侧要记来源）✓
func startGameUDP(split *tunnel.SplitConn, key []byte, peer net.Addr,
	game *net.UDPAddr, second bool, logf func(string, ...any)) (*tunnel.UDPPipe, error) {

	if game == nil {
		return nil, fmt.Errorf("不知道本机游戏的 UDP 端口")
	}
	conn, err := net.DialUDP("udp4", nil, game)
	if err != nil {
		return nil, fmt.Errorf("连本机游戏的 UDP 失败: %w", err)
	}
	pipe, err := tunnel.NewUDPPipe(split, key, conn, peer, game, second, logf)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return pipe, nil
}
