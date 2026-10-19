//go:build windows

package game

import (
	"fmt"
	"syscall"
	"unsafe"
)

// 这一份负责"查本机某个端口被哪个进程占着"。
//
// 它管两件事：
//
//	① 那个「游戏开没开」的灯 —— 原来只会**连一下本机的 TCP 端口**，
//	   对纯 UDP 的游戏（星露谷）**永远探不到** ✗，界面一直报"连不上"，
//	   用户以为自己没开好（2026-10-15 老板就卡在这儿）
//	② 更要紧的：**建房前确认"这个端口上真的是那款游戏"** ——
//	   不确认的话我们就等于一个通用隧道，填 80 就是网站、填 3389 就是远程桌面 ✗
//
// UDP 没有"连上"这回事，所以换个问法：**本机这个端口，是谁在听？**
// 靠 Windows 的 GetExtendedUdpTable / GetExtendedTcpTable（iphlpapi）
// 拿到「端口 → PID」，再拿进程名去比对 ✓

var (
	iphlpapi                = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedUdpTable = iphlpapi.NewProc("GetExtendedUdpTable")
	procGetExtendedTcpTable = iphlpapi.NewProc("GetExtendedTcpTable")

	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

const (
	afInet = 2
	// UDP_TABLE_CLASS 的取值（见 Win32 文档）：
	//
	//	UDP_TABLE_BASIC        = 0
	//	UDP_TABLE_OWNER_PID    = 1   ← 我们要的就是它
	//	UDP_TABLE_OWNER_MODULE = 2
	//
	// 🩸 别写成 3 —— 那是 **TCP** 那套里的 `TCP_TABLE_OWNER_PID_LISTENER`。
	// 串台的下场是 API 直接返回 87（ERROR_INVALID_PARAMETER）✗
	// （2026-10-15 写的时候就是这么错的，测试一跑就抓到了 ✓）
	udpTableOwnerPID = 1

	// TCP_TABLE_CLASS 的取值：
	//
	//	TCP_TABLE_BASIC_LISTENER        = 0
	//	TCP_TABLE_BASIC_CONNECTIONS     = 1
	//	TCP_TABLE_BASIC_ALL             = 2
	//	TCP_TABLE_OWNER_PID_LISTENER    = 3   ← 我们要的就是它（只看"谁在听"）
	//	TCP_TABLE_OWNER_PID_CONNECTIONS = 4
	//	TCP_TABLE_OWNER_PID_ALL         = 5
	tcpTableOwnerPIDListener = 3

	processQueryLimitedInformation = 0x1000
)

// mibUDPRowOwnerPID 对应 Win32 的 MIB_UDPROW_OWNER_PID ✓
type mibUDPRowOwnerPID struct {
	LocalAddr uint32
	LocalPort uint32 // ⚠️ 网络字节序（大端）—— 用之前要转
	OwningPID uint32
}

type mibUDPTableOwnerPID struct {
	NumEntries uint32
	Table      [1]mibUDPRowOwnerPID
}

// mibTCPRowOwnerPID 对应 MIB_TCPROW_OWNER_PID ✓
// ⚠️ 比 UDP 那个多三个字段（状态 / 远端地址 / 远端端口），别少写 ✗
type mibTCPRowOwnerPID struct {
	State      uint32
	LocalAddr  uint32
	LocalPort  uint32 // ⚠️ 也是网络字节序
	RemoteAddr uint32
	RemotePort uint32
	OwningPID  uint32
}

type mibTCPTableOwnerPID struct {
	NumEntries uint32
	Table      [1]mibTCPRowOwnerPID
}

// udpPortOwner 查「本机哪个进程在听这个 UDP 端口」。没人在听 ⇒ pid = 0 ✓
func udpPortOwner(port int) (uint32, error) {
	var size uint32
	// 先空跑一次问要多大的缓冲（返回值非 0 且是"缓冲不够"也属于正常）
	r, _, _ := procGetExtendedUdpTable.Call(
		0, uintptr(unsafe.Pointer(&size)), 0, afInet, udpTableOwnerPID, 0)
	if size == 0 {
		return 0, fmt.Errorf("取 UDP 表大小失败（返回值 %d）", r)
	}

	buf := make([]byte, size)
	r, _, err := procGetExtendedUdpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, afInet, udpTableOwnerPID, 0)
	if r != 0 {
		return 0, fmt.Errorf("读 UDP 表失败: %v", err)
	}

	tbl := (*mibUDPTableOwnerPID)(unsafe.Pointer(&buf[0]))
	if tbl.NumEntries == 0 {
		return 0, nil
	}
	rows := unsafe.Slice(&tbl.Table[0], tbl.NumEntries)
	for _, row := range rows {
		if netToHostPort(row.LocalPort) == port {
			return row.OwningPID, nil
		}
	}
	return 0, nil
}

