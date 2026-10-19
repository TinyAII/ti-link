// 指标采样器：**每 1 秒**采一次，内存里留 6 小时、同时追加落盘。
//
// 为什么要有这个（原来的 /stats 不够用）：
//
//	/stats 给的是**瞬时值** —— 画不出折线（趋势才是运营要看的）✓
//
// 🩸 **只采我们自己，不采整机**（老板 2026-10-16 改的，原话：
// "只要显示我们自己项目的，不要显示整个机器"）✓
//
// 采什么、各从哪来：
//
//	本进程 CPU   /proc/self/stat（utime+stime 的差值 / 间隔）
//	本进程 内存  /proc/self/statm（常驻页数 × 页大小）
//	我们带宽     代码里数（见下面 countingConn）—— 收/发各一个累计器
//	连接 / 房间 / 拒绝 / 令牌不认   进程内的计数器
//
// 📌 **不采整机的理由**：那几项里混着这台机器上别的项目（nginx/postgres/博客），
// 摆在自己面板上要么得解释半天、要么会让人把"机器很闲"误读成"我们很闲" ✗
// 而"我们自己的"这几个数跟这台机器上别的东西**分得清**，这才是面板该有的 ✓
//
// 📌 落盘用 CSV（不是 SQLite）：**不引入任何依赖**，文件小、能直接拿眼睛看、
// `tail` 一下就够 ✓ 撑到 8 MB 就轮转一次，永远只留两份 ✓
package main

import (
	"bufio"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// 🩸 采样间隔 2026-10-16 晚上从 10 秒改成 **1 秒**。
	//
	// 老板的原话："怎么不是按秒刷新啊，刷新这么慢" ——
	// 根子在**这里**，不在前端：前端拉得再勤，数据本身 10 秒才变一次 ✗
	//
	// 代价（都算过，可接受）：
	//   · 内存里 6 小时 ⇒ 21600 个点 ≈ 1.4 MB（每个 sample 几十字节）
	//   · 落盘 CSV：每秒一行 ≈ 一天 4.7 MB ⇒ 轮转阈值相应提到 32 MB（约 6.7 天）
	//   · 每个点读的是 /proc/self/{stat,statm}，代价极低 ✓
	// 📌 **不改**的：折线图最多还是回 360 个点（横着就 1000 来个像素），
	//    所以"1 小时 / 6 小时"那两档是靠降采样撑着的；**看到秒级抖动的是 5 分钟那档** ✓
	metricsInterval = 1 * time.Second
	metricsKeep     = 6 * time.Hour // 内存里留多久（1 秒粒度 ⇒ 21600 个点）
	metricsMaxBytes = 32 << 20      // 落盘文件超这个就轮转（≈ 6.7 天的量）
)

// sample 是一个采样点。字段名短，因为要落盘、要传网络、一个点存 6 小时 ✓
type sample struct {
	At    int64   `json:"t"`     // unix 秒
	PCpu  float64 `json:"pcpu"`  // 本进程 CPU %（占满一个核 = 100；多核可能 >100）
	RSS   float64 `json:"rss"`   // 本进程常驻内存 MB
	ORx   float64 `json:"orx"`   // 我们收 KB/s
	OTx   float64 `json:"otx"`   // 我们发 KB/s
	Conns int64   `json:"conns"` // 当前连接
	Rooms int64   `json:"rooms"` // 当前房间
	Rej   int64   `json:"rej"`   // 累计被限流挡掉
	Bad   int64   `json:"bad"`   // 累计令牌不认的（无效/过期/被踢下线）
}

// csvHeader 是落盘文件的表头。
//
// 🩸 改这个**等于改文件格式**：旧文件里列数不一样的行会被 parseCSVLine 跳过，
// 也就是"部署之后图上的历史从头开始" ✓ 换格式时必须跟使用的人说一声 ✓
const csvHeader = "at,pcpu,rss,orx,otx,conns,rooms,rej,bad\n"

// ── 我们自己的带宽：在连接上数 ────────────────────────────────
//
// 📌 为什么不从 /proc 拿："某个进程用了多少流量"在 /proc 里**根本没有** ✗
// 想要这个数只能在代码里数 —— 好在这也是最准的（跟这台机器上别的项目分得清）✓
var (
	ourRxBytes atomic.Int64
	ourTxBytes atomic.Int64
)

