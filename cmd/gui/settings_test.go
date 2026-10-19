// 设置默认值的测试。
//
// 只钉一件容易被人"顺手改回去"的事：提示音默认开着。
package main

import "testing"

func TestSoundOnConnectDefaultsOn(t *testing.T) {
	if !defaultSettings().SoundOnConnect {
		t.Error("「连接成功时提示音」的默认值该是开的")
	}
}
