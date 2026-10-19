package main

import (
	"net/http"
	"runtime"
	"strconv"
	"time"
)

// maxPoints 是回给前端的最多点数。
//
// 📌 为什么不把 21600 个点原样丢过去：折线图横着就 1000 来个像素，
// 点比像素还密 ⇒ 画出来是条糊的带子，而且白传一堆数据 ✗
// 360 个点足够看出形状，传输量也小 ✓
const maxPoints = 360

// metricsHandler 给**历史序列**（概览页的折线图用）。
//
// 跟 /stats 的分工：/stats 是"此刻几个数"（快、小）；
// 这个是"最近一段时间的走势"（带降采样）✓ 两个都留着 ✓
func metricsHandler(w http.ResponseWriter, r *http.Request) {
	sec := 3600
	if v := r.URL.Query().Get("range"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			sec = n
		}
	}
	if sec < 300 {
		sec = 300
	}
	if max := int(metricsKeep.Seconds()); sec > max {
		sec = max
	}

	metrics.mu.Lock()
	all := append([]sample(nil), metrics.ring...)
	proc := metrics.procOK
	metrics.mu.Unlock()

	// 切窗口。**顺手记住窗口前那一个点**：下面的差分要用它当基准 ——
	// 少了它，窗口里第一个点的增量会被当成"从 0 涨上来的"，开头凭空一个尖刺 ✗
	cut := time.Now().Add(-time.Duration(sec) * time.Second).Unix()
	pts := make([]sample, 0, len(all))
	var prev *sample
	for i := range all {
		if all[i].At < cut {
			prev = &all[i]
			continue
		}
		pts = append(pts, all[i])
	}
	toDelta(pts, prev)

	step := 1
	if len(pts) > maxPoints {
		step = (len(pts) + maxPoints - 1) / maxPoints
	}
	out := downsample(pts, step)

	writeLocalJSON(w, http.StatusOK, map[string]any{
		"interval": int(metricsInterval.Seconds()),
		"proc_ok":  proc,
		"range":    sec,
		"step":     step * int(metricsInterval.Seconds()),
		"points":   len(out),
		"t":        col(out, func(s sample) float64 { return float64(s.At) }),
		"pcpu":     col(out, func(s sample) float64 { return s.PCpu }),
		"rss":      col(out, func(s sample) float64 { return s.RSS }),
		"orx":      col(out, func(s sample) float64 { return s.ORx }),
		"otx":      col(out, func(s sample) float64 { return s.OTx }),
		"conns":    col(out, func(s sample) float64 { return float64(s.Conns) }),
		"rooms":    col(out, func(s sample) float64 { return float64(s.Rooms) }),
		// rej / bad 是**每个采样周期新增多少**（差分过了，见 toDelta）——
		// 不是累计值。累计值在 latest 里（那是原始 sample）✓
		// ⚠️ 一个点代表的时间 = step 秒 ⇒ 前端要换算成"次/分"得乘 60/step ✓
		"rej":      col(out, func(s sample) float64 { return float64(s.Rej) }),
		"bad":      col(out, func(s sample) float64 { return float64(s.Bad) }),
		"latest":   latestOf(all),
		"limits":   limitsNow(),
		"identity": identityNow(),
	})
}

// toDelta 把**累计值**（rej/bad：只增不减）就地改成**每周期新增量**。
//
// 🩸 为什么非改不可：累计值画出来是一条**一直往上爬的斜坡** ⇒
// 用户只能看见"总数在涨"，看不出"**什么时候**被挡了一下" ✗
// 而这个页面上真正有用的问题是后者 ✓
//
// 📌 必须在**原始分辨率、降采样之前**做：降完再减的话，
// 相邻点的间隔已经变成 step 倍，"每次新增多少"的量级就错了 ✗
//
// ⚠️ 计数器可能**变小**（服务重启，计数从 0 开始）⇒ 那种情况按 0 算，
// 否则图上会冒出一根向下的负尖刺 ✗
func toDelta(pts []sample, prev *sample) {
	var lastRej, lastBad int64
	if prev != nil {
		lastRej, lastBad = prev.Rej, prev.Bad
	}
	for i := range pts {
		d := pts[i].Rej - lastRej
		lastRej = pts[i].Rej
		if d < 0 {
			d = 0
		}
		pts[i].Rej = d

		d = pts[i].Bad - lastBad
		lastBad = pts[i].Bad
		if d < 0 {
			d = 0
		}
		pts[i].Bad = d
	}
}

