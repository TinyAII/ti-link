package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// historyHandler 是后台那一页唯一的数据口子。
// 这里钉三件事：正常能出数据、搜索能过、非 GET 要拒。
func TestHistoryHandlerOK(t *testing.T) {
	withEvents(t, []histEvent{
		evOpen("TIAAA", "我的世界", 3, nowMinus(30)),
		evJoin("TIAAA", "a", "阿甲", 0, true, nowMinus(30)),
		evJoin("TIAAA", "b", "小明", 1, false, nowMinus(25)),
		evLeave("TIAAA", "b", "left", nowMinus(10)),
		evClose("TIAAA", nowMinus(5)),
	})

	rec := httptest.NewRecorder()
	historyHandler(rec, httptest.NewRequest(http.MethodGet, "/history", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	var body struct {
		Rooms []struct {
			Code    string `json:"code"`
			Members []struct {
				Label  string `json:"label"`
				LeftAt int64  `json:"leftAt"`
			} `json:"members"`
		} `json:"rooms"`
		Days int `json:"days"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("返回的不是合法 JSON：%v", err)
	}
	if len(body.Rooms) != 1 || body.Rooms[0].Code != "TIAAA" {
		t.Fatalf("房间不对：%+v", body.Rooms)
	}
	if len(body.Rooms[0].Members) != 2 {
		t.Errorf("成员数 = %d，期望 2", len(body.Rooms[0].Members))
	}
	if body.Days != 30 {
		t.Errorf("默认窗口 = %d 天，期望 30", body.Days)
	}
}

// 没有记录时要返回**空数组**，不是 null —— 前端少一个判空分支
func TestHistoryHandlerEmptyIsArray(t *testing.T) {
	withEvents(t, nil)

	rec := httptest.NewRecorder()
	historyHandler(rec, httptest.NewRequest(http.MethodGet, "/history", nil))

	s := rec.Body.String()
	if !strings.Contains(s, "rooms") {
		t.Fatalf("返回体里没有 rooms：%s", s)
	}
	var body map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if string(body["rooms"]) != "[]" {
		t.Errorf("空数据应返回 []，实际 %s", string(body["rooms"]))
	}
}

// 关键词过滤要从 handler 透到查询里
func TestHistoryHandlerSearch(t *testing.T) {
	withEvents(t, []histEvent{
		evOpen("TIAAA", "我的世界", 1, nowMinus(30)),
		evOpen("TIBBB", "泰拉瑞亚", 2, nowMinus(20)),
	})

	rec := httptest.NewRecorder()
	historyHandler(rec, httptest.NewRequest(http.MethodGet, "/history?q=泰拉", nil))

	var body struct {
		Rooms []struct {
			Code string `json:"code"`
		} `json:"rooms"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Rooms) != 1 || body.Rooms[0].Code != "TIBBB" {
		t.Errorf("搜索没有生效：%+v", body.Rooms)
	}
}

// 只读端点：非 GET 一律拒（面板那边只会 GET，但接口自己也要挡住）
func TestHistoryHandlerRejectsPOST(t *testing.T) {
	withEvents(t, nil)
	rec := httptest.NewRecorder()
	historyHandler(rec, httptest.NewRequest(http.MethodPost, "/history", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 状态码 = %d，期望 405", rec.Code)
	}
}

// ---- 小工具 ----

// nowMinus 返回 n 秒前的时间戳。
// 📌 必须用**相对现在**的时间：写死 100/200 会被 30 天裁剪吃掉，
// 那种红是裁剪造成的，跟要测的东西没关系 ✗（同一个坑踩过一次了）
func nowMinus(sec int64) int64 { return time.Now().Unix() - sec }

// open 标记只该在"这间房现在真的还开着"时为 true。
//
// 🩸 这条是补一个**上线后才发现的 bug**：处理函数里局部变量叫 `rooms`，
// 把全局的房间表遮蔽了 ⇒ 遍历到的是历史结果自己 ⇒ 每个房间都被标成"还开着"
// ⇒ 页面把已经关掉的房间显示成"还在房间里" ✗
// 当时的处理器测试只断言了房间数和成员数，**没断言 open**，所以没抓住 ✓
func TestHistoryHandlerOpenFlag(t *testing.T) {
	withEvents(t, []histEvent{
		evOpen("TIOPEN", "A", 1, nowMinus(60)),
		evJoin("TIOPEN", "a", "甲", 0, true, nowMinus(60)),
	})

	// 把全局房间表换成一个干净的，测完还原
	roomsMu.Lock()
	oldRooms := rooms
	rooms = map[string]*room{}
	roomsMu.Unlock()
	t.Cleanup(func() {
		roomsMu.Lock()
		rooms = oldRooms
		roomsMu.Unlock()
	})

	read := func() bool {
		rec := httptest.NewRecorder()
		historyHandler(rec, httptest.NewRequest(http.MethodGet, "/history", nil))
		var body struct {
			Rooms []struct {
				Open bool `json:"open"`
			} `json:"rooms"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if len(body.Rooms) != 1 {
			t.Fatalf("房间数 = %d，期望 1", len(body.Rooms))
		}
		return body.Rooms[0].Open
	}

	// ① 房间表里没有它（已经关了 / 服务重启过）⇒ 不能算"开着"
	if read() {
		t.Error("房间表里没有这个房间，却标成了 open")
	}

	// ② 房间表里有它 ⇒ 才算开着
	roomsMu.Lock()
	rooms["TIOPEN"] = &room{code: "TIOPEN"}
	roomsMu.Unlock()
	if !read() {
		t.Error("房间表里明明有这个房间，却没标 open")
	}
}
