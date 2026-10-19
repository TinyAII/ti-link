// 指标采样器的测试。
//
// 🩸 这里最要紧的三条：
//
//	① **countingConn 有没有把 CloseWrite 弄丢** —— 丢了的话，
//	   "关房间时先通知一句再断"会被 RST 冲掉，而那个功能**已经上线了** ✗
//	   （加包装层是最容易把类型断言打掉的地方）
//	② **Windows 上采不到 /proc 时不许崩、也不许编假数** ——
//	   本地开发就是 Windows，这里没测的话，本地一跑就是 panic ✗
//	③ **回给前端的只有"我们自己"的数** —— 整机那几项（cpu/mem/nrx…）
//	   老板明确说不要了，这条钉住"以后别悄悄加回来" ✓
package main

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ── ① 包装层不能把半关能力吃掉 ─────────────────────────────

type fakeHalfCloser struct {
	net.Conn
	closedWrite bool
	closed      bool
}

func (f *fakeHalfCloser) CloseWrite() error { f.closedWrite = true; return nil }
func (f *fakeHalfCloser) Close() error      { f.closed = true; return nil }

// 反证方法：把 countingConn.CloseWrite 删掉 ⇒ 立刻红 ✓
func TestCountingConnKeepsCloseWrite(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	f := &fakeHalfCloser{Conn: a}
	c := newCountingConn(f)

	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if !f.closedWrite {
		t.Fatal("包了一层之后 CloseWrite 没被转发下去 ✗ —— " +
			"那关房间时那句通知会被 RST 冲掉（这功能已经上线了）")
	}
	if f.closed {
		t.Fatal("半关不该变成整关 —— 整关会让对面还没读完的数据被丢掉")
	}
}

