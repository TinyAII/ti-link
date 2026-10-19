package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withEvents 直接塞一串事件进去，绕开 histRecord 里的 time.Now ——
// 这样"谁几点进来"才能精确断言，而不是只能断言"有值" ✓
func withEvents(t *testing.T, events []histEvent) {
	t.Helper()
	h := &roomHistory{events: append([]histEvent(nil), events...)}
	histMu.Lock()
	old := hist
	hist = h
	histMu.Unlock()
	t.Cleanup(func() {
		histMu.Lock()
		hist = old
		histMu.Unlock()
	})
}

func evOpen(room, game string, acct int64, at int64) histEvent {
	return histEvent{T: at, Kind: histOpen, Room: room, Game: game, Acct: acct}
}
func evClose(room string, at int64) histEvent { return histEvent{T: at, Kind: histClose, Room: room} }
func evJoin(room, cid, label string, seat int, host bool, at int64) histEvent {
	return histEvent{T: at, Kind: histJoin, Room: room, CID: cid, Label: label, Seat: seat, Host: host}
}
func evLeave(room, cid, why string, at int64) histEvent {
	return histEvent{T: at, Kind: histLeave, Room: room, CID: cid, Why: why}
}

// writeLines 按行写文件。
// 📌 用 Fprintln 而不是自己拼换行符：源码里不出现转义序列，
// 测试数据的形状一眼能看清，也不会被"两层转义"折腾 ✓
func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	var buf bytes.Buffer
	for _, l := range lines {
		fmt.Fprintln(&buf, l)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 一局正常的：房主开房、两人进来、一人走了、关房。
func TestHistAggregateBasic(t *testing.T) {
	withEvents(t, []histEvent{
		evOpen("TI1", "我的世界", 7, 1000),
		evJoin("TI1", "h", "阿甲", 0, true, 1000),
		evJoin("TI1", "b", "小明", 1, false, 1010),
		evJoin("TI1", "c", "阿花", 2, false, 1020),
		evLeave("TI1", "b", "left", 1030),
		evClose("TI1", 1100),
	})

	rooms := histRooms("", 0)
	if len(rooms) != 1 {
		t.Fatalf("房间数 = %d，期望 1", len(rooms))
	}
	r := rooms[0]
	if r.ClosedAt != 1100 {
		t.Errorf("关闭时间 = %d，期望 1100", r.ClosedAt)
	}
	if r.Acct != 7 || r.Game != "我的世界" {
		t.Errorf("房主/游戏不对：%+v", r)
	}
	if r.MaxOnline != 3 {
		t.Errorf("最多同时在线 = %d，期望 3", r.MaxOnline)
	}
	if len(r.Members) != 3 {
		t.Fatalf("成员 %d 条，期望 3", len(r.Members))
	}
	var b *histMember
	for i := range r.Members {
		if r.Members[i].CID == "b" {
			b = &r.Members[i]
		}
	}
	if b == nil || b.LeftAt != 1030 || b.Why != "left" {
		t.Errorf("小明的离开时间/原因不对：%+v", b)
	}
}

// 走了又回来 ⇒ 两条记录，各自的时间边界
func TestHistRejoinTwoIntervals(t *testing.T) {
	withEvents(t, []histEvent{
		evOpen("TI1", "A", 1, 100),
		evJoin("TI1", "x", "某人", 1, false, 110),
		evLeave("TI1", "x", "gone", 120),
		evJoin("TI1", "x", "某人", 1, false, 130),
		evLeave("TI1", "x", "left", 140),
	})
	r := histRooms("", 0)[0]
	var xs []histMember
	for _, m := range r.Members {
		if m.CID == "x" {
			xs = append(xs, m)
		}
	}
	if len(xs) != 2 {
		t.Fatalf("同一个人走了又回来应该是 2 条，实际 %d", len(xs))
	}
	if xs[0].LeftAt != 120 || xs[1].JoinedAt != 130 {
		t.Errorf("两段的时间边界不对：%+v", xs)
	}
}

// 同一个房间号被用了两次 ⇒ 两次要分开算（不能糊成一局）
func TestHistRoomCodeReuse(t *testing.T) {
	withEvents(t, []histEvent{
		evOpen("TI1", "A", 1, 100),
		evJoin("TI1", "p", "早的", 1, false, 105),
		evClose("TI1", 200),
		evOpen("TI1", "B", 2, 300),
		evJoin("TI1", "q", "晚的", 1, false, 305),
		evClose("TI1", 400),
	})
	rooms := histRooms("", 0)
	if len(rooms) != 2 {
		t.Fatalf("房间数 = %d，期望 2（同一个号开了两次）", len(rooms))
	}
	// 最近的在前
	if rooms[0].OpenedAt != 300 || rooms[0].Game != "B" {
		t.Errorf("第一条应该是最新那次：%+v", rooms[0])
	}
	if len(rooms[0].Members) == 0 || rooms[0].Members[0].Label != "晚的" {
		t.Errorf("事件归错房间了：%+v", rooms[0].Members)
	}
	if len(rooms[1].Members) == 0 || rooms[1].Members[0].Label != "早的" {
		t.Errorf("事件归错房间了：%+v", rooms[1].Members)
	}
}

// 搜索：房间号 / 游戏 / 成员昵称，大小写不敏感
func TestHistSearch(t *testing.T) {
	withEvents(t, []histEvent{
		evOpen("TIABC", "泰拉瑞亚", 1, 100),
		evJoin("TIABC", "a", "小明", 1, false, 101),
		evOpen("TIXYZ", "我的世界", 2, 200),
		evJoin("TIXYZ", "b", "阿花", 1, false, 201),
	})
	if n := len(histRooms("tiabc", 0)); n != 1 {
		t.Errorf("按房间号搜：%d 条，期望 1", n)
	}
	if n := len(histRooms("泰拉", 0)); n != 1 {
		t.Errorf("按游戏搜：%d 条，期望 1", n)
	}
	if n := len(histRooms("小明", 0)); n != 1 {
		t.Errorf("按昵称搜：%d 条，期望 1", n)
	}
	if n := len(histRooms("不存在", 0)); n != 0 {
		t.Errorf("搜不到却返回了 %d 条", n)
	}
}

// 30 天前的记录要被清掉，而且**文件也要跟着变小**（不能只在内存里删）
func TestHistPruneDropsOldAndRewritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, histFile)

	oldT := fmt.Sprint(time.Now().Add(-40 * 24 * time.Hour).Unix())
	newT := fmt.Sprint(time.Now().Add(-1 * time.Hour).Unix())
	writeLines(t, path,
		`{"t":`+oldT+`,"k":"open","r":"OLD","g":"X","a":1}`,
		`{"t":`+newT+`,"k":"open","r":"NEW","g":"Y","a":2}`,
	)

	histInit(dir)
	t.Cleanup(func() { histMu.Lock(); hist = nil; histMu.Unlock() })

	rooms := histRooms("", 0)
	if len(rooms) != 1 || rooms[0].Code != "NEW" {
		t.Fatalf("裁剪后应该只剩 NEW，实际 %+v", rooms)
	}
	// 文件也得真的被重写 —— 否则下次启动又把老的读回来 ✗
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "NEW") || strings.Contains(string(b), "OLD") {
		t.Errorf("落盘文件没被重写：%s", string(b))
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("重写后还留着 .tmp")
	}
}

