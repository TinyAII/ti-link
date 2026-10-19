// /rooms 快照的测试。
//
// 🩸 这个端点是**唯一**会把"房间里的事"亮出来的地方，所以最要紧的断言不是
// "字段对不对"，而是**"有没有多给"**：来源地址必须是打码的、
// 不许出现 clientID 之类能直接认人的东西 ✓
package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var roomsNow = time.Date(2026, 10, 15, 23, 0, 0, 0, time.UTC)

func mkRoom(code, hostIP string, maxSize int, label string, accountID int64, openAgo time.Duration) *room {
	r := &room{
		code: code, maxSize: maxSize, bans: map[string]*ban{},
		hostIP: hostIP, openedAt: roomsNow.Add(-openAgo), accountID: accountID,
	}
	r.host = &member{id: "id-" + code, cid: "cid-" + code, label: label, seat: 0}
	return r
}

// 空列表该是 `[]` 而不是 `null` —— 前端那边 .length 直接就能用。
func TestRoomsSnapshotOfEmpty(t *testing.T) {
	got := roomsSnapshotOf(nil, roomsNow)
	if got == nil {
		t.Fatal("空的时候该给个空切片（JSON 里是 []），不是 nil（null）✗")
	}
	if len(got) != 0 {
		t.Fatalf("该是空的，实得 %d 条", len(got))
	}
}

// ⭐⭐ 最要紧的一条：**来源地址必须是打码的**。
//
// 这条红了就意味着"后台页面上能看到用户的完整公网 IP"——
// 而那个页面是为了"追责"做的，不是为了"看谁在哪" ✗
// 反证方法：把 roomsSnapshotOf 里的 netutil.MaskAddr 去掉，这条立刻红 ✓
func TestRoomsSnapshotMasksSourceAddr(t *testing.T) {
	r := mkRoom("T-MASK01", "120.227.45.9", 4, "", 7, time.Minute)
	got := roomsSnapshotOf([]*room{r}, roomsNow)
	if len(got) != 1 {
		t.Fatalf("该 1 条，实得 %d", len(got))
	}
	if got[0].From == "120.227.45.9" {
		t.Fatal("来源地址原样给出去了 —— 该打码成 120.227.*.* ✗")
	}
	if got[0].From != "120.227.*.*" {
		t.Errorf("打码结果该是 120.227.*.*，实得 %q", got[0].From)
	}
	if strings.Contains(got[0].From, "45") {
		t.Errorf("打码之后不该还剩主机号：%q", got[0].From)
	}
}

// 字段对不对（后台那张表要按这些列渲染）。
func TestRoomsSnapshotFields(t *testing.T) {
	r := mkRoom("T-FIELDS", "120.227.45.9", 6, "", 3, 90*time.Second)
	// 加两个朋友进去
	r.joins = []*member{
		{id: "j1", cid: "c1", seat: 1},
		{id: "j2", cid: "c2", seat: 2},
	}
	got := roomsSnapshotOf([]*room{r}, roomsNow)[0]
	if got.Members != 3 {
		t.Errorf("人数该是 1 房主 + 2 朋友 = 3，实得 %d", got.Members)
	}
	if got.MaxSize != 6 {
		t.Errorf("上限该是 6，实得 %d", got.MaxSize)
	}
	if got.OpenSeconds != 90 {
		t.Errorf("开了 90 秒，实得 %d", got.OpenSeconds)
	}
	if got.AccountID != 3 {
		t.Errorf("账号号该是 3，实得 %d", got.AccountID)
	}
}

// 房主起过名字 → 显示「房主 某某」；没起名 → 就是「房主」。
//
// 📌 昵称是**房间里本来就公开**的东西（同房的人都看得到），不算新暴露 ✓
func TestRoomsSnapshotHostName(t *testing.T) {
	named := mkRoom("T-NAME1", "1.2.3.4", 4, "沐恩", 0, time.Minute)
	if got := roomsSnapshotOf([]*room{named}, roomsNow)[0].Host; !strings.Contains(got, "沐恩") {
		t.Errorf("起过名就该显示名字，实得 %q", got)
	}
	anon := mkRoom("T-NAME2", "1.2.3.4", 4, "", 0, time.Minute)
	if got := roomsSnapshotOf([]*room{anon}, roomsNow)[0].Host; got != "房主" {
		t.Errorf("没起名就该只显示「房主」，实得 %q", got)
	}
}

