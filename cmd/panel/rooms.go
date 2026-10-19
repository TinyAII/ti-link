// 房间列表 / 指标 / 关房间：面板**不自己算**，都去信令服务那边。
//
// 为什么：房间是信令进程的**内存态**（`cmd/signal` 的 rooms 表），面板碰不到。
// 要做"看见房间、能关掉"这些事，只能是信令服务自己把状态亮出来、自己动手 ✓
//
// 📌 那条通道是信令服务的一个 **Unix socket**（`/root/p2plink-data/local.sock`，
// 权限 0600）。原来它是监听在 127.0.0.1 上的 TCP ——
// 🩸 换成 socket 是因为老板一句问：**回环地址挡不住同机上的其它进程** ✗
// 那台机器上还跑着别的项目（nginx/postgres/博客），谁都能 curl 一下。
// 读接口无所谓，但"关房间"是能踢人的写接口 ⇒ 权限交给操作系统判 ✓
// （见 `cmd/signal/localapi.go` 顶上那段完整的解释）
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

// signalResp 是面板转给前端的形状。
//
// 🩸 永远回 200 + available 标志，**不用 5xx** —— 信令侧端点没开是
// 一件**正常**的事（比如只开了联机没开指标），前端该显示一句人话，
// 而不是弹个"服务器错误"让人以为面板坏了 ✓
type signalResp struct {
	Available bool            `json:"available"`
	Error     string          `json:"error,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// maxSignalBytes 兜住信令侧的响应大小：房间再多也不该超过这个数。
const maxSignalBytes = 1 << 20 // 1 MiB

// signalHost 是个**占位主机名**：真正连到哪儿由 http.Client 的 Transport 决定
// （它 dial 的是那个 Unix socket）✓
// 📌 用一个明确的占位名而不是 127.0.0.1，是为了别让人误以为"它是在连本机端口" ✗
const signalHost = "http://signal"

func (s *Server) proxySignal(w http.ResponseWriter, r *http.Request, path string) {
	if s.signalSock == "" {
		writeJSON(w, http.StatusOK, signalResp{
			Available: false,
			Error:     "没有配置穿透服务器的本机端点（启动面板时给个 -signal）",
		})
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, signalHost+path, nil)
	if err != nil {
		writeJSON(w, http.StatusOK, signalResp{Available: false, Error: "请求造不出来"})
		return
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		// ⚠️ 日志里可以说具体错误：这条通道是**本机 socket**，
		// 错误里不会带用户地址之类的东西 ✓
		logLine(r, http.StatusOK, "信令侧取不到："+err.Error())
		writeJSON(w, http.StatusOK, signalResp{
			Available: false,
			Error:     "穿透服务器那边取不到（可能没开这个端点，或者它没在跑）",
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 🩸 **把信令侧那句话原样带出去** —— 那边说的是
		// "房间不在了（可能刚关掉，或者本来就没有）"这种具体的话，
		// 压成"回了异常状态"就没法告诉用户到底怎么了 ✗
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxSignalBytes))
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error == "" {
			e.Error = "穿透服务器那边回了异常状态"
		}
		logLine(r, http.StatusOK, "信令侧回了非 200："+e.Error)
		writeJSON(w, http.StatusOK, signalResp{Available: false, Error: e.Error})
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSignalBytes))
	if err != nil {
		writeJSON(w, http.StatusOK, signalResp{Available: false, Error: "读响应失败"})
		return
	}
	// 🩸 先验一遍是合法 JSON 再往外转。不验的话，等于把"信令侧回什么"
	// 原样喂给浏览器 —— 万一那边哪天回了半截内容，前端会以一种很难懂的方式炸 ✓
	if !json.Valid(body) {
		logLine(r, http.StatusOK, "信令侧回的不是合法 JSON")
		writeJSON(w, http.StatusOK, signalResp{Available: false, Error: "穿透服务器那边回的内容看不懂"})
		return
	}
	logLine(r, http.StatusOK, "")
	writeJSON(w, http.StatusOK, signalResp{Available: true, Data: body})
}

// proxySignalPost 是**写**方向：把面板的这个请求转给信令侧那个本机接口。
//
// 📌 跟读的那条**不用同一个形状**：写操作的结果前端要直接拿去显示
// （成功 / "没有这个房间"），所以回的是面板惯用的 `{ok}` / `{error}`
// + 合适的状态码，而不是 `{available, data}` 那层壳 ✓
func (s *Server) proxySignalPost(w http.ResponseWriter, r *http.Request, path string, body any) {
	if s.signalSock == "" {
		writeErr(w, http.StatusServiceUnavailable, "没有配置穿透服务器的本机端点（启动时给个 -signal）")
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "服务器内部错误")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, signalHost+path, bytes.NewReader(raw))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "请求造不出来")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.hc.Do(req)
	if err != nil {
		logLine(r, http.StatusBadGateway, "信令侧没接上："+err.Error())
		writeErr(w, http.StatusBadGateway, "穿透服务器那边没接上（可能它没在跑）")
		return
	}
	defer resp.Body.Close()

	out, _ := io.ReadAll(io.LimitReader(resp.Body, maxSignalBytes))
	if resp.StatusCode == http.StatusOK {
		logLine(r, http.StatusOK, "")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	// 信令侧那边给的是一句人话（"没有这个房间（可能刚被关掉了）"）⇒ 原样转给前端 ✓
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(out, &e)
	if e.Error == "" {
		e.Error = "穿透服务器那边没能关掉（它回了 " + http.StatusText(resp.StatusCode) + "）"
	}
	logLine(r, resp.StatusCode, "信令侧没关成")
	writeErr(w, resp.StatusCode, e.Error)
}
