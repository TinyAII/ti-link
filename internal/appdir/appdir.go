// Package appdir 决定「数据目录在哪」—— settings.json、client-id 这些都放这儿。
//
// 默认是 `%AppData%\p2plink`（跟老版本**完全一样**，老用户不受影响 ✓）。
// 用户可以在设置里换到别的地方（比如 D 盘）——那时我们是选**父目录**，
// 自己再在里面建一层 `p2plink` 子目录，不把文件撒在人家文件夹根上 ✓
//
// 🩸 为什么"新位置"记在**注册表**、而不是某个文件里：
// **数据目录里的东西自己也会搬家** ✗ —— 指针放在数据目录里，一搬就找不到自己了。
// 注册表在数据目录之外，是这里唯一靠谱的地方 ✓
// （反正开机自启那套已经在用注册表了，不多一个依赖 ✓）
//
// 📌 全项目**只认这一个来源**：`cmd/gui` 的 settings 和 `internal/identity` 的 client-id
// 都走 Dir()。不这么干的话，搬完家会出现"设置跟着换了、身份还留在老地方" ✗
package appdir

import (
	"os"
	"path/filepath"
	"strings"
)

// subdir 是我们自己那一层目录名。
// 用户选的是**父目录**，文件落在 `<父目录>\p2plink\` 里 ✓
const subdir = "p2plink"

// readParent / writeParent 是"用户改过的父目录"的读写口子，由平台文件在 init 里接上：
//
//	appdir_windows.go —— 写注册表 HKCU\Software\P2PLink
//	appdir_other.go   —— 空实现（别的平台没这个功能，走默认目录）
//
// 做成变量而不是函数，是为了让测试能顶掉 —— 不然测一次就往用户注册表里写东西 ✗
var (
	readParent  = func() string { return "" }
	writeParent = func(string) error { return nil }
)

// DefaultParent 系统默认的父目录（也就是 %AppData%）。
// 设置页那个「恢复默认」拿它做参照 ✓
func DefaultParent() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return d
}

// Parent 当前用的父目录。
//
// 优先级（2026-10-16 晚改）：
//
//	① 用户在设置里改过的 ⇒ 按他选的 ✓
//	② 老位置（%AppData%\p2plink）已经有数据 ⇒ 继续用它 ✓
//	③ 都没有 ⇒ %AppData%（每用户目录）✓
//
// 🩸 **原来还有"优先别的固定盘"那一档，已经删掉** ✗
//
//	它在多用户共用的电脑上会把数据落到共享盘（实测 D:\ 的 ACL 是
//	`Authenticated Users:(OI)(CI)(IO)(M)` —— 所有本地用户可写），
//	而界面 HTML 每次都写在那儿、用 file:// 加载 ⇒
//	**任一本地用户都能改写它**，受害者下次启动就运行攻击者的 JS ✗
//	换来的是"不占 C 盘" —— 代价太大 ✗
//	📌 %AppData% 是 Windows 的**每用户**目录，系统保证只有本人（+管理员）能读写 ✓
//	想放别处的人，仍然可以在设置里自己选（那是他的选择）✓
func Parent() string {
	return chooseParent(strings.TrimSpace(readParent()), DefaultParent())
}

// chooseParent 是**纯函数**：把探测结果传进来，返回该用哪个。
// 本身不碰硬盘、不碰注册表 ⇒ 好测，也不会在测试里把用户的盘写坏 ✓
//
// 🩸 只剩两档：**用户指定过就用他指定的，否则一律 %AppData%** ✓
// （原来还有"优先别的固定盘"一档 —— 删掉的理由见 Parent() 上面那段）
func chooseParent(overridden, appData string) string {
	if overridden != "" {
		return overridden
	}
	return appData
}

// hasData 目录里有没有**文件**（空目录不算）。
func hasData(dir string) bool {
	if dir == "" {
		return false
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() {
			return true
		}
	}
	return false
}