func TestCountingConnCountsBytes(t *testing.T) {
	before := ourRxBytes.Load() + ourTxBytes.Load()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := newCountingConn(a)

	go func() {
		buf := make([]byte, 16)
		_, _ = b.Read(buf)
		_, _ = b.Write([]byte("hello"))
	}()
	if _, err := c.Write([]byte("1234567890")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if _, err := c.Read(buf); err != nil {
		t.Fatal(err)
	}
	got := ourRxBytes.Load() + ourTxBytes.Load() - before
	if got != 15 { // 发出 10 + 收 5
		t.Fatalf("该数到 15 字节，实得 %d ✗", got)
	}
}

// ── ② 落盘：写出去还能读回来 ───────────────────────────────

func TestCSVRoundTrip(t *testing.T) {
	in := sample{At: 1760620000, PCpu: 0.55, RSS: 8.3, ORx: 0.12, OTx: 0.34,
		Conns: 2, Rooms: 1, Rej: 7, Bad: 3}
	out, ok := parseCSVLine(strings.TrimSpace(csvLine(in)))
	if !ok {
		t.Fatal("自己写出去的行，自己读不回来 ✗")
	}
	if out != in {
		t.Fatalf("来回一趟变了：\n  写 %+v\n  读 %+v", in, out)
	}
}

// 表头、半截行、坏行 ⇒ 跳过，不许当成数据 ✓
func TestCSVSkipsJunk(t *testing.T) {
	for _, bad := range []string{
		strings.TrimSpace(csvHeader),
		"1760620000,1,2",
		"",
		"a,b,c,d,e,f,g,h,i",
		// 🩸 **改表头之前的旧格式（14 列）也要跳过** ——
		// 不跳的话那些行的列会串位，图上会画出一条莫名其妙的线 ✗
		"1792137307,5.6,0.1,828.6,21.2,6.6,0,0,0,0,0,0,0,0",
	} {
		if _, ok := parseCSVLine(bad); ok {
			t.Errorf("这行不该被当成数据：%q", bad)
		}
	}
}

func TestFileAppendAndLoadTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.csv")
	m := &metricsStore{path: path}
	m.procOK = false // 走 tick 的时候别去碰 /proc（本地是 Windows）

	// 🩸 **必须走 tick() 而不是直接调 appendFile**：
	// 直接调的话，只验了"appendFile 自己能写"，
	// 而"tick 到底有没有调它"没人管 ⇒ 把 tick 里那句删掉测试照样绿 ✗
	// （2026-10-16 反证抓到：那条反证当时报"测试还是绿的"）
	for i := 0; i < 3; i++ {
		m.tick()
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("tick 之后该有个 CSV 文件了（是不是把 appendFile 的调用删了？）: %v", err)
	}
	body := string(b)
	if n := strings.Count(body, "at,pcpu"); n != 1 {
		t.Fatalf("表头该只写一次，实得 %d 次", n)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 4 { // 表头 + 3 个点
		t.Fatalf("该有 4 行（表头 + 3 个采样点），实得 %d 行：\n%s", len(lines), body)
	}
	for _, ln := range lines[1:] {
		if _, ok := parseCSVLine(ln); !ok {
			t.Errorf("写出去的行自己读不回来：%q", ln)
		}
	}

	// 重启：从文件尾部读回内存，图上的历史还在 ✓
	m2 := &metricsStore{path: path}
	m2.loadTail()
	if len(m2.ring) != 3 {
		t.Fatalf("该读回 3 个点，实得 %d", len(m2.ring))
	}

	// 权限：这个文件里有我们的进程画像，同机别的用户不该能读。
	//
	// ⚠️ 跟 socket 那条一样，**Windows 上验不了**（没有 POSIX 权限位，
	// 一律报 0666）⇒ 本地跳过，真实验在部署脚本里（会 stat 一下这个文件）✓
	if runtime.GOOS == "windows" {
		t.Log("Windows 上没有 POSIX 权限位，权限那条跳过（部署脚本在 Linux 上真验）")
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("落盘文件权限该是 0600（同机别的用户不该能读），实得 %04o", fi.Mode().Perm())
	}
}

// ── ③ 降采样 ──────────────────────────────────────────────

func TestDownsampleAverages(t *testing.T) {
	pts := []sample{{At: 1, PCpu: 10}, {At: 2, PCpu: 20}, {At: 3, PCpu: 30}, {At: 4, PCpu: 40}}
	out := downsample(pts, 2)
	if len(out) != 2 {
		t.Fatalf("4 个点按 2 合该剩 2 个，实得 %d", len(out))
	}
	if out[0].PCpu != 15 || out[1].PCpu != 35 {
		t.Fatalf("平均算错了：%v %v（该是 15 / 35）", out[0].PCpu, out[1].PCpu)
	}
	// 时间戳取这一段的**最后一个**（不然图上会整体左移半个格子）
	if out[0].At != 2 || out[1].At != 4 {
		t.Fatalf("时间戳该取段尾，实得 %d / %d", out[0].At, out[1].At)
	}
	if n := len(downsample(pts, 0)); n != 4 {
		t.Fatalf("step<=1 该原样返回，实得 %d 个", n)
	}
	// 计数类取"段里最后一个"（不是平均）—— 平均出来 1.5 个房间没有意义 ✗
	rooms := []sample{{At: 1, Rooms: 1}, {At: 2, Rooms: 4}}
	if got := downsample(rooms, 2)[0].Rooms; got != 4 {
		t.Fatalf("房间数该取段尾（4），实得 %d", got)
	}
	// rej/bad 进到这儿时是"每周期新增量" ⇒ 该**求和**（这一段总共多少次），
	// 取段尾的话就只剩一小截了 ✗
	rej := []sample{{At: 1, Rej: 1}, {At: 2, Rej: 2}, {At: 3, Rej: 0}, {At: 4, Rej: 3}}
	if got := downsample(rej, 2)[1].Rej; got != 3 {
		t.Fatalf("rej 该求和（0+3=3），实得 %d —— 只有 3 也可能蒙对，看下一句", got)
	}
	if got := downsample(rej, 2)[0].Rej; got != 3 {
		t.Fatalf("rej 该求和（1+2=3），实得 %d ✗（取段尾会得到 2）", got)
	}
}

// toDelta 把累计值改成"每周期新增量"。
//
// 反证方法：把 toDelta 里那两个 `d < 0` 判断删掉 ⇒ 最后一条立刻红 ✓
func TestToDelta(t *testing.T) {
	// 常规：有基准点
	pts := []sample{{At: 1, Rej: 5}, {At: 2, Rej: 7}, {At: 3, Rej: 7}, {At: 4, Rej: 12}}
	prev := sample{At: 0, Rej: 4}
	toDelta(pts, &prev)
	for i, want := range []int64{1, 2, 0, 5} {
		if pts[i].Rej != want {
			t.Fatalf("第 %d 个该是 %d，实得 %d（基准点 4 要算进去）", i, want, pts[i].Rej)
		}
	}

	// 没有基准点：窗口从数据开头开始，第一个点按"从 0 涨上来"算 ✓
	pts2 := []sample{{At: 1, Rej: 3}, {At: 2, Rej: 4}}
	toDelta(pts2, nil)
	if pts2[0].Rej != 3 || pts2[1].Rej != 1 {
		t.Fatalf("没基准点时该是 3 / 1，实得 %d / %d", pts2[0].Rej, pts2[1].Rej)
	}

	// 🩸 计数器变小（服务重启，计数从 0 开始）不许出负数 ——
	// 否则图上会冒一根**向下的尖刺**，看着像"被挡了 -98 次" ✗
	pts3 := []sample{{At: 1, Rej: 100}, {At: 2, Rej: 2}, {At: 3, Rej: 5}}
	toDelta(pts3, nil)
	if pts3[1].Rej != 0 {
		t.Fatalf("计数归零那一步该算 0，实得 %d ✗（负数 = 图上向下的尖刺）", pts3[1].Rej)
	}
	if pts3[2].Rej != 3 {
		t.Fatalf("归零之后该重新从新计数算（5-2=3），实得 %d ✗", pts3[2].Rej)
	}
}

// ── ④ /metrics 端点 ───────────────────────────────────────

func callMetrics(t *testing.T, query string) (int, []byte) {
	t.Helper()
	h := httptest.NewRecorder()
	metricsHandler(h, httptest.NewRequest("GET", "/metrics"+query, nil))
	return h.Code, h.Body.Bytes()
}

func TestMetricsHandlerShape(t *testing.T) {
	now := time.Now().Unix()
	metrics.ring = nil
	metrics.procOK = false // 假装在 Windows 上跑
	// Rej/Bad 按**累计值**造（只增不减）：每 10 秒各 +1 / +2 ⇒
	// 差分之后窗口里每个点该正好是 1 / 2，**不是** 60、59、58 那样一路涨下来 ✓
	for i := 60; i >= 0; i-- {
		metrics.ring = append(metrics.ring, sample{
			At: now - int64(i*10), PCpu: 0.5, Conns: int64(i),
			Rej: int64(60 - i), Bad: int64((60 - i) * 2),
		})
	}
	t.Cleanup(func() { metrics.ring = nil })

	code, body := callMetrics(t, "?range=600")
	if code != 200 {
		t.Fatalf("该 200，实得 %d", code)
	}
	var got struct {
		Interval int            `json:"interval"`
		Range    int            `json:"range"`
		Points   int            `json:"points"`
		T        []float64      `json:"t"`
		PCpu     []float64      `json:"pcpu"`
		RSS      []float64      `json:"rss"`
		ORx      []float64      `json:"orx"`
		OTx      []float64      `json:"otx"`
		Conns    []float64      `json:"conns"`
		Rooms    []float64      `json:"rooms"`
		Rej      []float64      `json:"rej"`
		Bad      []float64      `json:"bad"`
		Latest   *sample        `json:"latest"`
		Limits   map[string]any `json:"limits"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	// 🩸 所有序列长度必须一致：前端是按下标对齐画的，
	// 长度不齐会画出错位的线（而且不报错，只是看着不对）✗
	if len(got.T) != got.Points || len(got.PCpu) != got.Points ||
		len(got.Conns) != got.Points || len(got.RSS) != got.Points ||
		len(got.ORx) != got.Points || len(got.OTx) != got.Points {
		t.Fatalf("各序列长度该一样：points=%d t=%d pcpu=%d rss=%d orx=%d otx=%d conns=%d",
			got.Points, len(got.T), len(got.PCpu), len(got.RSS),
			len(got.ORx), len(got.OTx), len(got.Conns))
	}
	if got.Points == 0 || got.Points > maxPoints {
		t.Fatalf("点数该在 (0, %d]，实得 %d", maxPoints, got.Points)
	}
	// 采样间隔：2026-10-16 晚从 10 秒改成 1 秒（老板嫌刷新慢）——
	// 这里直接对着**常量**断言，改了常量测试跟着走、不会互相矛盾 ✓
	if got.Interval != int(metricsInterval.Seconds()) {
		t.Errorf("采样间隔该是 %v 秒，实得 %d", metricsInterval.Seconds(), got.Interval)
	}
	if got.Latest == nil {
		t.Error("该带上「最新那一个点」—— 界面上的大数字用它 ✗")
	}
	if got.Limits["max_rooms"] == nil {
		t.Error("该带上那几条上限（房间数 / 单 IP 并发…）✗")
	}

	// ⭐ 老板 2026-10-16 定的：**只看我们自己，不看整机** ✓
	// 反证方法：把整机那几个字段加回 metrics.go 的 sample ⇒ 立刻红 ✓
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	for _, k := range []string{"cpu", "mem", "memp", "nrx", "ntx"} {
		if _, ok := raw[k]; ok {
			t.Errorf("不该再回整机那个 %q 了 —— 老板明确说只看我们自己 ✗", k)
		}
	}
	for _, k := range []string{"pcpu", "rss", "orx", "otx", "conns", "rooms", "rej", "bad",
		"latest", "limits", "identity"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("该有的 %q 没了 ✗", k)
		}
	}

	// 🩸 rej/bad 序列必须是**每周期新增量**（差分过了），不是累计值 ✗
	// 造数据时累计值一路涨到 60/120 ⇒ 没差分的话这里会看到 60、59、58… ✓
	if got.Rej[len(got.Rej)-1] != 1 {
		t.Errorf("rej 该是「每周期新增 1 次」，实得 %v —— "+
			"要是 60 这种一路上涨的数，说明差分没做 ✗", got.Rej[len(got.Rej)-1])
	}
	if got.Bad[len(got.Bad)-1] != 2 {
		t.Errorf("bad 该是「每周期新增 2 次」，实得 %v ✗", got.Bad[len(got.Bad)-1])
	}
	// 累计值仍然要原样给（界面上"总共挡了多少"用 latest）✓
	if got.Latest == nil || got.Latest.Rej != 60 {
		t.Errorf("latest.rej 该是累计值 60，实得 %v ✗", got.Latest)
	}

	// 范围会被夹到 [300, 6 小时]
	var g2 struct {
		Range int `json:"range"`
	}
	_, b2 := callMetrics(t, "?range=99999999")
	_ = json.Unmarshal(b2, &g2)
	if g2.Range != int(metricsKeep.Seconds()) {
		t.Errorf("超大的范围该夹到 %v，实得 %d", metricsKeep.Seconds(), g2.Range)
	}
	var g3 struct {
		Range int `json:"range"`
	}
	_, b3 := callMetrics(t, "?range=1")
	_ = json.Unmarshal(b3, &g3)
	if g3.Range != 300 {
		t.Errorf("太小的范围该抬到 300，实得 %d", g3.Range)
	}
}

// ── ⑤ 采不到 /proc 时（Windows / 容器里没挂）不许编假数 ──────

func TestCollectWithoutProc(t *testing.T) {
	m := &metricsStore{}
	m.procOK = false
	m.tick() // 在 Windows 上跑：/proc 读不到，这一步必须安安静静地过
	if len(m.ring) != 1 {
		t.Fatalf("采不到进程数也该**留下一个点**（连接数/房间数还是有的），实得 %d 个", len(m.ring))
	}
	s := m.ring[0]
	if s.PCpu != 0 || s.RSS != 0 {
		t.Errorf("采不到 /proc 时 CPU/内存该是 0（界面显示成 —），"+
			"实得 pcpu=%v rss=%v —— **不许编假数** ✗", s.PCpu, s.RSS)
	}
}

// 环形缓冲该把 6 小时以前的点扔掉
func TestRingTrimsOld(t *testing.T) {
	m := &metricsStore{}
	m.procOK = false
	old := time.Now().Add(-metricsKeep - time.Hour).Unix()
	m.ring = []sample{{At: old, Conns: 99}, {At: time.Now().Unix() - 60, Conns: 1}}
	m.tick()
	for _, s := range m.ring {
		if s.At <= old {
			t.Fatalf("超过 %v 的老点该被扔掉（这条的 At=%d）✗", metricsKeep, s.At)
		}
	}
	if len(m.ring) != 2 {
		t.Fatalf("该剩 2 个点（刚采的 + 1 分钟前那个），实得 %d", len(m.ring))
	}
}

// 采样间隔是**产品决定**，不是实现细节 ⇒ 明确钉住 1 秒。
//
// 🩸 老板 2026-10-16 晚："怎么不是按秒刷新啊，刷新这么慢" ——
// 根子在**采样频率**，不在前端：前端每秒拉，这里 10 秒才采一次，
// 用户看到的就是"数字半天不动" ✗
//
// 📌 跟 TestMetricsHandlerShape 里那条的区别：
//
//	那条是"接口报的间隔 == 常量"（防**两边不一致**）；
//	这条是"常量就该是 1 秒"（防**被悄悄改慢**）—— 两条管的事不一样，都要 ✓
//
// 反证方法：把 metricsInterval 改成 10 * time.Second ⇒ 立刻红 ✓
func TestSampleIntervalIsOneSecond(t *testing.T) {
	if metricsInterval != time.Second {
		t.Fatalf("采样间隔该是 1 秒（老板要的按秒刷新），实得 %v ✗", metricsInterval)
	}
	if got := metricsKeep / metricsInterval; got != 21600 {
		t.Errorf("6 小时该是 21600 个点（约 1.4 MB），实得 %v —— 点数变了要想清内存代价", got)
	}
}
