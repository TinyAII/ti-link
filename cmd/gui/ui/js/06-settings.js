/* ---------- 设置 ---------- */
let appSettings = null;

// 拉一次设置。绑定万一还没就绪就稍后重试，别让设置页空着。
function refreshSettings(retry) {
  const n = retry || 0;
  if (!window.GetSettings) {
    if (n < 8) setTimeout(() => refreshSettings(n + 1), 300);
    return Promise.resolve();
  }
  return window.GetSettings().then(applySettings).catch(() => {});
}

function applySettings(s) {
  appSettings = s;
  // STUN 列表：空串 = 用内置默认 ⇒ 输入框留空、占位符写默认值 ✓
  const stun = $('stunInput');
  if (stun) {
    stun.value = s.stunServers || '';
    // 📌 占位符要短：把整串默认值塞进去会被输入框截断，看着像乱码 ✗
    //    完整列表放 title，鼠标停上去能看到 ✓
    stun.placeholder = '留空 = 用内置默认';
    stun.title = s.stunDefault ? '内置默认：' + s.stunDefault : '';
  }

  document.querySelectorAll('.switch[data-set]').forEach(el => {
    el.setAttribute('aria-checked', s[el.dataset.set] ? 'true' : 'false');
  });
  if (s.configDir) $('cfgPath').textContent = s.configDir;
  // 数据目录被用户改过才显示「恢复默认」（默认位置时那个按钮没意义 ✓）
  $('btnResetCfg').hidden = !s.customDir;
  // 关窗口的默认动作：它是个下拉（不是开关），所以不能靠 data-set 那套 ✓
  $('closeActionSel').value = ['ask', 'tray', 'quit'].includes(s.closeAction) ? s.closeAction : 'ask';

  // 启动时 Go 侧**静默**查过一次更新 —— 真查到了就**弹窗**告诉用户。
  //
  // 🩸 为什么必须弹窗：原来只在设置页那行小字上显示，可用户根本不会进设置页
  // ⇒ 等于白查 ✗ 更新提示得有存在感 ✓
  // （TakeStartupUpdate 是"取走"语义：一次启动只弹一个窗，来回进设置页不会重复弹）
  if (window.TakeStartupUpdate) {
    window.TakeStartupUpdate().then(info => {
      if (!hasUpdate(info)) return;
      $('updateHint').textContent = '发现新版本 v' + info.version;
      showDownloadBtn(true);
      showUpdateDialog(info);
    });
  }  // ---- 第一次运行：昵称和头像各自动来一份，都只来这一次 ----
  // 判据是那两个 *Set 标记 —— 用户自己动过之后（哪怕把昵称清空）就不再覆盖他的。

  // 昵称：摇一个默认名
  if (!nickRolled && !s.nickSet && !s.nick) {
    nickRolled = true;
    myNick = rollNick();
    if (window.SetSetting) window.SetSetting('nick', myNick);
  } else {
    myNick = (s.nick || '').trim();
  }

  // 头像：随机分一张，下标存进设置，之后一直用它
  if (!avatarRolled && !s.avatarSet) {
    avatarRolled = true;
    myAvatarIdx = Math.floor(Math.random() * Math.max(1, AVATARS.length));
    if (window.SetSetting) window.SetSetting('avatar', String(myAvatarIdx));
  } else {
    myAvatarIdx = s.avatar || 0;
  }

  applyMe();
}

// 两道"只来一次"的闸 —— 设置刷新可能触发多次 applySettings
let nickRolled = false;
let avatarRolled = false;

document.querySelectorAll('.switch[data-set]').forEach(el => {
  el.onclick = async () => {
    const key = el.dataset.set;
    const prev = el.getAttribute('aria-checked');
    const next = prev !== 'true';
    el.setAttribute('aria-checked', next ? 'true' : 'false');
    el.disabled = true;
    const err = await window.SetSetting(key, next ? 'true' : 'false');
    el.disabled = false;
    if (err) {
      el.setAttribute('aria-checked', prev); // 没存上就退回去，别骗人
      setStatus(err, 'err');
      return;
    }
    if (appSettings) appSettings[key] = next;
  };
});

