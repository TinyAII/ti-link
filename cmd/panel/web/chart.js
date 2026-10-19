'use strict';
/* 手写的 SVG 折线图。
 *
 * 为什么不引 Chart.js 之类：
 *   ① 面板的 CSP 写死了"只允许自己的资源"（连内联脚本都不许）——
 *      引库就得把 CDN 放进白名单，而万一以后在内网/没外网的环境打开，图会白屏 ✗
 *   ② 要的东西就"几条线 + 网格 + 坐标"，自己画一百多行，完全可控 ✓
 *   ③ 不引入依赖 = 以后不用跟着它升级 ✓
 *
 * 📌 这里只负责"把数字画成图"。取数、排版、自动刷新都在 app.js 里 ✓
 */

const NS = 'http://www.w3.org/2000/svg';

function svgEl(tag, attrs) {
  const e = document.createElementNS(NS, tag);
  for (const k in attrs) e.setAttribute(k, attrs[k]);
  return e;
}

/* niceMax 把纵轴上限抬到一个"整"的数（1/2/5 × 10^n）。
 * 不抬的话轴上限是数据的最大值 ⇒ 标签会写成 37.421 这种，很难看 ✗ */
function chNiceMax(v) {
  if (!(v > 0)) return 1;
  const p = Math.pow(10, Math.floor(Math.log10(v)));
  for (const m of [1, 2, 2.5, 5, 10]) {
    if (v <= m * p) return m * p;
  }
  return 10 * p;
}

/* chNum 把数字收成合适的小数位。
 * 📌 图上每个被显示的数都要过这里 —— 原始的 8.333333333 直接写上去太业余 ✓
 * 🩸 它**只给数字**、不给单位：卡片是「大数字 + 小号单位」的样式，
 *    单位得单独放（见 style.css 的 .stat .u）。拼成一串的话单位就变成大号字 ✗
 *    （2026-10-16 老板点名："连单位都没有" —— 那时单位压根没输出 ✗） */
function chNum(v) {
  if (v === null || v === undefined || isNaN(v)) return '—';
  const a = Math.abs(v);
  if (a >= 1000) return Math.round(v).toLocaleString('en-US');
  if (a >= 100) return v.toFixed(0);
  if (a >= 10) return v.toFixed(1);
  return v.toFixed(2);
}

/** chBytes 内存量（进来的单位是 MB）：够 1024 就换 GB，返回 [数字, 单位] 两截 ✓ */
function chBytes(mb) {
  if (mb === null || mb === undefined || isNaN(mb)) return ['—', ''];
  if (Math.abs(mb) >= 1024) return [chNum(mb / 1024), 'GB'];
  return [chNum(mb), 'MB'];
}

/** chInt 给**计数**用：连接数 2 就该显示 2，不该是 2.00 ✗ */
function chInt(v) {
  if (v === null || v === undefined || isNaN(v)) return '—';
  return Math.round(v).toLocaleString('en-US');
}

/** chAxis 是**坐标轴**上的数字：50 不要写成 50.0，1000 不要写成 1,000（轴窄）✓ */
function chAxis(v) {
  if (!isFinite(v)) return '';
  if (v >= 100) return String(Math.round(v));
  if (v >= 10) return String(Math.round(v * 10) / 10);
  return String(Math.round(v * 100) / 100);
}

function chTime(ts) {
  const d = new Date(ts * 1000);
  const p = (n) => String(n).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes());
}

/* lineChart 画一张折线图。
 *
 *   host    容器元素
 *   opts.title   标题
 *   opts.unit    单位（只影响标签）
 *   opts.series  [{name, color, data:[...], fill?:bool}]
 *   opts.t       时间戳数组（跟每条 data 下标对齐）
 *   opts.pct     纵轴按 0~100 固定（CPU / 内存用，比自适应好读）
 *   opts.warn/max 阈值线（画一条虚线，超了线变黄/红）
 */
// 每张图最后一次悬停在哪（跨重绘保留，见 lineChart 里的说明）
// key 用标题：同一页里标题是唯一的 ✓
const chartHover = new Map();