// tcpPortOwner 查「本机哪个进程在**监听**这个 TCP 端口」。没人在听 ⇒ pid = 0 ✓
func tcpPortOwner(port int) (uint32, error) {
	var size uint32
	r, _, _ := procGetExtendedTcpTable.Call(
		0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDListener, 0)
	if size == 0 {
		return 0, fmt.Errorf("取 TCP 表大小失败（返回值 %d）", r)
	}

	buf := make([]byte, size)
	r, _, err := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
		0, afInet, tcpTableOwnerPIDListener, 0)
	if r != 0 {
		return 0, fmt.Errorf("读 TCP 表失败: %v", err)
	}

	tbl := (*mibTCPTableOwnerPID)(unsafe.Pointer(&buf[0]))
	if tbl.NumEntries == 0 {
		return 0, nil
	}
	rows := unsafe.Slice(&tbl.Table[0], tbl.NumEntries)
	for _, row := range rows {
		if netToHostPort(row.LocalPort) == port {
			return row.OwningPID, nil
		}
	}
	return 0, nil
}

// netToHostPort 端口在表里是**网络字节序**（大端）⇒ 转回主机序 ✓
func netToHostPort(p uint32) int {
	return int(uint16(p>>8) | uint16(p&0xFF)<<8)
}

// processPath 拿某个 PID 的可执行文件全路径。
func processPath(pid uint32) (string, error) {
	if pid == 0 {
		return "", fmt.Errorf("pid 为 0")
	}
	h, _, err := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if h == 0 {
		return "", fmt.Errorf("打不开进程 %d: %v", pid, err)
	}
	defer syscall.CloseHandle(syscall.Handle(h))

	var buf [syscall.MAX_PATH]uint16
	n := uint32(len(buf))
	r, _, err := procQueryFullProcessImageNameW.Call(
		h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return "", fmt.Errorf("取进程路径失败: %v", err)
	}
	return syscall.UTF16ToString(buf[:n]), nil
}

// processPathsOnPort 把"本机这个端口上有哪些进程"一次性查出来。
//
// 📌 **TCP 和 UDP 两张表都查** ✓ —— 游戏可能只听其中一种：
//   - 我的世界 / 泰拉瑞亚 ⇒ TCP（泰拉瑞亚还带 UDP）
//   - 星露谷 ⇒ 只有 UDP
//
// 只查一张的话总有一类查不到 ✗
//
// 返回：
//
//	paths     端口上那些进程的可执行文件路径（读不出来的不算进去）
//	readNames 成功读出名字的**个数** —— 用来区分"确定没人"和"有人但读不到"✓
//	err       表读不了 / 名字读不出 之类的"说不准"
func processPathsOnPort(port int) (paths []string, readNames int, err error) {
	pids := map[uint32]bool{}
	for _, owner := range []func(int) (uint32, error){tcpPortOwner, udpPortOwner} {
		pid, e := owner(port)
		if e != nil {
			err = e
			continue
		}
		if pid != 0 {
			pids[pid] = true
		}
	}

	for pid := range pids {
		p, e := processPath(pid)
		if e != nil {
			err = e
			// 🩸 读不出名字（多半是权限）⇒ **不算数，但也不算"没有"** ✗
			// 调用方靠 readNames 判断出"说不准" ✓
			continue
		}
		paths = append(paths, p)
		readNames++
	}
	return paths, readNames, err
}
