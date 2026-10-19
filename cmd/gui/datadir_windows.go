// 「换数据目录」：让用户挑一个父目录，把老文件搬过去，再把新位置记下来。
//
// 📌 用户挑的是**父目录** ⇒ 文件落在 `<父目录>\p2plink\` 里 ✓
// 不往人家文件夹根上撒东西（老板明确要的"更整洁" ✓）
//
// 🩸 顺序要紧：**先搬成功、再记新位置** ✓
// 反过来的话，搬一半失败了设置已经指到新地方 ⇒ 老文件在新地方找不到，人以为丢数据了 ✗
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"p2plink/internal/appdir"
)

// ChooseDataDir 由设置页调用：弹框选目录 + 搬家。
// 返回错误文案（空串 = 成功或者用户取消）。
func (b *backend) ChooseDataDir() string {
	sel := pickFolder("选一个文件夹：本工具的设置和身份信息会放进它下面的 p2plink 目录")
	if sel == "" {
		return "" // 用户取消 —— 什么都不做，也别报错
	}
	if filepath.Clean(sel) == filepath.Clean(appdir.Parent()) {
		return "" // 选的就是现在这个，不用折腾
	}

	oldDir, newDir := appdir.Dir(), appdir.Subdir(sel)
	if oldDir == "" || newDir == "" {
		return "找不到数据目录，换不了"
	}

	if err := copyDataFiles(oldDir, newDir); err != nil {
		// 搬失败 ⇒ **不改设置**，继续用老目录 ✓ 两头保持一致
		return "搬家失败，还是用原来的目录：" + err.Error()
	}
	if err := appdir.SetParent(sel); err != nil {
		return "新位置没记下来：" + err.Error()
	}

	// 让新目录里那份设置也跟上（缓存里那份要写回到新位置）✓
	_ = updateSettings(func(*Settings) {})
	return ""
}

// ResetDataDir 回到默认位置（%AppData%\p2plink）。
func (b *backend) ResetDataDir() string {
	if !appdir.IsCustom() {
		return ""
	}
	oldDir, newDir := appdir.Dir(), appdir.Subdir(appdir.DefaultParent())
	if oldDir != newDir {
		if err := copyDataFiles(oldDir, newDir); err != nil {
			return "搬回默认位置失败：" + err.Error()
		}
	}
	if err := appdir.SetParent(""); err != nil {
		return "没记下来：" + err.Error()
	}
	_ = updateSettings(func(*Settings) {})
	return ""
}

// copyDataFiles 把数据目录里的**文件**复制过去（不删老的 ✓）。
//
// 🩸 **老目录不删**：那是用户数据，留着当备份 ✓ 大不了自己删 ✓
// 真删了又出问题，那就不是"设置没改对"而是"东西没了" ✗ 两回事。
func copyDataFiles(from, to string) error {
	if _, err := os.Stat(from); os.IsNotExist(err) {
		return nil // 老目录都还没有 ⇒ 没什么可搬的
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return err
	}
	ents, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue // 数据目录里本来就都是文件，不管子目录
		}
		src := filepath.Join(from, e.Name())
		dst := filepath.Join(to, e.Name())
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("%s：%w", e.Name(), err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// 先写临时文件再改名 —— 搬到一半断电也不会留下半个文件 ✗
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
