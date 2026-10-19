// 读 /proc/self 拿**本进程**的资源数字。
//
// ⚠️ **只读 /proc/self，不读整机的**（老板 2026-10-16 定的：面板上只看我们自己）——
// 所以这里没有 /proc/stat、没有 /proc/meminfo、没有 /proc/net/dev ✓
//
// ⚠️ 这些只在 Linux 上有（Windows 本地开发时 /proc 不存在）⇒
// 每个函数返回 ok=false 而不是崩 —— 采不到就显示"—" ✓
// 这也让本地的测试能照跑（断言"采不到时是 0 且不崩"）✓
//
// 📌 不引第三方库（gopsutil 那种）：要的数就两个，自己读 /proc 一共几十行，
// 而且**看得到每个数是怎么算的** ✓
package main

import (
	"os"
	"strconv"
	"strings"
)

// readSelfStat 返回本进程累计 CPU 秒数、常驻内存 MB。
//
// 📌 累计值是刻意的：算使用率要两次求差，所以这里给原始累计值，
// 百分比交给 metrics.go 用间隔去除 ✓
func readSelfStat() (cpuSeconds, rssMB float64, ok bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, 0, false
	}
	// ⚠️ comm 那一栏是**带括号的进程名、里面可能有空格** ⇒
	// 必须从**最后一个 ')' 之后**开始切，不能按空格整体切 ✗（经典坑）
	s := string(b)
	j := strings.LastIndexByte(s, ')')
	if j < 0 || j+2 >= len(s) {
		return 0, 0, false
	}
	f := strings.Fields(s[j+2:])
	// 从这里起：state 是第 1 个 ⇒ utime 是第 12 个、stime 第 13 个（下标 11、12）
	if len(f) < 13 {
		return 0, 0, false
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	// 单位是时钟滴答。Linux x86 上 HZ 恒为 100 ✓
	cpuSeconds = (ut + st) / 100

	if sb, err := os.ReadFile("/proc/self/statm"); err == nil {
		sf := strings.Fields(string(sb))
		if len(sf) >= 2 {
			if pages, err := strconv.ParseFloat(sf[1], 64); err == nil {
				rssMB = pages * float64(os.Getpagesize()) / 1024 / 1024
			}
		}
	}
	return cpuSeconds, rssMB, true
}
