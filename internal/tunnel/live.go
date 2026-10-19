// 实时连接状态：延迟、丢包、走的是 v4 还是 v6。
//
// 为什么值得做（2026-10-18 老板提的）：
//	连上之后界面就一句"已连接"，用户不知道这条路好不好。
//	出了问题只能猜"是不是我网卡""是不是对面卡" —— 而这其实**量得出来** ✓
//
// 📌 数据来源是分流层的心跳（split.go 的 typPing），不额外开通道 ✔
package tunnel

import (
	"fmt"
	"net"
	"sync"
	"time"
)

// LiveStat 是给界面看的一行状态。
type LiveStat struct {
	RTTms   int    // 最近几次心跳的平均往返（毫秒）
	LossPct int    // 最近一段时间里没回的百分比
	Family  string // "v4" / "v6"
	OK      bool   // 有没有采到过数据（刚连上时还没有）
}

// String 拼成一句话。界面直接用，别在 JS 里再拼一遍 ✓
func (s LiveStat) String() string {
	if !s.OK {
		return "直连 · 测速中…"
	}
	txt := fmt.Sprintf("直连 %s · %d ms", s.Family, s.RTTms)
	if s.LossPct > 0 {
		txt += fmt.Sprintf(" · 丢包 %d%%", s.LossPct)
	}
	return txt
}

// probeWindow 是算平均值的样本窗口。太大就反应迟钝，太小就一跳一跳的 ✓
const probeWindow = 6

// StartProbe 开始周期心跳。返回一个停止函数（调用方 defer 它）。
//
// stop 是外部停止信号（退房间时关掉），两个都要能收住 ✓
func StartProbe(sc *SplitConn, peer net.Addr, every time.Duration,
	onUpdate func(LiveStat), stop <-chan struct{}) func() {

	family := "v4"
	if a, ok := peer.(*net.UDPAddr); ok && a.IP.To4() == nil {
		family = "v6"
	}

	var mu sync.Mutex
	var rtts []time.Duration
	lossWin := make([]bool, 0, probeWindow) // true = 这一发没回

	done := make(chan struct{})
	var once sync.Once

	report := func() {
		mu.Lock()
		var avg time.Duration
		for _, d := range rtts {
			avg += d
		}
		if len(rtts) > 0 {
			avg /= time.Duration(len(rtts))
		}
		lost := 0
		for _, l := range lossWin {
			if l {
				lost++
			}
		}
		stat := LiveStat{
			Family: family,
			OK:     len(rtts) > 0,
		}
		if stat.OK {
			stat.RTTms = int(avg.Milliseconds())
		}
		if len(lossWin) > 0 {
			stat.LossPct = lost * 100 / len(lossWin)
		}
		mu.Unlock()

		if onUpdate != nil {
			onUpdate(stat)
		}
	}

	sc.SetPingHandler(func(rtt time.Duration, lost bool) {
		mu.Lock()
		if lost {
			lossWin = append(lossWin, true)
		} else {
			lossWin = append(lossWin, false)
			rtts = append(rtts, rtt)
			if len(rtts) > probeWindow {
				rtts = rtts[1:]
			}
		}
		if len(lossWin) > probeWindow {
			lossWin = lossWin[1:]
		}
		mu.Unlock()
		report()
	})

	// 先报一次"测速中"，界面不用等 2 秒才有反应 ✓
	report()

	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-stop:
				return
			case <-t.C:
				sc.Ping()
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }
}
