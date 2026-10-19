// 数据目录「挪地方」这件事的记忆 —— 存在注册表里。
//
// 🩸 为什么非得是注册表：**数据目录里的东西自己也会搬家** ✗
// 指针放在数据目录里 = 一搬就找不到自己了。注册表在它之外，是唯一靠谱的地方 ✓
// （反正开机自启那套已经用 HKCU 了，不多一个新依赖 ✓）
package appdir

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	regKeyPath = `Software\P2PLink`
	regValue   = "DataParent"

	// driveFixed 是 GetDriveTypeW 里"固定盘"的返回值。
	// 🩸 只认它：光驱（5）、U 盘（2）、网络盘（4）一律不要 ——
	// 把数据放可移动盘上，拔了就等于丢 ✗
	driveFixed = 3
)

var (
	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procGetDriveTy = kernel32.NewProc("GetDriveTypeW")
)

// driveType 返回某个盘的类型（3 = 固定盘）。
func driveType(root string) uint32 {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0
	}
	t, _, _ := procGetDriveTy.Call(uintptr(unsafe.Pointer(p)))
	return uint32(t)
}

func init() {
	// 从 D: 开始往后找固定盘（**跳过 C 盘** —— 老板要的就是"万不得已别放 C 盘" ✓）
	listUsableDrives = func() []string {
		var out []string
		for c := 'D'; c <= 'Z'; c++ {
			root := string(c) + `:\`
			if _, err := os.Stat(root); err != nil {
				continue // 这个盘符不存在
			}
			if driveType(root) != driveFixed {
				continue // 光驱 / U 盘 / 网络盘
			}
			out = append(out, root)
		}
		return out
	}

	readParent = func() string {
		k, err := registry.OpenKey(registry.CURRENT_USER, regKeyPath, registry.QUERY_VALUE)
		if err != nil {
			return ""
		}
		defer k.Close()
		v, _, err := k.GetStringValue(regValue)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(v)
	}
	writeParent = func(base string) error {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, regKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		if base == "" {
			// 恢复默认 = 把这个值删掉（键留着没关系）
			if err := k.DeleteValue(regValue); err != nil && err != registry.ErrNotExist {
				return err
			}
			return nil
		}
		return k.SetStringValue(regValue, base)
	}
}
