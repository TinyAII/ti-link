package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 峰值只能往上顶 —— 它要是会跌，"峰值"这个词就没意义了。
func TestBumpPeak(t *testing.T) {
	var p atomic.Int64
	bumpPeak(&p, 5)
	if p.Load() != 5 {
		t.Fatalf("第一次该顶到 5，实得 %d", p.Load())
	}
	bumpPeak(&p, 3)
	if p.Load() != 5 {
		t.Errorf("更小的值不该把峰值拉下来，实得 %d", p.Load())
	}
	bumpPeak(&p, 9)
	if p.Load() != 9 {
		t.Errorf("更大的值该顶上去，实得 %d", p.Load())
	}
}

// 端点返回合法 JSON，而且每个数都对得上。
func TestStatsHandlerJSON(t *testing.T) {
	before := statsSnapshotNow()

	connOpened()
	connOpened()
	defer connClosed()
	defer connClosed()
	statRoomsTotal.Add(1)
	statRejectRate.Add(1)

	rec := httptest.NewRecorder()
	statsHandler(rec, httptest.NewRequest("GET", "/stats", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("Content-Type 该是 json，实得 %q", ct)
	}

	var got statsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("返回的不是合法 JSON: %v\n%s", err, rec.Body.String())
	}

	if want := before.Conns + 2; got.Conns != want {
		t.Errorf("连接数该是 %d，实得 %d", want, got.Conns)
	}
	if want := before.RoomsTotal + 1; got.RoomsTotal != want {
		t.Errorf("累计房间数该是 %d，实得 %d", want, got.RoomsTotal)
	}
	if want := before.RejectRate + 1; got.RejectRate != want {
		t.Errorf("限流拒绝数该是 %d，实得 %d", want, got.RejectRate)
	}
	if got.ConnsPeak < got.Conns {
		t.Errorf("峰值不该小于当前值：peak=%d conns=%d", got.ConnsPeak, got.Conns)
	}
	if got.Uptime == "" {
		t.Error("uptime 不该是空的")
	}
	// 限流参数也一起返回：数字不对时一眼能看出"是规则拦的还是规则没生效"
	if got.MaxRooms <= 0 || got.ConnsPerIP <= 0 || got.NewConnsPerS <= 0 {
		t.Errorf("限流参数没带全：%+v", got)
	}
}

// 🩸 这个端点**只许有聚合数字**，不许有任何能追到某个人的字段。
//
// 这条是防后人"顺手加个在线用户列表"的 —— 那正是我们花力气把日志 IP 打码掉
// 要避免的事（公开分发之后，"记录谁连过"就是收集个人信息）✗
func TestStatsHasNoPerUserFields(t *testing.T) {
	rec := httptest.NewRecorder()
	statsHandler(rec, httptest.NewRequest("GET", "/stats", nil))

	raw := strings.ToLower(rec.Body.String())
	// 用带前引号的写法，免得 conns_per_ip 这种"限流参数"被误伤
	for _, forbidden := range []string{`"remote`, `"addr`, `"peer`, `"nip`, `"nick`, `"label`, `"clientid`, `"ip`} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("指标里出现了 %s 开头的字段 —— 这里只许有聚合数字，不许能追到人 ✗", forbidden)
		}
	}
}
