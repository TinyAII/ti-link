// 提示音「该不该响」的判断测试。
//
// 老板定的规矩：
//
//	房主 —— **有玩家加入进来**才响（有人离开不响）
//	玩家 —— **自己成功进房**才响（别的成员加入不响）
//
// 这里能机械验证的是房主那一半（人数变多的判断）+ 状态文字解析。
// 玩家那一半挂在一个"只执行一次"的标志上（backend.go 的 onJoined），
// 没法单独调（要起真的 webview），靠出图/实测覆盖。
package main

import "testing"

func TestHostGrewOnlyOnIncrease(t *testing.T) {
	b := &backend{}

	if !b.hostGrew(1) {
		t.Error("0 → 1（第一个朋友进来）该响")
	}
	if !b.hostGrew(2) {
		t.Error("1 → 2（又来一个）该响")
	}
	if b.hostGrew(1) {
		t.Error("2 → 1（有人走了）不该响")
	}
	if b.hostGrew(1) {
		t.Error("人数没变不该响")
	}
	if b.hostGrew(0) {
		t.Error("走光了也不该响")
	}
	if !b.hostGrew(1) {
		t.Error("走光之后又来一个，该响")
	}
}

func TestFriendCount(t *testing.T) {
	if n, ok := friendCount("已有 3 位朋友连接"); !ok || n != 3 {
		t.Errorf("该从「已有 3 位朋友连接」里抠出 3，实得 %d（ok=%v）", n, ok)
	}
	// 房主建好房、还没人进来时的那句 —— 不能当成"有人加入"
	if _, ok := friendCount("房间已创建，等待朋友加入…"); ok {
		t.Error("「等待朋友加入…」不该被当成人数")
	}
	if _, ok := friendCount("正在创建房间…"); ok {
		t.Error("「正在创建房间…」不该被当成人数")
	}
}
