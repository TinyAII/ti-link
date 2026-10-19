// 提示音的测试。
//
// 为什么值得测：声音这东西"没声音"最难查 —— 分不清是没播、格式错了、
// 还是一整段静音。这里把能机械判定的都钉住：WAV 头对、长度对、**确实有波形**。
//
// 顺带把合成结果写到 tmp/ding.wav，可以双击试听。
package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestDingWAV(t *testing.T) {
	w := dingWAV()

	if len(w) < 44 {
		t.Fatalf("WAV 太短（%d 字节），连头都不够", len(w))
	}
	if string(w[0:4]) != "RIFF" || string(w[8:12]) != "WAVE" || string(w[12:16]) != "fmt " {
		t.Fatalf("WAV 头不对: %q %q %q", w[0:4], w[8:12], w[12:16])
	}

	channels := binary.LittleEndian.Uint16(w[22:24])
	rate := binary.LittleEndian.Uint32(w[24:28])
	bits := binary.LittleEndian.Uint16(w[34:36])
	if channels != 1 || rate != 44100 || bits != 16 {
		t.Errorf("格式该是 单声道/44100/16bit，实得 %d/%d/%d", channels, rate, bits)
	}

	dataLen := int(binary.LittleEndian.Uint32(w[40:44]))
	if 44+dataLen != len(w) {
		t.Errorf("data 长度对不上：头里写 %d，实际有 %d", dataLen, len(w)-44)
	}

	// 🩸 关键：不能是一整段静音 —— 那样"格式全对"但根本没声音
	maxAbs := 0
	for i := 44; i+1 < len(w); i += 2 {
		s := int(int16(binary.LittleEndian.Uint16(w[i : i+2])))
		if s < 0 {
			s = -s
		}
		if s > maxAbs {
			maxAbs = s
		}
	}
	if maxAbs < 3000 {
		t.Errorf("波形几乎是静的（峰值只有 %d）—— 播出来会听不见", maxAbs)
	}

	// 结尾必须收得住（不然"叮"会拖成"呜"）：最后 5% 的峰值要明显小于整体
	tailStart := len(w) - (len(w)-44)/20
	tailMax := 0
	for i := tailStart &^ 1; i+1 < len(w); i += 2 {
		s := int(int16(binary.LittleEndian.Uint16(w[i : i+2])))
		if s < 0 {
			s = -s
		}
		if s > tailMax {
			tailMax = s
		}
	}
	if tailMax > maxAbs/4 {
		t.Errorf("结尾没衰减（尾部峰值 %d，整体 %d）—— 听着不会像'叮'", tailMax, maxAbs)
	}

	// 写一份出来，方便试听
	out := filepath.Join(moduleRoot(t), "tmp", "ding.wav")
	if err := os.WriteFile(out, w, 0o644); err == nil {
		t.Logf("试听文件: %s（%d 字节，峰值 %d）", out, len(w), maxAbs)
	}
}