// 房主正在断开（host 已经置 nil）时不能崩 —— 那是个真实存在的瞬间状态。
func TestRoomsSnapshotHostGone(t *testing.T) {
	r := mkRoom("T-GONE01", "1.2.3.4", 4, "", 0, time.Minute)
	r.host = nil
	r.joins = []*member{{id: "j", cid: "c", seat: 1}}
	got := roomsSnapshotOf([]*room{r}, roomsNow)
	if len(got) != 1 || got[0].Members != 1 {
		t.Fatalf("房主没了的时候该按剩下的朋友数报，实得 %+v", got)
	}
	if got[0].Host != "" {
		t.Errorf("房主没了就不该有房主名，实得 %q", got[0].Host)
	}
}

// 新开的排前面。
func TestRoomsSnapshotSortedNewestFirst(t *testing.T) {
	old := mkRoom("T-OLD001", "1.2.3.4", 4, "", 0, time.Hour)
	mid := mkRoom("T-MID001", "1.2.3.4", 4, "", 0, 10*time.Minute)
	fresh := mkRoom("T-NEW001", "1.2.3.4", 4, "", 0, 5*time.Second)
	got := roomsSnapshotOf([]*room{old, mid, fresh}, roomsNow)
	want := []string{"T-NEW001", "T-MID001", "T-OLD001"}
	for i := range want {
		if got[i].Room != want[i] {
			t.Fatalf("排序不对：第 %d 个该是 %s，实得 %s（全表 %v）", i, want[i], got[i].Room, got)
		}
	}
}

// ---- HTTP 那一层 ----

func TestRoomsHandlerJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	roomsHandler(rec, httptest.NewRequest("GET", "/rooms", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Content-Type 该是 json，实得 %q", ct)
	}
	var got roomsSnapshotResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("返回的不是合法 JSON: %v\n%s", err, rec.Body.String())
	}
	// 形状必须是 {"rooms": [...]} —— 管理面板读的就是 .rooms
	if !strings.Contains(rec.Body.String(), `"rooms"`) {
		t.Errorf("响应里该有 rooms 字段：%s", rec.Body.String())
	}
}

// 🩸 纪律测试：响应里**不许**出现能直接认人的东西。
//
// /stats 那边有一条同样的（TestStatsHasNoPerUserFields）；这条是 /rooms 的版本。
// /rooms 比 /stats 松一点 —— 它本来就给房主昵称（房间里公开的）和**打码**的来源，
// 但完整 IP、clientID、设备 ID 这些一律不许出现 ✓
func TestRoomsHasNoRawIdentity(t *testing.T) {
	// 直接拿一份快照序列化出来查 —— 不依赖全局房间表里恰好有什么
	got := roomsSnapshotOf([]*room{
		mkRoom("T-LEAK01", "120.227.45.9", 4, "沐恩", 42, time.Minute),
	}, roomsNow)
	b, err := json.Marshal(roomsSnapshotResp{Rooms: got})
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.ToLower(string(b))

	for _, forbidden := range []string{"120.227.45.9", "clientid", "cid-", "id-t-leak01", "token"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("快照里出现了 %q —— 这个端点只能给「打码的来源 + 房间公开信息」✗\n%s",
				forbidden, b)
		}
	}
	// 但打码后的来源、房间号、账号号**该**在（不然管理面板就没用了）
	for _, want := range []string{"120.227.*.*", "t-leak01", `"accountid":42`} {
		if !strings.Contains(raw, want) {
			t.Errorf("快照里少了 %q —— 管理面板要靠它工作：\n%s", want, b)
		}
	}
}