function lineChart(host, opts) {
  host.textContent = '';
  const W = 100, H = 34; // viewBox 单位：让它自己缩放，不用管真实像素 ✓
  const padL = 7, padR = 1.5, padT = 3.5, padB = 5.5;
  const iw = W - padL - padR, ih = H - padT - padB;

  const series = (opts.series || []).filter((s) => s && s.data && s.data.length);
  const n = series.length ? series[0].data.length : 0;

  const head = document.createElement('div');
  head.className = 'charthead';
  const t = document.createElement('div');
  t.className = 'charttitle';
  t.textContent = opts.title;
  head.appendChild(t);
  const lg = document.createElement('div');
  lg.className = 'legend';
  for (const s of series) {
    const it = document.createElement('span');
    it.className = 'lgitem';
    const dot = document.createElement('span');
    dot.className = 'lgdot';
    dot.style.background = s.color;
    it.appendChild(dot);
    const nm = document.createElement('span');
    nm.textContent = s.name;
    it.appendChild(nm);
    const last = s.data[s.data.length - 1];
    const val = document.createElement('b');
    val.textContent = (opts.int ? chInt(last) : chNum(last)) + (opts.unit ? ' ' + opts.unit : '');
    it.appendChild(val);
    lg.appendChild(it);
  }
  head.appendChild(lg);
  host.appendChild(head);

  if (!n) {
    const empty = document.createElement('div');
    empty.className = 'chartempty';
    empty.textContent = '还没有数据 —— 采样器刚起来，等十几秒';
    host.appendChild(empty);
    return;
  }

  let max = 0;
  for (const s of series) for (const v of s.data) if (v > max) max = v;
  const top = opts.pct ? 100 : chNiceMax(max * 1.12);
  const X = (i) => padL + (n <= 1 ? iw / 2 : (i / (n - 1)) * iw);
  const Y = (v) => padT + ih - Math.min(1, Math.max(0, v / top)) * ih;

  const svg = svgEl('svg', {
    viewBox: '0 0 ' + W + ' ' + H,
    preserveAspectRatio: 'none',
    class: 'chart',
  });

  // 横向网格 + 纵轴标签（3 档）
  for (const frac of [0, 0.5, 1]) {
    const y = padT + ih - frac * ih;
    svg.appendChild(svgEl('line', {
      x1: padL, y1: y, x2: W - padR, y2: y,
      stroke: 'var(--line)', 'stroke-width': 0.18,
      'vector-effect': 'non-scaling-stroke',
    }));
    const lab = svgEl('text', {
      x: 0, y: y + 1.1, 'font-size': 2.6, fill: 'var(--dim)',
    });
    // 计数类（连接 / 房间 / 次数）的轴只印整数 —— 印出 0.5 个连接很奇怪 ✗
    lab.textContent = opts.int ? String(Math.round(top * frac)) : chAxis(top * frac);
    svg.appendChild(lab);
  }

  // 阈值线（比如 CPU 80%）
  if (opts.warn && top > 0) {
    const y = Y(opts.warn);
    svg.appendChild(svgEl('line', {
      x1: padL, y1: y, x2: W - padR, y2: y,
      stroke: 'var(--warn)', 'stroke-width': 0.6, 'stroke-dasharray': '1.4 1.4',
      'vector-effect': 'non-scaling-stroke', opacity: 0.7,
    }));
  }

  for (const s of series) {
    const path = s.data
      .map((v, i) => (i ? 'L' : 'M') + X(i).toFixed(2) + ' ' + Y(v).toFixed(2))
      .join(' ');
    if (s.fill) {
      const area = path + ' L' + X(n - 1).toFixed(2) + ' ' + Y(0) +
        ' L' + X(0).toFixed(2) + ' ' + Y(0) + ' Z';
      svg.appendChild(svgEl('path', { d: area, fill: s.color, opacity: 0.13, stroke: 'none' }));
    }
    svg.appendChild(svgEl('path', {
      d: path, fill: 'none', stroke: s.color,
      'stroke-width': 1.4, 'stroke-linejoin': 'round', 'stroke-linecap': 'round',
      'vector-effect': 'non-scaling-stroke',
    }));
  }

  host.appendChild(svg);

  /* ---------- 悬浮提示 ----------
   *
   * 🩸 两个坑必须处理，不然这功能等于没做：
   *  ① SVG 是 viewBox=100x34 + preserveAspectRatio=none **拉伸**的
   *     ⇒ 不能拿像素直接算索引，得先按 rect 宽度归一到 viewBox 坐标 ✓
   *  ② 概览/大屏**每秒重绘**（整块 DOM 重建）⇒ 提示框会被抹掉、每秒闪一下 ✗
   *     ⇒ 把"鼠标最后停在哪个 x"记在模块级的 chartHover 里，重建后对回去 ✓
   */
  const cross = svgEl('line', {
    x1: 0, x2: 0, y1: padT, y2: padT + ih,
    stroke: 'var(--dim)', 'stroke-width': 0.14,
    'vector-effect': 'non-scaling-stroke', visibility: 'hidden',
    'pointer-events': 'none',
  });
  svg.appendChild(cross);

  const dots = series.map((s2) => {
    const c = svgEl('circle', {
      r: 1.5, cx: 0, cy: 0, fill: s2.color,
      visibility: 'hidden', 'pointer-events': 'none',
    });
    svg.appendChild(c);
    return c;
  });

  const tip = document.createElement('div');
  tip.className = 'charttip';
  tip.hidden = true;
  host.appendChild(tip);

  // 把鼠标的 clientX 换成最近的**数据点**索引。
  // 📌 吸附到最近的实点，不做插值 —— 插出来的值是编的，看着专业其实在骗人 ✗
  const idxAt = (clientX) => {
    const r = svg.getBoundingClientRect();
    if (!r.width || n <= 0) return 0;
    const vx = ((clientX - r.left) / r.width) * W;
    const i = n <= 1 ? 0 : Math.round(((vx - padL) / iw) * (n - 1));
    return Math.max(0, Math.min(n - 1, i));
  };

  const paint = (i, clientX, clientY) => {
    const x = X(i);
    cross.setAttribute('x1', x.toFixed(2));
    cross.setAttribute('x2', x.toFixed(2));
    cross.setAttribute('visibility', 'visible');
    series.forEach((s2, k) => {
      dots[k].setAttribute('cx', x.toFixed(2));
      dots[k].setAttribute('cy', Y(s2.data[i]).toFixed(2));
      dots[k].setAttribute('visibility', 'visible');
    });

    // 内容
    tip.textContent = '';
    const tt = document.createElement('div');
    tt.className = 'tt-t';
    tt.textContent = opts.t && opts.t[i] ? chTime(opts.t[i]) : ('第 ' + (i + 1) + ' 点');
    tip.appendChild(tt);
    for (const s2 of series) {
      const row = document.createElement('div');
      row.className = 'tt-row';
      const dot = document.createElement('span');
      dot.className = 'tt-dot';
      dot.style.background = s2.color;
      row.appendChild(dot);
      const nm = document.createElement('span');
      nm.textContent = s2.name;
      row.appendChild(nm);
      const val = document.createElement('b');
      const v = s2.data[i];
      val.textContent = (opts.int ? chInt(v) : chNum(v)) + (opts.unit ? ' ' + opts.unit : '');
      row.appendChild(val);
      tip.appendChild(row);
    }
    tip.hidden = false;

    // 位置：**跟着光标**走（别用 svg.offsetTop —— 那是相对 offsetParent 的，
    // 各层 padding 一改就飘；用光标自己的坐标最直接 ✓）。
    // 右边放不下就翻到光标左侧，上下也夹在框里，免得被裁掉 ✗
    const hr = host.getBoundingClientRect();
    const w = tip.offsetWidth, h = tip.offsetHeight;
    let left = clientX - hr.left + 14;
    if (left + w > hr.width - 4) left = clientX - hr.left - w - 14;
    left = Math.max(4, Math.min(hr.width - w - 4, left));
    let top = clientY - hr.top - h / 2;
    top = Math.max(2, Math.min(hr.height - h - 2, top));
    tip.style.left = left + 'px';
    tip.style.top = top + 'px';
  };

  const hide = () => {
    cross.setAttribute('visibility', 'hidden');
    for (const d of dots) d.setAttribute('visibility', 'hidden');
    tip.hidden = true;
  };

  svg.addEventListener('mousemove', (e) => {
    chartHover.set(opts.title, { x: e.clientX, y: e.clientY });
    paint(idxAt(e.clientX), e.clientX, e.clientY);
  });
  svg.addEventListener('mouseleave', () => {
    chartHover.delete(opts.title);
    hide();
  });

  // 重绘之后接上：鼠标如果还停在这张图上，就照原样再画一次 ✓
  //
  // 🩸 必须**延后一帧**：调用方是先 lineChart(box) 再 charts.appendChild(box)，
  //    所以这会儿宿主还没挂进文档 ⇒ getBoundingClientRect 全是 0、
  //    elementFromPoint 也命中不了 ⇒ 恢复永远不生效，提示框每秒被抹掉 ✗
  //    （实测：直接调用的话截图里根本看不到提示框）
  const last = chartHover.get(opts.title);
  if (last) {
    requestAnimationFrame(() => {
      const el = document.elementFromPoint(last.x, last.y);
      if (el && host.contains(el)) paint(idxAt(last.x), last.x, last.y);
    });
  }

  // 横轴的起止时刻（只标两头，中间标了就挤了）
  const foot = document.createElement('div');
  foot.className = 'chartfoot';
  const a = document.createElement('span');
  a.textContent = opts.t && opts.t.length ? chTime(opts.t[0]) : '';
  const b = document.createElement('span');
  b.textContent = opts.t && opts.t.length ? chTime(opts.t[opts.t.length - 1]) : '';
  b.className = 'now';
  foot.appendChild(a);
  foot.appendChild(b);
  host.appendChild(foot);
}
