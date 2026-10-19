/* ---------- 主题 ---------- */
const SUN  = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>';
const MOON = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>';

function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  const isDark = (t === 'dark');
  // 图标显示"点了会变成什么"
  $('themeIcon').innerHTML = isDark ? SUN : MOON;
  $('themeLabel').textContent = isDark ? '浅色' : '深色';
}
applyTheme(document.documentElement.dataset.theme === 'light' ? 'light' : 'dark');

$('themeBtn').onclick = () => {
  const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  applyTheme(next);
  if (window.SaveTheme) window.SaveTheme(next);
};

