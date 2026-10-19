/* ---------- 网络自检页：四项检测 + 总览 ----------
 *
 * 2026-10-18 重构：原来是三个文件（打洞 / IPv6 / 连接与代理）各写一套
 * "加载态 + 渲染 + 报错"，三份几乎一样的代码 ✗
 * 现在合成一份，四个检测只描述"怎么跑、结果怎么画"，公共部分由 runOne 统一处理 ✓
 *
 * 老板的两轮反馈决定了这一版的形状：
 *   ① "从设置里剥离出去，侧边栏新开一列" ⇒ 独立成页
 *   ② "这样显示不直观" ⇒ 加**状态徽章**（不点也能看到好坏）+ **总览条** + 一键全测
 *
 * 🩸 接口是否存在要在**点击时**判，不能在加载时判 ——
 *    加载时判的话，一旦那时还没绑定好，按钮就成了"点了没反应、还不报错" ✗
 */
(function () {
  // ---------- 工具 ----------

  // 打码：IPv4 只留前两段（跟客户端日志一个口径）
  function maskV4(s) {
    const i = s.lastIndexOf(':');
    if (i < 0) return s;
    const host = s.slice(0, i), port = s.slice(i);
    const parts = host.split('.');
    if (parts.length === 4) return parts[0] + '.' + parts[1] + '.*.*' + port;
    return host + port;
  }

  // 打码：v6 只留前两组（/32）。
  // 📌 别按字符串位置硬切 —— `::` 压缩形式会切串（踩过）✗
  function maskV6(s) {
    let host = s, tail = '';
    if (s.startsWith('[')) {
      const i = s.indexOf(']');
      if (i > 0) {
        host = s.slice(1, i);
        tail = s.slice(i + 1);
      }
    }
    const parts = host.split(':').filter((x) => x !== '');
    if (parts.length < 2) return s;
    const masked = parts[0] + ':' + parts[1] + ':**';
    return tail ? '[' + masked + ']' + tail : masked;
  }

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  // 两列明细（左名右值）—— 四张卡都用这个
  function rows(out, list) {
    if (!list.length) return;
    const box = el('div', 'natprobes');
    for (const [k, v, tone] of list) {
      const row = el('div', 'natrow');
      row.appendChild(el('span', 'natsrv', k));
      row.appendChild(el('b', tone || 'ok', v));
      box.appendChild(row);
    }
    out.appendChild(box);
  }

  // ---------- 四个检测 ----------

  const CHECKS = [
    {
      key: 'server',
      short: '服务器',
      btn: 'serverCheckBtn',
      out: 'serverResult',
      badge: 'badgeServer',
      api: 'ServerCheck',
      wait: '正在连接…',
      // verdict → 徽章颜色：能连=绿，慢=黄，连不上=红
      tone: { ok: 'ok', slow: 'warn', down: 'err' },
      body: (out, r) => {
        // 📌 不显示服务器地址：界面上只报"通不通、多快"（老板要求）
        const list = [];
        if (r.rtts && r.rtts.length) {
          list.push(['往返（各次）', r.rtts.map((x) => x + ' ms').join(' / ')]);
        }
        rows(out, list);
      },
    },
    {
      key: 'nat',
      short: '打洞',
      btn: 'natCheckBtn',
      out: 'natResult',
      badge: 'badgeNat',
      api: 'NatCheck',
      wait: '正在问几个 STUN 服务器…',
      tone: { cone: 'ok', symmetric: 'err', cgnat: 'err', blocked: 'warn', unknown: 'warn' },
      body: (out, r) => {
        rows(out, (r.probes || []).map((p) =>
          p.mapped
            ? [p.server, maskV4(p.mapped), 'ok']
            : [p.server, '无响应', 'err']));
      },
    },
    {
      key: 'v6',
      short: 'IPv6',
      btn: 'ipv6CheckBtn',
      out: 'v6Result',
      badge: 'badgeV6',
      api: 'IPv6Check',
      wait: '正在检查本机地址和出网…',
      // 🩸 IPv6 是**加分项**，不是必备条件（老板 2026-10-18）：
      //    标红会让人以为"软件我不能用"✗ 所以缺省一律用中性灰，
      //    文案也换成"没开"而不是"不通" ✓
      optional: true,
      tone: { ok: 'ok', off: 'mute', 'no-address': 'mute', 'no-route': 'mute' },
      badgeText: { ok: '✓ 可用', mute: '— 没开' },
      body: (out, r) => {
        const list = (r.addrs || []).map((a) => ['本机 IPv6', maskV6(a)]);
        if (r.target) list.push(['出网测试目标', maskV6(r.target)]);
        rows(out, list);
      },
    },
    {
      key: 'proxy',
      short: '代理/VPN',
      btn: 'proxyCheckBtn',
      out: 'proxyResult',
      badge: 'badgeProxy',
      api: 'ProxyCheck',
      wait: '正在检查…',
      tone: { ok: 'ok', proxy: 'warn', vpn: 'warn' },
      body: (out, r) => {
        const list = [];
        if (r.sysProxy) list.push(['系统代理', r.sysProxy, 'warn']);
        if (r.pac) list.push(['自动配置脚本', r.pac, 'warn']);
        if (r.envProxy) list.push(['环境变量代理', r.envProxy, 'warn']);
        if (r.vpn && r.vpn.length) list.push(['虚拟网卡', r.vpn.join('、'), 'warn']);
        rows(out, list);
      },
    },
  ];

  const state = {}; // key -> { tone, headline }

  // ---------- 徽章与总览 ----------

  const BADGE = { ok: '✓ 正常', warn: '! 注意', err: '✗ 不通' };

  function setBadge(c, tone, headline) {
    const b = $(c.badge);
    if (!b) return;
    b.className = 'diagbadge show ' + tone;
    b.textContent = (c.badgeText && c.badgeText[tone]) || BADGE[tone] || tone;
    b.title = headline || '';
  }

  // refreshSummary 把所有已测项汇总成一句话。
  //
  // 🩸 只报"几项正常"没用 —— 必须点出**是哪几项**，否则用户还得自己一行行看 ✗
  function refreshSummary() {
    const box = $('diagSummary');
    const text = $('diagSummaryText');
    if (!box || !text) return;

    const done = CHECKS.filter((c) => state[c.key]);
    if (!done.length) {
      box.hidden = true;
      return;
    }

    // 🩸 可选项（IPv6）**不进"有问题"的统计**：
    //    绝大多数人本来就没有 IPv6，把它算成问题会让所有人以为自己坏了 ✗
    const required = done.filter((c) => !c.optional);
    const bad = required.filter((c) => state[c.key].tone === 'err');
    const warn = required.filter((c) => state[c.key].tone === 'warn');
    const okN = required.length - bad.length - warn.length;
    const optIssue = done.filter((c) => c.optional && state[c.key].tone !== 'ok');
    const optNote = optIssue.length ? '（IPv6 没开，不影响使用）' : '';

    box.hidden = false;
    let tone = 'ok';
    let msg = '';

    if (bad.length) {
      tone = 'err';
      msg = `${bad.length} 项有问题：` + bad.map((c) => c.short).join('、');
      // 🩸 有问题时**也要把"注意"的列出来** ——
      //    否则徽章上明明显示「! 注意」，总览里却一句不提，两处对不上 ✗
      if (warn.length) msg += '；' + warn.length + ' 项需要注意：' + warn.map((c) => c.short).join('、');
      if (okN) msg += `（${okN} 项正常）`;
    } else if (warn.length) {
      tone = 'warn';
      msg = `${warn.length} 项需要注意：` + warn.map((c) => c.short).join('、');
      if (okN) msg += `（${okN} 项正常）`;
    } else {
      msg = '需要的东西都正常';
      if (done.length < CHECKS.length) msg += '（还有没测的）';
    }
    msg += optNote;

    box.className = 'diagsummary ' + tone;
    text.innerHTML = '';
    text.appendChild(el('b', '', msg));
    // 把"该动哪一层"简要带上 —— 这正是这个页面的价值
    if (bad.length || warn.length) {
      text.appendChild(document.createTextNode('。展开有问题的项，里面写着该去动哪儿'));
    } else if (optIssue.length) {
      text.appendChild(document.createTextNode('。IPv6 是加分项，没有它也能正常联机'));
    }
  }

  // ---------- 跑一个检测 ----------

  async function runOne(c) {
    const btn = $(c.btn);
    const out = $(c.out);
    if (!btn || !out) return null;

    if (typeof window[c.api] !== 'function') {
      out.innerHTML = '';
      out.appendChild(el('p', 'nathead err', '这个检测没接上（' + c.api + ' 不可用），请重新打开程序再试'));
      return null;
    }

    btn.disabled = true;
    btn.textContent = '检测中…';
    out.innerHTML = '';
    out.appendChild(el('p', 'modaldesc', c.wait));

    let r = null;
    try {
      r = await window[c.api]();
    } catch (e) {
      r = { error: String((e && e.message) || e) };
    }

    btn.disabled = false;
    btn.textContent = '重新检测';

    if (!r || r.error) {
      out.innerHTML = '';
      out.appendChild(el('p', 'nathead err', '检测没跑起来：' + ((r && r.error) || '未知原因')));
      state[c.key] = null;
      return null;
    }

    const tone = c.tone[r.verdict] || 'warn';
    out.innerHTML = '';
    out.appendChild(el('p', 'nathead ' + tone, r.headline || '检测完成'));
    out.appendChild(el('p', 'natdetail', r.detail || ''));
    if (c.body) c.body(out, r);

    state[c.key] = { tone, headline: r.headline };
    setBadge(c, tone, r.headline);
    refreshSummary();
    return r;
  }

  // ---------- 接线 ----------

  CHECKS.forEach((c) => {
    const b = $(c.btn);
    if (b) b.onclick = () => runOne(c);
  });

  const all = $('diagRunAll');
  if (all) {
    all.onclick = async () => {
      all.disabled = true;
      all.textContent = '检测中…';
      // 并行跑：四项互不依赖，串着跑要白等好几秒 ✗
      await Promise.all(CHECKS.map((c) => runOne(c)));
      all.disabled = false;
      all.textContent = '全部重测';
    };
  }
})();
