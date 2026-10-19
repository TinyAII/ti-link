//go:build windows

package game

import (
	"net"
	"testing"
)

// 底层两张表的测试：**端口 → PID** 这条链得真的通 ✓
//
// 上面 `Verdict` / `VerdictFor` 那些判断全靠它 ——
// 它要是不准，那些判断全都是空中楼阁 ✓
func TestPortOwnerTables(t *testing.T) {
	// ── UDP 表 ──
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 UDP 监听失败: %v", err)
	}
	udpPort := pc.LocalAddr().(*net.UDPAddr).Port

	pid, err := udpPortOwner(udpPort)
	if err != nil {
		t.Fatalf("查 UDP 端口占用失败: %v", err)
	}
	if pid == 0 {
		t.Errorf("UDP 端口 %d 明明在听，却没查到占有者 ✗", udpPort)
	}

	// ── TCP 表 ──
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 TCP 监听失败: %v", err)
	}
	tcpPort := ln.Addr().(*net.TCPAddr).Port

	pid, err = tcpPortOwner(tcpPort)
	if err != nil {
		t.Fatalf("查 TCP 端口占用失败: %v", err)
	}
	if pid == 0 {
		t.Errorf("TCP 端口 %d 明明在听，却没查到占有者 ✗", tcpPort)
	}

	// ── 关掉之后该查不到 ──
	pc.Close()
	ln.Close()

	if pid, err := udpPortOwner(udpPort); err != nil || pid != 0 {
		t.Errorf("UDP 监听已关，该查到 0（实得 pid=%d err=%v）", pid, err)
	}
	if pid, err := tcpPortOwner(tcpPort); err != nil || pid != 0 {
		t.Errorf("TCP 监听已关，该查到 0（实得 pid=%d err=%v）", pid, err)
	}
}

// 📌 **两张表都要查** —— 游戏可能只听其中一种：
// 我的世界/泰拉瑞亚是 TCP，星露谷只有 UDP ✓
// 这条钉住"只查一张"这种退化 ✗
func TestProcessPathsSeesBothProtocols(t *testing.T) {
	// 只在 UDP 上听一个端口
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起 UDP 监听失败: %v", err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port

	paths, readNames, err := processPathsOnPort(port)
	if err != nil {
		t.Fatalf("查失败: %v", err)
	}
	if readNames == 0 {
		t.Error("只在 UDP 上听的端口，也该被查出来 ✗（说明漏查了 UDP 表）")
	}
	if len(paths) == 0 {
		t.Error("该拿到进程路径 ✓")
	}
}
