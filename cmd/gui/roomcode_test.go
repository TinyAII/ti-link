// 房间 ID 的形状测试。
//
// 为什么值得单独立个测试：格式变了**不会有任何报错** ——
// 号照样能建房间、能加入，只是长得不对了。这种"静默跑偏"最能拖到你某天才发现。
package main

import (
	"regexp"
	"testing"
)

func TestNewRoomCode(t *testing.T) {
	// TI + 6 位数字 + 2 位大写字母，形如 TI123456DY
	shape := regexp.MustCompile(`^TI[0-9]{6}[A-Z]{2}$`)

	var samples []string
	for i := 0; i < 6; i++ {
		samples = append(samples, newRoomCode())
	}
	t.Logf("样例：%v", samples)

	const n = 3000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		c := newRoomCode()
		if !shape.MatchString(c) {
			t.Fatalf("房间 ID 形状不对：%q（应该是 TI+6位数字+2位大写字母）", c)
		}
		if len(c) != 10 {
			t.Fatalf("房间 ID 该是 10 位，实得 %d：%q", len(c), c)
		}
		if seen[c] {
			t.Fatalf("连摇 %d 次就撞号了：%q —— 随机性不对", i+1, c)
		}
		seen[c] = true
	}

	// 顺带确认它不是"只换了前缀、后面还是死的"：
	// 数字那 6 位、字母那 2 位都得真的在变。
	var digitPart, tailPart [6]byte
	first := newRoomCode()
	copy(digitPart[:], first[2:8])
	tailPart[0], tailPart[1] = first[8], first[9]
	var digitMoved, tailMoved bool
	for i := 0; i < 200; i++ {
		c := newRoomCode()
		if c[2:8] != string(digitPart[:]) {
			digitMoved = true
		}
		if c[8] != tailPart[0] || c[9] != tailPart[1] {
			tailMoved = true
		}
	}
	if !digitMoved {
		t.Error("摇 200 次数字部分一模一样 —— 随机的是不是只随机了后缀？")
	}
	if !tailMoved {
		t.Error("摇 200 次后缀一模一样 —— 随机的是不是只随机了数字？")
	}
}
