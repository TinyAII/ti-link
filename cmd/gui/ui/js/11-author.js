/* ---------- 关于作者：官网 + 几个 QQ 群链接 ----------
 *
 * 🩸 不让页面自己 window.open（webview 里不一定允许，弹出来也没地址栏）✗
 *    ⇒ 走 window.OpenLink，后端用系统浏览器打开 ✓
 *    后端只放行白名单里的域名（见 settings_api.go 的 OpenLink）✓
 *    📌 加新站点时**两边都要动**：HTML 里加 data-url、Go 里加白名单前缀，
 *      只改一边的话点了会提示"这个链接不在允许范围内" ✓
 *
 * 📌 链接写在 HTML 的 data-url 上，这里只负责"点哪个开哪个" ✓
 *    以后加群就复制一行 HTML，不用改 JS ✓ */
(function () {
  function bind() {
    document.querySelectorAll('.authorrow').forEach(btn => {
      if (btn.dataset.bound) return;
      btn.dataset.bound = '1';
      btn.onclick = async () => {
        const url = btn.dataset.url;
        if (!url || !window.OpenLink) return;
        let err = '';
        try {
          err = await window.OpenLink(url);
        } catch (e) {
          err = '打开失败：' + e;
        }
        if (err) setStatus(err, 'err');
      };
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bind);
  } else {
    bind();
  }
})();