/* ---------- STUN 服务器 ----------
 *
 * 为什么要给入口：内置三个 STUN 在极少数网络下一个都探不到，
 * 那时候用户除了换网没有别的办法 —— 允许他填自己知道的地址 ✓
 *
 * 🩸 保存前必须由后端校验格式：STUN 不通要白等 2.4 秒，
 * 随手粘个网址进去等于每次都变慢 ✗ 后端会给具体哪个不对 ✓
 */
(function () {
  const inp = $('stunInput');
  const rst = $('stunReset');
  if (!inp) return;

  let saved = inp.value;
  const hint = $('stunHint');

  function say(text, cls) {
    if (!hint) return;
    hint.textContent = text || '';
    hint.className = 'sethint' + (cls ? ' ' + cls : '');
  }

  async function commit(v) {
    inp.disabled = true;
    const err = await window.SetSetting('stunServers', v);
    inp.disabled = false;
    if (err) {
      inp.value = saved;          // 没存上就退回去，别让界面和实际不一致
      say(err, 'err');
      return;
    }
    saved = v;
    inp.value = v;
    say(v ? '已保存，下次建房/加入时生效' : '已恢复内置默认', 'ok');
  }

  inp.onchange = () => {
    say('');
    commit(inp.value.trim());
  };

  if (rst) {
    rst.onclick = () => {
      inp.value = '';
      commit('');
    };
  }
})();

/* ---------- 更新检查 ---------- */
// hasUpdate 判断"这条 Go 侧给的信息算不算真的有新版本"。
//
// 🩸🩸 判据必须是 **version**，不能是 `!!info`、也不能只看 url：
//   · Go 侧没有待更新时返回的是 **空 map `{}`**（见 settings_api.go 的 UpdateInfo），
//     而 JS 里 `!!{}` 是 **true** ⇒ 用 `!!info` 会把"没有更新"当成"有更新" ✗
//     症状：每次启动都弹一个没有版本号的更新窗，点「去官网下载」时后端说
//     "还没有可下载的新版本"，而那句话被弹窗盖住 ⇒ 用户看到的是**点了没反应** ✗
//     （2026-10-17 就这么翻的车，排查了半天）
//   · 也不能只看 url：url 万一漏写，后端会退到官网（officialSite），
//     这时候**该显示按钮**而不是藏起来 ✓
//   · version 只在真有更新时才有值 ⇒ 拿它当唯一判据两头的坑都躲开了 ✓
function hasUpdate(info) {
  return !!(info && info.version);
}

// 「去下载」只在**真有**新版本时才露出来。没版本可下的时候杵着一个灰按钮，
// 只会让人以为坏了。
function showDownloadBtn(has) {
  $('btnGoDownload').hidden = !has;
}

let updPending = null; // 当前弹窗对应的更新信息

// 弹「发现新版本」。**两条路都走它**：启动静默查到的、手动点检测查到的 ✓
function showUpdateDialog(info) {
  updPending = info || null;
  const ver = (info && info.version) || '';
  $('updateText').textContent = ver
    ? '新版本 v' + ver + ' 已经发布了，去官网下载更新。'
    : '有新版本可以下载了，去官网看看。';
  const note = (info && info.note) || '';
  $('updateNote').textContent = note;
  $('updateNote').hidden = !note;
  $('updateErr').textContent = '';
  $('updateErr').hidden = true;
  // 没给下载地址就不显示「去下载」—— 一个点了没反应的按钮比没有更糟 ✗
  $('btnUpdGo').hidden = !hasUpdate(info);
  $('updateModal').classList.add('show');
}

function closeUpdateDialog() { $('updateModal').classList.remove('show'); }

// 启动时 Go 侧查完更新会**主动叫这个**（见 main.go 里那段注释）。
// 为什么不让页面自己来取：启动检查是后台跑的，页面加载完那一刻它多半还没回来 ✗
// （用户主动点「检测更新」不走这儿 —— 那条路本来就是点了才查，他在等结果。）
window.onStartupUpdate = function () {
  if (!window.TakeStartupUpdate) return;
  window.TakeStartupUpdate().then(info => {
    if (!hasUpdate(info)) return;
    $('updateHint').textContent = '发现新版本 v' + info.version;
    showDownloadBtn(true);
    showUpdateDialog(info);
  });
};