// writable 往这个盘试写一个临时文件（写完就删）。
//
// 🩸 光看盘符不够：只读挂载、权限受限的盘一写就失败 ✗ 得**真试一下** ✓
// （用临时文件而不是建目录：试完不留痕迹 ✓）
func writable(root string) bool {
	f, err := os.CreateTemp(root, ".p2plink-probe-")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// listUsableDrives 由平台文件提供：列出"能拿来放数据的别的盘"（从 D 开始）。
// 做成变量是为了测试能顶掉 —— 不然测试会真去翻用户的硬盘 ✗
//
//	· Windows：只认**固定盘**（DRIVE_FIXED），光驱 / U 盘 / 网络盘一律排除 ✗
//	· 别的平台：返回空（= 用系统默认目录）
var listUsableDrives = func() []string { return nil }

// 🩸🩸 **别把这个接回选择逻辑**（2026-10-16 晚从 Parent() 里删掉了）：
//
//	别的固定盘（比如 D:\）在多用户共用的电脑上**所有本地用户可写**
//	（实测 ACL：`Authenticated Users:(OI)(CI)(IO)(M)`），
//	而界面 HTML 就写在数据目录里、用 file:// 加载 ⇒ 会被别人改写 ⇒ JS 注入 ✗
//	现在只剩调试入口 PickOtherDriveForDebug 用它 ✓
//
// pickOtherDrive 挑一个别的盘里**已经放了我们数据**的那个；
// 都没有的话挑第一个能写的。挑不到返回空串 ✓
func pickOtherDrive() string {
	drives := listUsableDrives()
	// 先找"已经有数据"的：重装 / 换版本时能直接接上，不用重新设置 ✓
	for _, d := range drives {
		if hasData(Subdir(d)) {
			return d
		}
	}
	// 再找第一个能写的
	for _, d := range drives {
		if writable(d) {
			return d
		}
	}
	return ""
}

// Subdir 返回某个父目录下我们自己的那层目录。
// 单独抽出来是为了让设置页能"预览"：选 D:\我的资料 ⇒ 会写到 D:\我的资料\p2plink\
func Subdir(parent string) string {
	if parent == "" {
		return ""
	}
	return filepath.Join(parent, subdir)
}

// EnvDir 环境变量名：设了就直接拿它当数据目录。
//
// 两个用途：
//
//	· **测试**：不然测试会写到用户真实的数据目录里 ✗ ——
//	  我加了"自动挑非 C 盘"之后，原来只顶 `APPDATA` 的测试就绕过它、
//	  直接写到 `D:\p2plink` 去了（真实发生过 ✓）
//	· 绿色版 / 便携用法：把数据放在程序旁边，U 盘一拔就走 ✓
const EnvDir = "P2PLINK_DATA_DIR"

// Dir 是数据目录 —— 全项目所有落盘的东西都走它 ✓
// 返回空串表示连系统默认目录都问不出来（极罕见），调用方自己兜底 ✓
func Dir() string {
	if d := strings.TrimSpace(os.Getenv(EnvDir)); d != "" {
		return d
	}
	return Subdir(Parent())
}

// SetParent 把数据目录换到 base 下面；base 传空串 = 恢复默认。
//
// 📌 它**只记位置、不搬文件** —— 搬迁是调用方的事（只有它知道要搬哪几个文件、
// 搬到一半失败了该怎么办）✓
func SetParent(base string) error {
	return writeParent(strings.TrimSpace(base))
}

// IsCustom 数据目录是不是用户自己改过的（设置页要据此显示"恢复默认" ✓）
func IsCustom() bool {
	return strings.TrimSpace(readParent()) != ""
}

// PickOtherDriveForDebug 只给 tmp/dircheck 那个小工具看的：
// 把"自动挑盘"的结果暴露出来，好确认它在真机器上挑到哪 ✓
func PickOtherDriveForDebug() string { return pickOtherDrive() }
