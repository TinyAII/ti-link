#include "webview.h"

// PATCHED(p2plink): 窗口相关的几处补丁。
//
// 实现在这个单独的编译单元里 —— header-only 的 static 变量会在每个翻译单元
// 各存一份，那样 Go 设的值和 WndProc 里读到的就不是同一个了。

extern "C" {

/* ---------- 关闭拦截钩子（「关闭时收进托盘」用） ---------- */

static int (*g_onclose_intercept)(void) = nullptr;

WEBVIEW_API void webview_set_onclose_intercept(int (*fn)(void)) {
  g_onclose_intercept = fn;
}

WEBVIEW_API int webview_onclose_intercept(void) {
  if (g_onclose_intercept == nullptr) {
    return 0; // 没装钩子 = 不拦截，窗口按默认行为关闭
  }
  return g_onclose_intercept();
}

/* ---------- 无边框窗口 ---------- */

static int g_frameless = 0;
static int g_caption_height = 0;  // 顶部可拖动的条有多高
static int g_buttons_width = 0;   // 右上角按钮区有多宽（那块不能拖）

WEBVIEW_API void webview_set_frameless(int enabled, int caption_height,
                                       int buttons_width) {
  g_frameless = enabled;
  g_caption_height = caption_height;
  g_buttons_width = buttons_width;
}

WEBVIEW_API int webview_is_frameless(void) { return g_frameless; }

WEBVIEW_API int webview_frame_hittest(int x, int y, int window_width) {
  // 顶部那条、且不在右侧按钮区 → 交给系统当标题栏（可拖动、双击最大化）
  if (y >= 0 && y < g_caption_height && x < window_width - g_buttons_width) {
    return HTCAPTION;
  }
  return HTCLIENT;
}

} // extern "C"