$('updateX').onclick = closeUpdateDialog;
$('btnUpdLater').onclick = closeUpdateDialog;
$('btnUpdGo').onclick = async () => {
  const err = await window.OpenDownloadPage();
  if (err) {
    // 弹窗还开着的，所以错误必须写在弹窗里 —— 状态栏在它后面看不见 ✗
    $('updateErr').textContent = err;
    $('updateErr').hidden = false;
    return;
  }
  closeUpdateDialog();
};

$('btnCheckUpdate').onclick = async () => {
  $('btnCheckUpdate').disabled = true;
  $('updateHint').textContent = '正在检查…';
  showDownloadBtn(false);    // 上一轮的结论先收起来，免得看着自相矛盾
  const msg = await window.CheckUpdate();
  $('updateHint').textContent = msg || '检查完成';
  // 信息是 Go 侧刚记下的（CheckUpdate 里存的），不是页面拼的
  const info = window.UpdateInfo ? await window.UpdateInfo() : null;
  const has = hasUpdate(info);
  showDownloadBtn(has);
  // 手动问的 ⇒ 每次都弹（他主动点的，不该算打扰）
  if (has) showUpdateDialog(info);
  $('btnCheckUpdate').disabled = false;
};

$('btnGoDownload').onclick = async () => {
  const err = await window.OpenDownloadPage();
  if (err) setStatus(err, 'err');
};

$('btnOpenCfg').onclick = async () => {
  const err = await window.OpenConfigDir();
  if (err) setStatus(err, 'err');
};

// 改数据目录：用户挑一个**父目录**，工具在里面建 p2plink 子目录放东西 ✓
// （不往人家文件夹根上撒文件 ✓ 后端负责搬家，老目录会留着当备份 ✓）
$('btnPickCfg').onclick = async () => {
  const err = await window.ChooseDataDir();
  if (err) { setStatus(err, 'err'); return; }
  await refreshSettings(); // 路径那一行、还有「恢复默认」的显隐都跟着变
  setStatus('配置位置已改', 'ok');
};

$('btnResetCfg').onclick = async () => {
  const err = await window.ResetDataDir();
  if (err) { setStatus(err, 'err'); return; }
  await refreshSettings();
  setStatus('已回到默认位置', 'ok');
};

// 关窗口的默认动作：下拉不是开关，得单独接线 ✓
$('closeActionSel').onchange = async () => {
  const err = await window.SetSetting('closeAction', $('closeActionSel').value);
  if (err) setStatus(err, 'err');
};

$('btnResetSet').onclick = async () => {
  const err = await window.ResetSettings();
  if (err) { setStatus(err, 'err'); return; }
  $('updateHint').textContent = '手动检查是否有新版本可用';
  await refreshSettings();
  setStatus('已恢复默认设置', 'ok');
};

/* ---------- 复制 ---------- */
function copyText(s, hint) {
  const done = () => setStatus(hint, 'ok');
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(s).then(done).catch(() => { fallbackCopy(s); done(); });
  } else { fallbackCopy(s); done(); }
}
function fallbackCopy(s) {
  const ta = document.createElement('textarea');
  ta.value = s; document.body.appendChild(ta); ta.select();
  try { document.execCommand('copy'); } catch (e) {}
  document.body.removeChild(ta);
}
/* ---------- 日志：复制 / 导出 ---------- */
// 复制走的是界面上看到的那份 —— **不含任何地址**。
// 导出可以带地址，但那份也是打码的（只留 IP 前两段 + 端口）；
// 完整 IP 在客户端的任何地方都不出现（界面、复制、导出都不行）。
function logTextOf(termId) {
  const lines = document.querySelectorAll('#' + termId + ' .ln');
  return Array.from(lines).map(el => el.textContent).join('\n');
}

function bindLogTools(suffix) {
  const copyBtn = $('copyLog' + suffix);
  const termId = 'term' + suffix;
  if (!copyBtn) return;
  copyBtn.onclick = () => {
    const t = logTextOf(termId);
    if (!t) { setStatus('还没有日志可复制', 'err'); return; }
    copyText(t, '日志已复制');
  };
}
bindLogTools('Room');

