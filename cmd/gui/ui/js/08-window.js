/* ---------- 窗口按钮（窗口是无边框的，这两个按钮由页面自己画） ---------- */
// 窗口是固定大小的（后端去掉了"可最大化"），所以没有最大化按钮，
// 也没有双击标题栏最大化 —— 双击会跟拖动打架。
if (window.WinMinimize) {
  $('btnMin').onclick = () => window.WinMinimize();
  $('btnClose').onclick = () => window.WinClose();
}

// 告诉后端"右上角这一排别当拖动区"。
// 由页面量实际宽度报过去，比两边各写死一个数可靠 —— 主题按钮文字一变就得改。
// 后端方法万一还没注入，就稍后重试（不然顶栏会整条都拖不动）。
function reportDragExclusion(retry) {
  const n = retry || 0;
  if (!window.SetDragExclusion) {
    if (n < 10) setTimeout(() => reportDragExclusion(n + 1), 300);
    return;
  }
  const r = document.querySelector('.topright').getBoundingClientRect();
  window.SetDragExclusion(Math.ceil(window.innerWidth - r.left), Math.ceil(r.height)).catch(() => {});
}

if (window.WinMinimize) {
  $('btnMin').onclick = () => window.WinMinimize();
  $('btnClose').onclick = () => window.WinClose();
}

reportDragExclusion();
window.addEventListener('resize', reportDragExclusion);

/* ---------- 拖动窗口（按住顶栏空白处） ---------- */
// 为什么要绕页面这一圈：鼠标事件全被 WebView2 那个子窗口吃了，主窗口收不到
// WM_NCHITTEST；系统那套"标题栏拖动"在无边框 + WebView2 下也点不着
// （实测消息发出去，拖动循环根本不启动）。所以干脆由页面盯着鼠标，
// 按增量让后端挪窗口 —— 不挑前台、不依赖消息循环，一定能拖。
const topbarEl = $('topbar');
if (topbarEl && window.DragMove) {
  let drag = null;
  // 屏幕有缩放时 CSS 像素 ≠ 设备像素，得换算，不然窗口会追不上鼠标
  const dpr = () => window.devicePixelRatio || 1;

  topbarEl.addEventListener('pointerdown', e => {
    if (e.button !== 0) return;             // 只认左键
    if (e.target.closest('button')) return; // 按钮有自己要做的事，别抢
    e.preventDefault();
    drag = { id: e.pointerId, x: e.screenX, y: e.screenY };
    try { topbarEl.setPointerCapture(e.pointerId); } catch (_) {} // 移出窗口也要收得到
  });

  topbarEl.addEventListener('pointermove', e => {
    if (!drag || e.pointerId !== drag.id) return;
    const dx = e.screenX - drag.x;
    const dy = e.screenY - drag.y;
    drag.x = e.screenX;
    drag.y = e.screenY;
    if (!dx && !dy) return;
    window.DragMove(Math.round(dx * dpr()), Math.round(dy * dpr())).catch(() => {});
  });

  const endDrag = e => {
    if (!drag || e.pointerId !== drag.id) return;
    drag = null;
    try { topbarEl.releasePointerCapture(e.pointerId); } catch (_) {}
  };
  topbarEl.addEventListener('pointerup', endDrag);
  topbarEl.addEventListener('pointercancel', endDrag);
}

/* ---------- hash 路由（也方便自动截图） ---------- */
const hash = (location.hash || '').slice(1);
if (['games', 'mode', 'create', 'join', 'room', 'help', 'about', 'settings', 'profile', 'tutorial', 'tutorial-join'].includes(hash)) show(hash);
else show('games');

// 头像选择器是纯 DOM 拼的（分类和图库都从 AVATARS 来），先建好再拉设置
buildAvatarPicker();

// 拉一次设置（主题以外的开关都靠它；「记住端口」也要用）
refreshSettings();

// 告诉 Go：界面已经画好了，可以把窗口亮出来了（避免启动时闪白框）
if (window.__ready) window.__ready();
