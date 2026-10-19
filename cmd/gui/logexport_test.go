package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 导出日志：内容能写出来、路径说得如实、失败要能看出来。
//
// 🩸 2026-10-18 老板报「运行日志点击导出失败」——
// 真因不在导出本身，而是**按钮根本没绑上事件**（见下面 TestExportLogEntryWired）。
// 这条测试守住"导出这个动作本身是好的"，出事时能立刻分清是功能坏了还是没接上 ✓
func TestExportLogWrites(t *testing.T) {
	dir := t.TempDir()
	old := exportDir
	exportDir = func() string { return dir }
	t.Cleanup(func() { exportDir = old })

	b := &backend{}
	b.logs.add("房间已创建", "房间 TI123")
	b.logs.addDetailOnly("STUN 探测成功: 1.2.*.*:5000")

	msg := b.ExportLog(false, "md")
	if !strings.HasPrefix(msg, "已导出") {
		t.Fatalf("导出没成功：%s", msg)
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("目录里应该正好 1 个文件，实际 %d：%s", len(files), msg)
	}
	p := filepath.Join(dir, files[0].Name())

	// 提示里必须带上**真实目录** —— 桌面不可用时会退到别处，
	// 那时候还说"已导出到桌面"，用户会去桌面翻半天 ✗
	if !strings.Contains(msg, dir) {
		t.Errorf("提示里没说清导到哪儿了（实际目录 %s）：%s", dir, msg)
	}

	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读不出来：%v", err)
	}
	if !strings.Contains(string(body), "房间已创建") {
		t.Error("导出内容里没有普通日志")
	}
	// 没勾「含地址」时，只进导出的 detail 条目**不该出现**
	if strings.Contains(string(body), "STUN 探测成功") {
		t.Error("没勾含地址，却把 detail 条目写进去了")
	}
}

// 勾了「含地址」才带 detail（而且那份也是打过码的）
func TestExportLogWithAddr(t *testing.T) {
	dir := t.TempDir()
	old := exportDir
	exportDir = func() string { return dir }
	t.Cleanup(func() { exportDir = old })

	b := &backend{}
	b.logs.addDetailOnly("收到房主候选 = 1.2.*.*:5000")

	if msg := b.ExportLog(false, "txt"); !strings.HasPrefix(msg, "已导出") {
		t.Fatalf("导出失败：%s", msg)
	}
	files, _ := os.ReadDir(dir)
	body, _ := os.ReadFile(filepath.Join(dir, files[0].Name()))
	if strings.Contains(string(body), "收到房主候选") {
		t.Fatal("没勾含地址却写出了 detail")
	}

	if msg := b.ExportLog(true, "txt"); !strings.HasPrefix(msg, "已导出") {
		t.Fatalf("导出失败：%s", msg)
	}
	files, _ = os.ReadDir(dir)
	newest := ""
	for _, f := range files {
		if f.Name() > newest {
			newest = f.Name()
		}
	}
	body, _ = os.ReadFile(filepath.Join(dir, newest))
	if !strings.Contains(string(body), "收到房主候选") {
		t.Error("勾了含地址，detail 却没写进去")
	}
}

// 🩸🩸 这条是补 2026-10-18 那个 bug：**HTML 上的导出按钮压根没被 JS 绑上**。
//
// 原来 JS 是按 id 拼的：`$('exportLog' + s)`，s 取 Create / Join。
// 「独立房间页」改版把按钮改成了 `exportLogRoom`，JS 没跟着改
// ⇒ 找的是两个不存在的 id，`if (b)` 又把 null 挡掉了 ⇒ 点了完全没反应，
// 而且不报错，看起来就像"导出失败" ✗
//
// 现在改成按 `data-export-log` 属性绑：加按钮不用改 JS，也就不会再对不上 ✓
func TestExportLogEntryWired(t *testing.T) {
	html, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatalf("读不到页面：%v", err)
	}
	if !strings.Contains(string(html), "data-export-log") {
		t.Fatal("页面上没有 data-export-log 的入口 —— 用户就点不到导出")
	}

	js, err := os.ReadFile("ui/js/07-room.js")
	if err != nil {
		t.Fatalf("读不到 07-room.js：%v", err)
	}
	if !strings.Contains(string(js), "data-export-log") {
		t.Fatal("JS 没有按 data-export-log 绑定 —— 按钮会变成点了没反应")
	}

	// 别再退回"按 id 拼"的老写法（只看代码，不看注释 ——
	// 注释里为了说明问题会引用旧代码，不能因此判定"又用回去了"）
	if strings.Contains(stripLineComments(string(js)), "exportLog' + s") {
		t.Error("又用 id 拼了：改版时一定会对不上，用 data 属性")
	}
}

// stripLineComments 去掉 JS 的 // 行注释（保留 https:// 里的双斜杠）。
// 只是为了上面那条测试能分清"代码里又用回去了"和"注释里引用了旧代码"。
func stripLineComments(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "//"); i >= 0 && (i == 0 || line[i-1] != ':') {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// 导出里必须带「环境」和「自检」两段。
//
// 🩸 这两段是 2026-10-18 专门加的：光看连接日志，
//
//	"对面什么系统、有没有 IPv6、开没开加速器"全查不出来，
//	只能来回问 —— 而这几样恰恰是最常见的故障原因 ✗
//	以后谁改渲染器改丢了一段，这条会拦住 ✓
func TestExportHasEnvAndSnapshot(t *testing.T) {
	entries := []logEntry{
		{At: time.Now(), Human: "加入了房间", Detail: "隧道就绪"},
	}
	meta := exportMeta{
		Mode: "加入房间", Room: "TI214372GV", At: time.Now(),
		Env:  []string{"系统：Windows 11（Build 26200）", "IPv6 协议：已启用"},
		Snap: []string{"网络类型：锥型 NAT", "穿透服务器：能连上（40 ms）"},
	}

	md := renderLogMarkdown(entries, meta)
	for _, want := range []string{"## 环境", "## 自检", "Windows 11", "锥型 NAT"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown 导出缺了 %q\n%s", want, md)
		}
	}
	// 两段要排在日志**前面**：先看结论，再看过程 ✓
	if strings.Index(md, "## 自检") > strings.Index(md, "## 日志") {
		t.Error("环境/自检应该排在日志前面")
	}

	txt := renderLogText(entries, meta)
	for _, want := range []string{"环境：", "自检：", "锥型 NAT"} {
		if !strings.Contains(txt, want) {
			t.Errorf("纯文本导出缺了 %q\n%s", want, txt)
		}
	}

	js := renderLogJSON(entries, meta)
	for _, want := range []string{`"env"`, `"snapshot"`, "锥型 NAT"} {
		if !strings.Contains(js, want) {
			t.Errorf("JSON 导出缺了 %q\n%s", want, js)
		}
	}
}
