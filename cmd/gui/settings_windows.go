// Windows 平台相关的设置实现：开机自启（注册表）、提示音。
package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// 开机自启写在当前用户的 Run 键下 —— 不需要管理员权限。
	runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue   = "P2PLink"
)

var (
	procPlaySound = windows.NewLazySystemDLL("winmm.dll").NewProc("PlaySoundA")

	dingOnce sync.Once
	// dingPCM 合成好的"叮"（WAV 字节）。
	// 🩸 必须是**包级变量**：SND_MEMORY 播放期间这块内存得一直有效，
	// 放在函数里的话播到一半可能被 GC 收走。
	dingPCM []byte
)

const (
	sndAsync     = 0x0001 // SND_ASYNC：别卡住调用方
	sndNoDefault = 0x0002 // SND_NODEFAULT：放不出来也别用系统默认音顶上
	sndMemory    = 0x0004 // SND_MEMORY：pszSound 指向内存里的 WAV
)

// autoStartEnabled 查注册表，看开机自启是不是开着。
func autoStartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return err == nil && strings.TrimSpace(v) != ""
}

// setAutoStart 开/关开机自启。关的时候顺手把注册表项删掉，不留垃圾。
func setAutoStart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath,
		registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()

	if !on {
		if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	// 路径带空格时注册表不认，必须加引号
	return k.SetStringValue(runValue, `"`+exe+`"`)
}

// playBeep 播一声"叮"（对端接上时用）。
//
// 🩸 原来用的是 MessageBeep —— 那玩意播的是**系统声音方案**里的音效：
// 用户把"星号"设成"无"、或者整个关掉系统音效，就一声都听不见（老板碰到的就是这个）。
// 现在自己合成一段 PCM 直接播，跟系统声音方案无关。
func playBeep() {
	dingOnce.Do(func() { dingPCM = dingWAV() })
	if len(dingPCM) == 0 {
		return
	}
	procPlaySound.Call(
		uintptr(unsafe.Pointer(&dingPCM[0])), 0,
		sndMemory|sndAsync|sndNoDefault,
	)
}

// dingWAV 现场合成一声"叮"：880Hz 正弦 + 一点二次谐波，指数衰减收尾。
//
// 加二次谐波是为了别太"电子味"；指数衰减是"叮"的关键 ——
// 音量掉得够快才有敲击感，匀速收尾听着像"呜"。
func dingWAV() []byte {
	const (
		rate = 44100
		ms   = 240
		freq = 880.0
	)
	n := rate * ms / 1000
	secs := float64(ms) / 1000

	pcm := make([]byte, 0, n*2)
	for i := 0; i < n; i++ {
		t := float64(i) / rate
		env := math.Exp(-7 * t / secs)
		v := 0.78*math.Sin(2*math.Pi*freq*t) + 0.22*math.Sin(2*math.Pi*freq*2*t)
		s := int16(v * env * 0.5 * 32767)
		pcm = append(pcm, byte(s), byte(uint16(s)>>8))
	}

	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))     // fmt 块长度
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))      // PCM
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))      // 单声道
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))   // 采样率
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*2)) // 字节率
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))      // 块对齐
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))     // 位深
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}