// 文件里混了半行（上次写盘被中断）不能整份作废
func TestHistSkipsBrokenLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, histFile)

	// ⚠️ 时间戳必须是**相对现在**的：写成 100/200 那种会被 30 天裁剪先清掉，
	//    那样测出来的"0 间"是裁剪造成的，跟坏行一点关系都没有 ✗（第一次就这么红的）
	t1 := fmt.Sprint(time.Now().Add(-2 * time.Hour).Unix())
	t2 := fmt.Sprint(time.Now().Add(-1 * time.Hour).Unix())
	writeLines(t, path,
		`{"t":`+t1+`,"k":"open","r":"OK1","g":"A","a":1}`,
		`{"t":`+t2+`,"k":"op`, // 半行：故意截断
		`{"t":`+t2+`,"k":"open","r":"OK2","g":"B","a":2}`,
	)

	histInit(dir)
	t.Cleanup(func() { histMu.Lock(); hist = nil; histMu.Unlock() })

	rooms := histRooms("", 0)
	if len(rooms) != 2 {
		t.Fatalf("坏行应该被跳过、好行都留着，实际 %d 间", len(rooms))
	}
}

// 没有历史（hist 为 nil）时查询要返回空，不能 panic ——
// 服务刚起来还没 init、或者没配数据目录时就是这种情况 ✓
func TestHistNilSafe(t *testing.T) {
	histMu.Lock()
	old := hist
	hist = nil
	histMu.Unlock()
	t.Cleanup(func() { histMu.Lock(); hist = old; histMu.Unlock() })

	if got := histRooms("", 0); got != nil {
		t.Errorf("没有记录器时该返回 nil，实际 %v", got)
	}
	histRecord(evOpen("X", "Y", 1, 0)) // 不能 panic
}

// LastAt 要停在"最后一条记录"上 —— 没结束时间的房间靠它算时长。
//
// 🩸 2026-10-17：老板看到「时长 35 分钟（未正常结束）」还一直涨。
// 根因是前端拿 now 兜底；但后端得给出一个**固定**的参照（最后一条事件），
// 否则前端只能瞎猜 ✓
func TestHistLastAt(t *testing.T) {
	// ① 正常关房：LastAt 应该是关房那一刻
	withEvents(t, []histEvent{
		evOpen("TI1", "A", 1, 100),
		evJoin("TI1", "a", "甲", 0, true, 140),
		evClose("TI1", 190),
	})
	if got := histRooms("", 0)[0].LastAt; got != 190 {
		t.Errorf("有 close 时 LastAt = %d，期望 190", got)
	}

	// ② 没有 close（服务器重启 / 进程被杀）：停在最后一条成员事件上，**不是 0**
	withEvents(t, []histEvent{
		evOpen("TI2", "B", 2, 100),
		evJoin("TI2", "b", "乙", 0, true, 100),
		evJoin("TI2", "c", "丙", 1, false, 260),
		evLeave("TI2", "c", "left", 300),
	})
	if got := histRooms("", 0)[0].LastAt; got != 300 {
		t.Errorf("没有 close 时 LastAt = %d，期望 300（最后一条事件）", got)
	}

	// ③ 只有 open 一条：LastAt 至少等于开始时间，不能是 0
	withEvents(t, []histEvent{evOpen("TI3", "C", 3, 500)})
	if got := histRooms("", 0)[0].LastAt; got != 500 {
		t.Errorf("只有 open 时 LastAt = %d，期望 500", got)
	}
}