// countingConn 包住一条连接，数**我们自己**在它上面收发了多少字节。
//
// 两个用途：
//
//	① 全局累计（上面那两个计数器）⇒ 概览页那条「带宽 · 我们」✓
//	② **每条连接各一份**（rx/tx）⇒ 房间详情页看"这个人跟服务器通了多少信令"✓
//
// 🩸 **CloseWrite 必须一起转发**：signal.Client 用类型断言找它来做"半关"
// （关房间时先说完那句通知再断，否则会被 RST 冲掉）。
// 包一层而不转发的话，那个断言会失败 ⇒ 半关退化成整关 ⇒ 那句话又丢了 ✗
// （2026-10-16 差点踩到 —— 加包装层的时候一定要把它带上）
//
// 📌 内嵌 net.Conn（而不是自己列几个方法）：LocalAddr / SetDeadline 那些也得有，
// 少一个它就不是 net.Conn 了 ✗
//
// 🩸 2026-10-16 晚改成**指针**（`*countingConn`）：因为"每条连接各一份"要能被
// 外面读到 —— 值拷贝的话，读到的永远是 0 ✗（而且原子计数器本身就不能拷贝）
type countingConn struct {
	net.Conn
	rx int64 // 这条连接上收到的字节（atomic）
	tx int64 // 这条连接上发出的字节（atomic）
}

func newCountingConn(c net.Conn) *countingConn { return &countingConn{Conn: c} }

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		ourRxBytes.Add(int64(n))
		atomic.AddInt64(&c.rx, int64(n))
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		ourTxBytes.Add(int64(n))
		atomic.AddInt64(&c.tx, int64(n))
	}
	return n, err
}

func (c *countingConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

// connBytes 读这条连接上的收发字节（房间详情页用）✓
//
// ⚠️ 读到的数**很小是正常的** —— 它只算信令：游戏数据走两台机器直连、
// 根本不经过这儿。界面上必须写清这一点，不然会被当成"他在传游戏数据" ✗
func (c *countingConn) connBytes() (rx, tx int64) {
	if c == nil {
		return 0, 0
	}
	return atomic.LoadInt64(&c.rx), atomic.LoadInt64(&c.tx)
}

// metricsStore 是采样器的状态。
type metricsStore struct {
	mu   sync.Mutex
	ring []sample

	// 🩸 落盘用**自己的一把锁**，不跟上面那把（采样写 + /metrics 读）共用：
	// 采样改成 1 秒之后每秒都要写一次盘 —— 拿读路径那把锁去做磁盘 IO 的话，
	// 面板那次请求就得排在文件写完后面 ✗（写本身还得串行，不然跟轮转打架）
	fileMu sync.Mutex
	path   string

	procOK bool // 能不能读 /proc/self（Windows 上不能）

	// 算速率用的"上一次"
	prevAt      time.Time
	prevProcCPU float64
	prevRx      int64
	prevTx      int64
}

var metrics = &metricsStore{}

// startMetrics 起采样循环。path 为空就只存内存（不落盘）。
//
// ⚠️ 它**绝不能影响联机**：采不到就采不到，错了就记一条日志继续跑 ✓
func startMetrics(path string) {
	metrics.path = path
	metrics.procOK = procAvailable()
	if path != "" {
		metrics.loadTail()
	}
	go func() {
		t := time.NewTicker(metricsInterval)
		defer t.Stop()
		metrics.tick() // 先采一次，别让界面开局空着
		for range t.C {
			metrics.tick()
		}
	}()
}

// procAvailable 看这台机器有没有 /proc/self（Windows 上没有）。
func procAvailable() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
}

