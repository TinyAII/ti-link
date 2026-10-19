package node

import (
	"fmt"
	"net"
	"strconv"

	"p2plink/internal/tunnel"
)

// 玩家侧的「游戏 UDP」这路（EnableUDP 打开时才有）。
// 房主侧在 internal/host/udp.go，两边只差"本机那头是谁"：
//
//	房主：DialUDP 主动连本机游戏的 UDP 端口
//	玩家：ListenUDP 在本机端口上等游戏连过来 ← 这个文件
//
// 📌 为什么监听的是**和 TCP 同一个端口**：玩家在游戏里填的是"地址:端口"，
// 就一个号。而 TCP 和 UDP 的端口空间是独立的（TCP 25565 和 UDP 25565
// 互不冲突）⇒ 同号监听正是游戏期望的样子 ✓
//
// 📌 为什么 fixedDest 传 nil：玩家侧不知道游戏会从哪个端口来
// （游戏可能换端口重连）⇒ 靠 UDPPipe 记"最近来的那个来源" ✓
func startGameUDPListen(split *tunnel.SplitConn, key []byte, peer net.Addr, second bool,
	local string, logf func(string, ...any)) (net.PacketConn, *tunnel.UDPPipe, error) {

	_, p, err := net.SplitHostPort(local)
	if err != nil {
		return nil, nil, fmt.Errorf("看不懂本机监听地址 %q: %w", local, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil || port <= 0 || port > 65535 {
		return nil, nil, fmt.Errorf("本机监听地址里的端口不对: %q", local)
	}

	uln, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return nil, nil, fmt.Errorf("UDP 端口 %d 用不了: %w", port, err)
	}
	pipe, err := tunnel.NewUDPPipe(split, key, uln, peer, nil, second, logf)
	if err != nil {
		uln.Close()
		return nil, nil, err
	}
	return uln, pipe, nil
}
