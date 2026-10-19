// 日志的收集与导出。
//
// 🩸 两条铁律：
//  1. 界面（和「复制日志」）只看 human 版 —— 一个字地址都不含
//  2. **完整 IP 不允许出现在客户端的任何地方**：导出里的 detail 也是打过码的
//     （`120.227.*.*:1962` 这种，前两段 + 端口）
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// logEntry 一条日志。Human 给人看，Detail 带（打码后的）地址，只在导出勾选时用。
type logEntry struct {
	At time.Time `json:"at"`
	// Human 为空表示"这条只进导出、不在界面上出现"（human=="" 的约定，见 logPair）。
	// 加 omitempty 是免得导出的 JSON 里一堆 "message": ""。
	Human  string `json:"message,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// logStore 攒一份日志供导出。界面走的是 emit，这里是给"导出"留的底。
type logStore struct {
	mu      sync.Mutex
	entries []logEntry
}

// skipReset 见 keepNextReset。
func (s *logStore) add(human, detail string) {
	if human == "" && detail == "" {
		return
	}
	s.mu.Lock()
	s.entries = append(s.entries, logEntry{At: time.Now(), Human: human, Detail: detail})
	s.mu.Unlock()
}

// addDetailOnly 存一条**只进导出**的日志（界面不显示，见 logPair 的 human=="" 约定）。
func (s *logStore) addDetailOnly(detail string) {
	s.add("", detail)
}

func (s *logStore) snapshot() []logEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]logEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// reset 换会话时清空（免得把上一局的日志混进来）。
//
// 📌 注意：**「重新连接」不再走这条路**，所以这里不需要"跳过清空"的开关 ✓
//
//	2026-10-18 之后 RetryJoin 直接走 beginJoin（不经过 JoinRoom），
//	压根碰不到 reset —— 原来那个 keepNextReset/skipReset 就成了没人拨的开关，
//	留着只会让后来的人以为它在起作用 ✗ 已删。
func (s *logStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
}

/* ==================== 导出 ==================== */

type exportMeta struct {
	Mode     string // 创建房间 / 加入房间
	Room     string
	WithAddr bool
	At       time.Time

	// Env 是运行环境（系统版本 / IPv6 开关 / 网卡数 …）。
	// Snap 是导出时**自动跑一遍**的四项自检结论。
	//
	// 🩸 为什么要带这两段（2026-10-18）：光看连接日志，
	//    "对面什么系统、有没有 IPv6、开没开加速器"全查不出来 ——
	//    今晚花了半小时才问出"他网卡上根本没 IPv6"这种信息 ✗
	//    带上之后，用户点一次导出就把该问的全带走了 ✓
	Env  []string
	Snap []string
}

// exportDir 挑一个用户一眼能找到的地方：桌面；没有就退回主目录。
// exportDir 是"日志导到哪里"。
// 📌 做成变量是为了测试能顶掉它 —— 不然跑一次测试就在用户的桌面上拉一个文件 ✗
var exportDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	desktop := filepath.Join(home, "Desktop")
	if st, err := os.Stat(desktop); err == nil && st.IsDir() {
		return desktop
	}
	return home
}

// ExportLog 由页面调用：把日志导出成**用户选的那种格式**，只出一个文件。
//
//	format: "md" / "txt" / "json"（认不出来就当 txt）
//	withAddr=true 时用带（打码）地址的版本 —— 也就是 detail。
//
// 返回一句给用户看的提示文案。
func (b *backend) ExportLog(withAddr bool, format string) string {
	entries := b.logs.snapshot()
	if len(entries) == 0 {
		return "还没有日志可以导出"
	}

	b.mu.Lock()
	mode, room := b.mode, b.room
	b.mu.Unlock()

	at := time.Now()
	base := "TI联机日志-" + at.Format("20060102-1504")
	dir := exportDir()
	meta := exportMeta{Mode: mode, Room: room, WithAddr: withAddr, At: at}
	// 🩸 导出时**顺便**把环境和四项自检带上（2026-10-18）：
	//    这些信息用户平时不会主动去测，出事时又最需要 ✗
	//    自检要连 STUN，最坏几秒 —— diagSnapshot 里是四项并行跑的 ✓
	meta.Env = envLines()
	meta.Snap = b.diagSnapshot()

	body, ext := renderLog(entries, meta, format)
	p := filepath.Join(dir, base+ext)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return "导出失败：" + err.Error()
	}
	// 🩸 别硬写"桌面"：桌面不可用时会退到 home 或临时目录，
	//    那时候还说"已导出到桌面"就是假话，用户会去桌面翻半天 ✗
	if home, err := os.UserHomeDir(); err == nil && dir == filepath.Join(home, "Desktop") {
		return "已导出到桌面：" + filepath.Base(p)
	}
	return "已导出到 " + dir + "：" + filepath.Base(p)
}

// renderLog 按格式出内容。format 认不出来就回退纯文本。
func renderLog(entries []logEntry, meta exportMeta, format string) (body, ext string) {
	switch format {
	case "md", "markdown":
		return renderLogMarkdown(entries, meta), ".md"
	case "json":
		return renderLogJSON(entries, meta), ".json"
	default:
		return renderLogText(entries, meta), ".txt"
	}
}

// logLine 是 txt / md 里的一行，第二个返回值表示"这一行要不要输出"。
//
// Human 为空的条目是"只进导出"的过程日志：**没勾选含地址就直接跳过**，
// 否则会输出一行空消息（而且那种条目的内容恰恰就是地址，不勾选时不该露出来）。
func logLine(e logEntry, withAddr bool) (string, bool) {
	if e.Human == "" {
		if !withAddr {
			return "", false
		}
		return e.At.Format("15:04:05") + " " + e.Detail, true
	}
	msg := e.Human
	if withAddr && e.Detail != "" && e.Detail != e.Human {
		msg += "　（" + e.Detail + "）"
	}
	return e.At.Format("15:04:05") + " " + msg, true
}

func sessionLine(meta exportMeta) string {
	if meta.Mode == "" {
		return ""
	}
	s := meta.Mode
	if meta.Room != "" {
		s += " · 房间 ID " + meta.Room
	}
	return s
}

func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func renderLogText(entries []logEntry, meta exportMeta) string {
	var b strings.Builder
	b.WriteString("TI 联机日志\n")
	b.WriteString("导出时间：" + meta.At.Format("2006-01-02 15:04:05") + "\n")
	b.WriteString("软件版本：v" + appVersion + "\n")
	if s := sessionLine(meta); s != "" {
		b.WriteString("本次会话：" + s + "\n")
	}
	b.WriteString("含连接地址：" + yesNo(meta.WithAddr) + "\n")
	writeSectionText(&b, "环境", meta.Env)
	writeSectionText(&b, "自检", meta.Snap)
	b.WriteString(strings.Repeat("-", 46) + "\n")
	for _, e := range entries {
		if line, ok := logLine(e, meta.WithAddr); ok {
			b.WriteString("[" + line + "]\n")
		}
	}
	return b.String()
}

func renderLogMarkdown(entries []logEntry, meta exportMeta) string {
	var b strings.Builder
	b.WriteString("# TI 联机日志\n\n")
	b.WriteString("| 项 | 内容 |\n| --- | --- |\n")
	b.WriteString("| 导出时间 | " + meta.At.Format("2006-01-02 15:04:05") + " |\n")
	b.WriteString("| 软件版本 | v" + appVersion + " |\n")
	if s := sessionLine(meta); s != "" {
		b.WriteString("| 本次会话 | " + s + " |\n")
	}
	b.WriteString("| 含连接地址 | " + yesNo(meta.WithAddr) + " |\n\n")
	writeSectionMD(&b, "环境", meta.Env)
	writeSectionMD(&b, "自检", meta.Snap)
	b.WriteString("## 日志\n\n")
	for _, e := range entries {
		if line, ok := logLine(e, meta.WithAddr); ok {
			b.WriteString("- `" + line + "`\n")
		}
	}
	return b.String()
}

type logJSONDoc struct {
	ExportedAt     string     `json:"exportedAt"`
	Version        string     `json:"version"`
	Mode           string     `json:"mode,omitempty"`
	Room           string     `json:"room,omitempty"`
	IncludeAddress bool       `json:"includeAddress"`
	Note           string     `json:"note"`
	Env            []string   `json:"env,omitempty"`
	Snapshot       []string   `json:"snapshot,omitempty"`
	Entries        []logEntry `json:"entries"`
}

func renderLogJSON(entries []logEntry, meta exportMeta) string {
	es := make([]logEntry, 0, len(entries))
	for _, e := range entries {
		// 没勾选就根本不把 detail 写进去（而不是写个空字段）
		if !meta.WithAddr {
			// "只进导出"的条目（Human 为空）在没勾选时整条跳过 ——
			// 它的内容就是地址，不勾选就不该出现。
			if e.Human == "" {
				continue
			}
			e.Detail = ""
		}
		es = append(es, e)
	}
	doc := logJSONDoc{
		ExportedAt:     meta.At.Format(time.RFC3339),
		Version:        "v" + appVersion,
		Mode:           meta.Mode,
		Room:           meta.Room,
		IncludeAddress: meta.WithAddr,
		Note:           "含地址时也只保留 IP 的前两段和端口；完整地址不会出现在客户端。",
		Env:            meta.Env,
		Snapshot:       meta.Snap,
		Entries:        es,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b) + "\n"
}
