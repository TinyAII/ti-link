// 让用户挑一个文件夹（用来改数据目录的位置）。
//
// 用的是 shell32 的 SHBrowseForFolderW —— 比 COM 那套 IFileDialog 省事得多，
// 而且不需要初始化 OLE ✓ BIF_NEWDIALOGSTYLE 能让它长得不那么 90 年代 ✓
package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	bifReturnOnlyFSDirs = 0x0001 // 只能选文件夹，不能选文件
	bifNewDialogStyle   = 0x0040 // 新式对话框（可拖动、能新建文件夹）
)

var (
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	procSHBrowseForFolderW  = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree       = windows.NewLazySystemDLL("ole32.dll").NewProc("CoTaskMemFree")
)

// browseInfo 对应 Windows 的 BROWSEINFOW（只用到前几个字段）。
type browseInfo struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

// pickFolder 弹一个"选择文件夹"的框；用户取消就返回空串 ✓
//
// title 是框上那句提示。起始位置**不预置**：SHBrowseForFolder 只能靠
// BFFM_INITIALIZED 回调塞进去（很绕），而系统本来就会记住用户上次停在哪 ——
// 对用户来说那样更顺手 ✓
func pickFolder(title string) string {
	t, _ := windows.UTF16PtrFromString(title)

	var display [260]uint16
	bi := browseInfo{
		pszDisplayName: &display[0],
		lpszTitle:      t,
		ulFlags:        bifReturnOnlyFSDirs | bifNewDialogStyle,
	}

	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return "" // 用户点了取消
	}
	defer procCoTaskMemFree.Call(pidl)

	var buf [260]uint16
	ok, _, _ := procSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:])
}
