// 「换数据目录」里可测的那部分：搬文件。
//
// 原生文件夹选择框没法自动点 ✗ 但搬文件这段是纯文件操作，能测 ✓
// 而且这段最要命 —— 搬坏了就是"用户以为数据丢了" ✗
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyDataFiles(t *testing.T) {
	from := t.TempDir()
	to := filepath.Join(t.TempDir(), "p2plink") // 目标还不存在，得自己建 ✓

	// 造几个像样的文件（跟真实数据目录里那几样同名）
	files := map[string]string{
		"settings.json":    `{"theme":"dark"}`,
		"client-id":        "0123456789abcdef",
		"closechoice.json": `{"action":"tray"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(from, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := copyDataFiles(from, to); err != nil {
		t.Fatalf("搬失败: %v", err)
	}

	for name, want := range files {
		b, err := os.ReadFile(filepath.Join(to, name))
		if err != nil {
			t.Errorf("%s 没搬过去: %v", name, err)
			continue
		}
		if string(b) != want {
			t.Errorf("%s 内容不对：%q", name, b)
		}
	}

	// 🩸 老目录**不能被删** —— 那是用户数据，留着当备份 ✓
	if _, err := os.Stat(filepath.Join(from, "settings.json")); err != nil {
		t.Errorf("老目录里的文件不该被删掉（留作备份）：%v", err)
	}
	// 也不该留下 .tmp 半成品
	ents, _ := os.ReadDir(to)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("留下了临时文件：%s", e.Name())
		}
	}
}

// 老目录压根不存在时（全新安装就换位置）⇒ 不该报错，直接算成功 ✓
func TestCopyDataFilesMissingSource(t *testing.T) {
	to := filepath.Join(t.TempDir(), "p2plink")
	if err := copyDataFiles(filepath.Join(t.TempDir(), "不存在"), to); err != nil {
		t.Fatalf("老目录不存在时不该报错，实得: %v", err)
	}
}