func col(pts []sample, f func(sample) float64) []float64 {
	out := make([]float64, len(pts))
	for i, s := range pts {
		out[i] = f(s)
	}
	return out
}

// downsample 把每 step 个点平均成一个。
//
// 📌 平均而不是抽一个：抽一个的话，尖峰可能整个被抽掉 ⇒
// 图上看不见"刚才那一下卡顿" ✗ 平均起码能把它抬起来一点 ✓
func downsample(pts []sample, step int) []sample {
	if step <= 1 {
		return pts
	}
	out := make([]sample, 0, len(pts)/step+1)
	for i := 0; i < len(pts); i += step {
		j := i + step
		if j > len(pts) {
			j = len(pts)
		}
		var sum sample
		n := float64(j - i)
		for _, s := range pts[i:j] {
			sum.At = s.At // 取这一段的最后一个时间戳
			sum.PCpu += s.PCpu
			sum.RSS += s.RSS
			sum.ORx += s.ORx
			sum.OTx += s.OTx
			sum.Conns = s.Conns
			sum.Rooms = s.Rooms
			// rej/bad **求和**（不是取最后一个）——
			// 它们进到这儿的时候已经是"每周期新增多少"（见 toDelta），
			// 这一段的总数 = 加起来 ✓ 取最后一个的话就只剩一小截 ✗
			sum.Rej += s.Rej
			sum.Bad += s.Bad
		}
		sum.PCpu = round2(sum.PCpu / n)
		sum.RSS = round1(sum.RSS / n)
		sum.ORx, sum.OTx = round2(sum.ORx/n), round2(sum.OTx/n)
		out = append(out, sum)
	}
	return out
}

// latestOf 给"最新那一个采样点"：界面上的大数字要的是瞬时值，
// 而不是"最近 6 小时的平均" ✗
func latestOf(all []sample) any {
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

// limitsNow 是那几条**配置**（不是指标）：房间上限、单 IP 并发……
// 📌 跟指标分开放：一个是"现在多少"、一个是"允许多少"，混在一起界面没法标单位 ✓
func limitsNow() map[string]any {
	return map[string]any{
		"max_rooms":         maxRooms,
		"conns_per_ip":      maxConnsPerIP,
		"new_conns_per_sec": connsPerSecond,
		"uptime_seconds":    int64(time.Since(startedAt).Seconds()),
		"reject_ban":        statRejectBan.Load(),
		"bad_token":         statBadToken.Load(),
		"no_token":          statNoToken.Load(),
		"conns_peak":        statConnsPeak.Load(),
		"rooms_peak":        statRoomsPeak.Load(),
		"rooms_total":       statRoomsTotal.Load(),
		// 采样本身的参数也摆出来：看到"保留 6 小时"才知道那个时间范围按钮的上限是从哪来的 ✓
		"metrics_keep":     int(metricsKeep.Seconds()),
		"metrics_interval": int(metricsInterval.Seconds()),
		// 🩸 **必须是服务器自己的核数**（runtime.NumCPU）——
		// 前端原来那个是 `navigator.hardwareConcurrency`，那是**浏览器所在机器**的 ✗
		// 拿来看"CPU 占了几成"会看错（你电脑 8 核、服务器 4 核，比例差一倍）✗
		"cpus": runtime.NumCPU(),
	}
}