func (m *metricsStore) tick() {
	now := time.Now()
	s := sample{At: now.Unix()}

	s.Conns = statConns.Load()
	s.Rooms, _ = roomCount()
	s.Rej, s.Bad = statRejectRate.Load(), statBadToken.Load()

	secs := 0.0
	if !m.prevAt.IsZero() {
		secs = now.Sub(m.prevAt).Seconds()
	}

	// 我们自己的流量：计数器是累计的 ⇒ 除以间隔得速率 ✓
	rx, tx := ourRxBytes.Load(), ourTxBytes.Load()
	if secs > 0 {
		s.ORx = float64(rx-m.prevRx) / 1024 / secs
		s.OTx = float64(tx-m.prevTx) / 1024 / secs
	}
	m.prevRx, m.prevTx = rx, tx

	// 本进程的 CPU / 内存 —— **只有我们自己**，不是整机的 ✓
	if m.procOK {
		if cpuSec, rssMB, ok := readSelfStat(); ok {
			if secs > 0 {
				s.PCpu = (cpuSec - m.prevProcCPU) / secs * 100
			}
			m.prevProcCPU = cpuSec
			s.RSS = rssMB
		}
	}
	m.prevAt = now

	m.mu.Lock()
	s.ORx, s.OTx = round2(s.ORx), round2(s.OTx)
	s.PCpu = round2(s.PCpu)
	s.RSS = round1(s.RSS)
	m.ring = append(m.ring, s)
	cut := now.Add(-metricsKeep).Unix()
	for len(m.ring) > 0 && m.ring[0].At < cut {
		m.ring = m.ring[1:]
	}
	m.mu.Unlock()

	m.appendFile(s)
}

// ── 落盘 ──────────────────────────────────────────────────

func (m *metricsStore) appendFile(s sample) {
	if m.path == "" {
		return
	}
	// 📌 用 fileMu 而不是 m.mu：写盘不该把 /metrics 的读挡住（见结构体那段注释）✓
	m.fileMu.Lock()
	defer m.fileMu.Unlock()
	m.rotateLocked()
	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return // 采不到指标不该影响联机，也不该刷屏：静默
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() == 0 {
		_, _ = f.WriteString(csvHeader)
	}
	_, _ = f.WriteString(csvLine(s))
}

// rotateLocked 文件超了就挪成 .old（只留两份）。
//
// 📌 只留两份是刻意的：这个文件是"看看趋势"，不是台账。
// 无限长的历史对排查没用，但占磁盘、还得有人管 ✓
func (m *metricsStore) rotateLocked() {
	fi, err := os.Stat(m.path)
	if err != nil || fi.Size() < metricsMaxBytes {
		return
	}
	_ = os.Rename(m.path, m.path+".old")
}

// loadTail 启动时把文件尾部读回内存，这样重启之后图还有一段历史 ✓
func (m *metricsStore) loadTail() {
	f, err := os.Open(m.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var got []sample
	skipped := 0
	for sc.Scan() {
		line := sc.Text()
		if s, ok := parseCSVLine(line); ok {
			got = append(got, s)
		} else if strings.TrimSpace(line) != strings.TrimSpace(csvHeader) {
			skipped++
		}
	}
	cut := time.Now().Add(-metricsKeep).Unix()
	for _, s := range got {
		if s.At >= cut {
			m.ring = append(m.ring, s)
		}
	}
	if n := len(m.ring); n > 0 {
		// 🩸 那个换算里的秒数**读常量**，别写死 —— 写死的话改了采样间隔，
		// 这句日志就会一本正经地报一个错的分钟数 ✗
		log.Printf("指标：从 %s 读回了 %d 个历史点（约 %d 分钟）", filepath.Base(m.path), n,
			n*int(metricsInterval.Seconds())/60)
	}
	if skipped > 0 {
		// 多半是改过表头的旧格式 —— 说一声，别让人以为"数据丢了"
		log.Printf("指标：跳过了 %d 行读不出来的旧数据（改过表头的旧格式？）", skipped)
	}
}

func csvLine(s sample) string {
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	i := func(v int64) string { return strconv.FormatInt(v, 10) }
	return strings.Join([]string{
		i(s.At), f(s.PCpu), f(s.RSS), f(s.ORx), f(s.OTx),
		i(s.Conns), i(s.Rooms), i(s.Rej), i(s.Bad),
	}, ",") + "\n"
}

func parseCSVLine(line string) (sample, bool) {
	parts := strings.Split(line, ",")
	if len(parts) != 9 {
		return sample{}, false
	}
	var s sample
	nums := make([]float64, 9)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return sample{}, false // 表头、半截行之类，跳过 ✓
		}
		nums[i] = v
	}
	s.At = int64(nums[0])
	s.PCpu, s.RSS, s.ORx, s.OTx = nums[1], nums[2], nums[3], nums[4]
	s.Conns, s.Rooms, s.Rej, s.Bad = int64(nums[5]), int64(nums[6]), int64(nums[7]), int64(nums[8])
	return s, true
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
