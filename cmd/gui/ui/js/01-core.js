const $ = (id) => document.getElementById(id);

/* ============ 游戏列表：**唯一的数据源** ============

   每个游戏的全部信息都在这一条里 —— 名字、端口、封面、房间页标签，
   以及**各个页面上那些"按游戏不一样的话"**。

   🔑 **加一个新游戏 = 往这个列表加一条**。HTML 不用动、渲染函数不用动 ✓

   以前这些东西分散在帮助页、房主页、面包屑……好几个地方手写，
   加个游戏要改四五处、还每次都可能漏掉一个 ✗
   （2026-10-15 一天之内连漏四次：房间页标题 / 房主页三条提示 / 面包屑 / 房间页封面）
   ================================================== */

const ICON_HOST = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 20V10l9-6 9 6v10"/><path d="M9 20v-6h6v6"/></svg>';
const ICON_GUEST = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1 1"/><path d="M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1-1"/></svg>';
const ICON_INFO = '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>';

// 所有游戏共通的那几条常见问题。渲染时固定带上 ——
// 每个游戏各抄一遍是纯重复，改一处就得改四处 ✗
const COMMON_TIPS = [
  ['两边都要装这个软件吗？',
   '要。房主和朋友各跑一份 —— 它是在<b>两台电脑之间</b>架一条直连通道，单独一边没法用。'],
  ['房主的游戏必须先开着吗？',
   '要。房主那边就是「服务器」那一端，游戏一关，朋友那头就没东西可连了。'],
  ['房间 ID 每次都一样吗？',
   '不一样。每次建房都会生成一个新的；人走光了房间就结束，下次开房是新的 ID。']
];

const GAMES = [
  {
    name: '我的世界', sub: 'Java 版', port: 25565, cover: '', roomLabel: '我的世界 Java 版',
    proto: 'tcp', // Java 版走 TCP ✓

    // 进程名关键词。**两个用途**（语义是同一个：这游戏跑在什么进程里）：
    //   ① 建房前校验"这个端口上真的是它吗"（挡滥用，见 cmd/gui 的 checkGameOnPort）
    //   ② 那个状态灯的 proc 那一路
    //
    // ⚠️ java / javaw 是**很宽泛**的关键词 —— 任何 java 程序都叫这个，
    // 所以它能挡住"填 8080 的随便一个程序"，但挡不住"另一个 java 程序" ✗
    // 要做到后者得去读命令行参数（看是不是 -jar server.jar），复杂一档，先不做 ✓
    // （客户端是 javaw，服务端是 java —— 两个都写上 ✓）
    procMatch: ['java', 'javaw'],

    // 帮助页「第一次联机」：房主 / 朋友各一张卡，卡里几行话
    help: {
      host: [
        '先把游戏开起来（开服务端，或进游戏按 <b>Esc</b> → <b>对局域网开放</b>）',
        '回到本软件 →「创建房间」→ 填端口 → 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '打开游戏 → 多人游戏 → 直接连接 → 输入界面给的地址'
      ],
      // 只写"这个游戏自己不一样"的 —— 共通的那几条在 COMMON_TIPS 里 ✓
      tips: [
        ['报「无效会话 / Invalid session」怎么办？',
         '房主服务端开着正版验证。打开服务端目录下的 <code>server.properties</code>，' +
         '把 <code>online-mode=true</code> 改成 <code>online-mode=false</code>，然后重启服务端。']
      ]
    },

    // 房主页「注意事项」里按游戏变的三条
    notice: {
      port:    '用「对局域网开放」时端口每次都会变，记得到工具里<strong>填新的那个</strong>。',
      version: '<strong>版本、模组必须完全一致</strong>（含整合包），差一点都进不去。',
      fail:    '报「无效会话」→ 让房主把服务端的 <code>online-mode</code> 改成 <code>false</code>。'
    }
  },
  {
    name: '泰拉瑞亚', sub: 'Terraria', port: 7777, cover: 'cover-terraria', roomLabel: '泰拉瑞亚',
    // 官方 Wiki 写的是"服务端跑在 TCP 7777 上"，但也提过云服务器上
    // **TCP 和 UDP 都要放行** ⇒ 说明除了主连接的 TCP，还有些东西走 UDP ✓
    // 2026-10-15 实测：只开 TCP 就能进游戏 ✓ 但开 UDP 多一路转发、代价很小，
    // 万一某些交互依赖它也不会出问题 ⇒ 给它 both 更保险 ✓
    proto: 'both',

    // 见"我的世界"那条的说明：既是建房校验的依据，也是状态灯 proc 那一路的依据 ✓
    procMatch: ['terraria'],

    help: {
      host: [
        '在泰拉瑞亚里开一个多人房间：主菜单 →「<b>多人游戏</b>」→「<b>开服并玩</b>」',
        '选角色、选世界 → 记下你设的<b>端口</b>（默认 7777）和密码 → 进游戏',
        '回到本软件 →「创建房间」→ 端口填<b>刚才那个</b> → 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '打开泰拉瑞亚 →「多人游戏」→「<b>通过 IP 加入</b>」',
        '填<b>界面上给的地址和端口</b>；房主设了密码就一起填上'
      ],
      tips: [
        // 📌 这条是**实测出来的**（2026-10-15 老板亲测）：
        // 隧道通了、能连上，但版本低的那一边会被游戏告知"版本太低" ⇒ 卡在这儿
        ['两边版本不一样能连吗？',
         '不能 —— 版本低的那边会提示「版本太低」，进不去。<b>两边必须完全一致</b>' +
         '（比如都是 1.4.4），差一个小版本都不行。让版本低的那边更新一下就好。']
      ]
    },

    notice: {
      port:    '泰拉瑞亚的端口是开服时自己定的（默认 7777）—— 要和工具里填的<strong>保持一致</strong>。',
      version: '<strong>版本要完全一致</strong>（比如都是 1.4.4）—— 装了 mod 的话，两边也得装同样的。',
      fail:    '连不上先核对<strong>端口和密码</strong> —— 泰拉瑞亚的密码是开服时设的，两边得一样。'
    }
  },

  // 下面两个**还没写过专属文案** ⇒ 不带 help / notice ⇒ 渲染时自动走兜底 ✓
  // 宁可给通用套路，也不能让人照着别家游戏的菜单去找 ✗
  //
  // ⚠️ proto 这一栏：**没实测过的一律给 both**
  //
  // 🩸 原来我给它们填的是 'tcp'（想省资源）—— 方向选反了 ✗
  // 两个错误的代价完全不对等：
  //   多开一路 UDP ⇒ 几乎零代价
  //   少开一路 UDP ⇒ 那个游戏**直接用不了**（2026-10-15 星露谷就是这样，
  //                 它其实是 UDP 的，我却按 tcp 配了）
  // ⇒ 不确定的时候，选"保证能用"那边 ✓
  // ⚠️ 星露谷：官方 troubleshooting 写的是 **"PC: TCP/UDP: 24642"** ⇒ 两个都用 ✓
  // （一开始我填 tcp、后来听老板说 UDP 又改成 udp —— 都不准，官方文档是 both ✓）
  //
  // ⭐ 实测（2026-10-15 老板自己跑的 netstat）：
  //     UDP  0.0.0.0:24642   ← **只有 UDP，一行 TCP 都没有** ✓
  // ⇒ 它其实**只监听 UDP**；官方写的 "TCP/UDP" 是"防火墙要放行的端口"，
  //   不等于游戏真的开 TCP ✓ 所以那个"游戏开没开"的灯对它**必然探不到** ✓
  //
  // addrStyle: 'host-only' ⇒ 给它填地址时**只填 IP、不要端口** ✓
  // （端口是游戏内置的，带上它游戏那边会当成无效地址 ✗）
  {
    name: '星露谷物语', sub: 'Stardew', port: 24642, cover: 'cover-stardew',
    roomLabel: '星露谷物语', proto: 'both', addrStyle: 'host-only',

    // ── 怎么探"游戏开没开" ──
    //
    // 📌 写成**列表**，按顺序试，**任一成功就算开着** ✓
    //   'tcp'  连一下它的 TCP 端口
    //   'proc' 查本机这个 UDP 端口是不是目标进程在听
    //   []     什么都不试 ⇒ 界面如实说"测不了"
    //
    // 🔑 这里之所以是 `['tcp', 'proc']` 而不是单个 'proc'：
    // 老板定的原则是「**能 TCP 就 TCP，进程是没办法了才看**」——
    // 而"两路都留着"比"二选一"稳：
    //   TCP 通了 ⇒ 直接绿灯，不用管进程名叫啥（玩家改了文件名也不怕 ✓）
    //   TCP 不通 ⇒ 再看进程，兜住"TCP 探不到但游戏确实开着"的情况 ✓
    probe: ['tcp', 'proc'],

    // 进程名关键词，**可以是多个**（任意一个命中就算 ✓）
    //
    // 🩸 为什么不是写死一个完整进程名：这名字**会变** ——
    // 游戏更新可能改、Steam/GOG 版写法可能不同、**玩家自己也能改 exe 文件名**
    // ⇒ 所以①只写短关键词（容纳更多写法）②允许多写几个 ③**匹配不上
    //    绝不能报"游戏没开"**（那两件事不一样，见 cmd/gui 的探测逻辑）✓
    procMatch: ['stardew'],

    help: {
      host: [
        '游戏里先开好：「<b>选项</b>」→「<b>多人游戏</b>」→ 打开「<b>允许局域网连接</b>」' +
        ' —— 不开这个，谁也进不来',
        '给每个朋友在<b>罗宾的木匠商店</b>建一间<b>小屋</b>，一人一间 —— 没小屋的人进不来',
        '存好档退回标题界面 → 点「<b>合作</b>」（<b>不是</b>「加载」）→ 选存档开始',
        '回到本软件 →「创建房间」→ 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '打开星露谷 →「合作」→ 切到「<b>加入</b>」标签 →「<b>加入局域网游戏</b>」',
        '地址填<b>界面上给的那个</b> —— <b>只填 IP，不要带端口</b>（端口是游戏内置的）',
        '它<b>不会自己</b>从列表里冒出来：我们转发的是单播，广播过不去 ⇒ 得手动填 ✓'
      ],
      tips: [
        ['为什么它检测不到我开没开？',
         '星露谷只走 UDP，而我们的检测手段是连 TCP —— 探不到是<b>正常的</b>。' +
         '游戏开着就行，不用管那个灯。'],
        ['版本不一样能连吗？',
         '不能。版本要一致；装了 mod 的话，两边得装同样的。']
      ]
    },

    notice: {
      port:    '星露谷的端口是游戏内置的（24642）—— 界面上那个地址<b>照抄就行</b>。',
      version: '<strong>两边版本要一致</strong>；装了 mod 的话，两边也得装同样的。',
      fail:    '连不上先看两条：游戏里「<b>允许局域网连接</b>」开了没、朋友那边<b>有没有空小屋</b>。'
    }
  },
  { name: '生存战争', sub: 'Survivalcraft 2', port: 4234, cover: 'cover-survival', roomLabel: '生存战争', proto: 'both' },

  {
    // 幻兽帕鲁（Palworld）
    //
    // 🩸 三条都要点（老板给的信息，已按它设计）：
    //   ① 主端口**只有 UDP 8211** —— 单端口，不像饥荒要"地面+洞穴"两路 ✓
    //   ② UDP 8212 / 27015 只是"社区服务器列表可见"用的，
    //      好友走 IP 直连**不需要** ⇒ 我们不碰 ✓
    //   ③ TCP 8212(REST) / 25575(RCON) 是服务器管理接口
    //      ⇒ **绝对不能放行** ✓（本软件只开 8211 这一路）
    name: '幻兽帕鲁', sub: 'Palworld', port: 8211, cover: 'cover-palworld',
    roomLabel: '幻兽帕鲁', proto: 'udp',

    // 纯 UDP ⇒ 探 TCP 是白等，只查进程 ✓
    probe: ['proc'],
    procMatch: ['palworld'],

    help: {
      host: [
        '先把世界开起来：游戏里<b>创建世界</b>时勾上多人（或起专用服务器），端口保持默认 <b>8211</b>',
        '回到本软件 →「创建房间」→ 端口填 <b>8211</b> → 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里点<b>加入多人游戏</b> → 在 IP 框里填 <b>127.0.0.1:8211</b>',
        '📌 <b>要带端口</b>，只填 127.0.0.1 它不会默认 8211'
      ],
      tips: [
        ['只有 PC 能这么连',
         '《幻兽帕鲁》的<b>加入多人游戏 → 输入 IP</b>只有 PC 版有。<br>Xbox / PS5 只能通过社区服务器列表找，用不了这个办法。'],
        ['只有一个端口',
         '主端口是 <b>UDP 8211</b>，一路就够。<br>不需要像饥荒那样再开一个洞穴端口。'],
        ['版本要一致',
         '两边游戏版本要一样；开了模组的话，房主装了哪些，朋友也得装。']
      ]
    }
  },

  {
    // 铁锈战争（Rusted Warfare）
    //
    // 🩸 它跟饥荒那种"两个端口"完全不是一回事，别搞混：
    //   · 端口**只有一个 5123**，但可以走 **TCP 或 UDP 两种协议**（同端口）
    //   · 默认 TCP；游戏设置里勾了「使用 UDP 协议降低延迟（实验性）」才走 UDP
    //   ⇒ 所以这里给 `proto: 'both'`：TCP 那路一直开，UDP 那路多开一份
    //     ⇒ 房主勾不勾 UDP 都能连，**不用让玩家知道这个选项** ✓
    //   📌 这就是"同端口两种协议"，跟 DualPort（第二路不同端口）是两件事 ✗
    name: '铁锈战争', sub: 'Rusted Warfare', port: 5123, cover: 'cover-rusted',
    roomLabel: '铁锈战争', proto: 'both',

    // 有 TCP，所以先探 TCP，探不到再看进程（老板原则：能 TCP 就 TCP）
    probe: ['tcp', 'proc'],
    procMatch: ['rusted'],

    help: {
      host: [
        '游戏里点<b>多人游戏</b> → <b>创建房间</b>（端口就用默认的 <b>5123</b>）',
        '回到本软件 →「创建房间」→ 端口填 <b>5123</b> → 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里点<b>多人游戏</b> → <b>Direct Join</b>（直接加入）→ 地址填 <b>127.0.0.1:5123</b>'
      ],
      tips: [
        ['别用中继节点',
         '游戏里除了 Direct Join 还有 <b>Relay</b>（中继节点，比如 asia1.relay）。<br>那个数据要绕一圈中继服务器，延迟更高 —— 我们这条是**直连**，用 Direct Join 更好。'],
        ['关于 UDP 那个实验选项',
         '游戏设置里有个「使用 UDP 协议降低网络延迟（实验性）」。<br>勾不勾都行：本软件<b>TCP 和 UDP 都开着</b>，勾了就走 UDP，没勾走 TCP。'],
        ['端口可以改',
         '默认 5123，能在「设置 → 联网 → Port Number」里改。<br>改了的话，本软件里也要填改后的那个号（两边必须一样）。']
      ]
    }
  },

  {
    // 方舟：生存进化（ARK: Survival Evolved）
    //
    // 🩸 **第二个需要两路的游戏**（第一个是饥荒），但原因不一样：
    //   · 饥荒：主世界 + 洞穴 = **两个不同的游戏端口**（10999 / 10998）
    //   · 方舟：主端口 + Raw UDP Socket = 7777 / **7778**（就 7777+1）
    //   ⇒ 都用 DualPort，走同一条隧道的两路 pipe ✓
    //
    // 端口清单（老板给的资料，按它取舍）：
    //   UDP 7777  = 游戏通信主端口           ⇒ 打 ✓
    //   UDP 7778  = Raw UDP Socket（7777+1） ⇒ 一起打 ✓（extraPort）
    //   UDP 27015 = Steam 查询端口           ⇒ **不打**（IP 直连不需要）
    //   TCP 27020 = RCON 远程管理            ⇒ **绝对不放行** ✗
    name: '方舟：生存进化', sub: 'ARK: Survival Evolved', port: 7777,
    cover: 'cover-ark', roomLabel: '方舟：生存进化', proto: 'udp',
    extraPort: 7778,

    // 纯 UDP ⇒ 探 TCP 白等，只查进程
    probe: ['proc'],
    // 进程是 ShooterGame.exe。写 `ark` 太宽泛（一堆程序带 ark），写全名更准 ✓
    procMatch: ['shootergame'],

    help: {
      host: [
        '把服务器开起来：<b>专用服务器</b>（Dedicated）或游戏里的<b>非专用主机</b>，端口保持默认 <b>7777</b>',
        '回到本软件 →「创建房间」→ 端口填 <b>7777</b> → 把房间 ID 发给朋友',
        '📌 本软件已经<b>同时打通了 7778</b>（Raw UDP），你不用再管它'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里选 <b>IP 直连</b> → 地址填 <b>127.0.0.1:7777</b>',
        '📌 <b>要带端口</b>；如果本机 7777 被占、顺延了，按界面显示的那个号填'
      ],
      tips: [
        ['刷不出房间就用 IP 直连',
         '方舟的<b>非专用主机</b>靠局域网广播找房间，跨网络时经常失效 —— 所以列表里刷不出来是正常的。<br>用 <b>IP 直连</b> 一定连得上。'],
        ['两个端口都打通了',
         '方舟除了主端口 7777，还有个 <b>7778</b>（Raw UDP Socket）。<br>本软件两路都转发了，不用你自己再开。'],
        ['Steam 列表 / RCON 都不碰',
         'UDP 27015 只是让服务器出现在 Steam 列表里，IP 直连不需要。<br>TCP 27020 是服主远程管理用的，跟联机无关，本软件<b>不会放行</b>。']
      ]
    }
  },

  {
    // 方舟：生存飞升（ARK: Survival Ascended，简称 ASA）
    // 是《生存进化》的虚幻5重制版 —— 端口差不多，但**发现机制换了**：
    //
    // 🩸 最大的变化：Steam 服务器浏览器 → **EOS（Epic Online Services）**
    //   ⇒ 服务器要在 EOS 后端注册，玩家才在游戏内列表里看得到
    //   ⇒ 跨网络时那个列表**更靠不住** ⇒ **IP 直连是唯一稳的路** ✓
    //   📌 这也是我们唯一该引导的方式，别让玩家去列表里找 ✓
    //
    // 端口：
    //   UDP 7777  = 游戏通信主端口           ⇒ 打 ✓
    //   UDP 7778  = Peer Port                ⇒ 保守起见也打（资料说"可能并非必需"）
    //   UDP 27015 = Steam 查询               ⇒ 飞升可能已弃用 ⇒ **不打**
    //   TCP 27020 = RCON 远程管理            ⇒ **绝对不放行** ✗
    name: '方舟：生存飞升', sub: 'ARK: Survival Ascended', port: 7777,
    cover: 'cover-asa', roomLabel: '方舟：生存飞升', proto: 'udp',
    extraPort: 7778,

    // 纯 UDP ⇒ 只查进程
    probe: ['proc'],
    // 🩸 ArkAscended.exe —— **跟前作完全不同**（前作是 ShooterGame.exe）
    //    两个关键词互不包含，不会混 ✓
    procMatch: ['arkascended'],

    help: {
      host: [
        '把服务器开起来（<b>专用服务器</b>或游戏内开世界），端口保持默认 <b>7777</b>',
        '回到本软件 →「创建房间」→ 端口填 <b>7777</b> → 把房间 ID 发给朋友',
        '📌 本软件已经把 <b>7778</b> 也一起打通了，你不用管'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里选 <b>IP 直连</b> → 地址填 <b>127.0.0.1:7777</b>',
        '📌 <b>别去服务器列表里找</b> —— 飞升用的是 EOS 发现，跨网络基本搜不到'
      ],
      tips: [
        ['为什么不让你用服务器列表',
         '《生存飞升》把发现机制换成了 <b>EOS</b>（Epic Online Services）：服务器要先在 EOS 注册，才会出现在游戏内的列表里。<br>跨网络时这个列表基本靠不住 ⇒ <b>IP 直连是唯一稳的</b>。'],
        ['只有 PC 能 IP 直连',
         'Xbox / PS5 玩家只能靠游戏内的服务器浏览器，用不了 IP 直连。<br>所以本软件目前主要服务 PC 玩家。'],
        ['跟前作是两个图标',
         '《生存进化》(Evolved) 和《生存飞升》(Ascended) 是两款游戏，端口差不多但别选错。<br>进化的程序是 ShooterGame.exe，飞升是 ArkAscended.exe。']
      ]
    }
  },

  {
    // 像素工厂（Mindustry）
    //
    // 🩸 它跟铁锈战争是同一个模式：**一个端口 6567，但 TCP 和 UDP 都要**
    //   TCP 6567 = 可靠传输（聊天、建筑操作，不能丢）
    //   UDP 6567 = 实时同步（玩家位置、单位动作，允许少量丢包）
    //   ⇒ `proto: 'both'` 正好，**不需要 DualPort** ✓
    //
    // ⚠️ 它的局域网发现走**多播**（227.2.4.7:20151）：
    //   隧道转不了多播包 ⇒ 跨网络的朋友**绝对搜不到**房间 ✗
    //   ⇒ 只能引导 IP 直连（游戏里「加入游戏」→「添加服务器」）✓
    name: '像素工厂', sub: 'Mindustry', port: 6567, cover: 'cover-mind',
    roomLabel: '像素工厂', proto: 'both',

    // 有 TCP 可探，先 TCP 再进程
    probe: ['tcp', 'proc'],
    procMatch: ['mindustry'],

    help: {
      host: [
        '游戏里点<b>自定义游戏</b>开一局，进游戏后按 <b>Esc</b> → <b>主机</b> 打开（端口默认 <b>6567</b>）',
        '回到本软件 →「创建房间」→ 端口填 <b>6567</b> → 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里点<b>加入游戏</b> → <b>添加服务器</b> → 地址填 <b>127.0.0.1:6567</b>',
        '📌 <b>别在服务器列表里找</b> —— 那个列表靠局域网多播，跨网络根本收不到'
      ],
      tips: [
        ['为什么列表里搜不到',
         '《像素工厂》的局域网发现走的是<b>多播</b>（227.2.4.7:20151）。<br>多播包只能在同一个物理局域网里传播，隧道转不了它 ⇒ 跨网络一定搜不到 ✗<br>所以用<b>添加服务器</b>手动填地址 ✓'],
        ['TCP 和 UDP 都要',
         '它同一个端口上跑两种协议：TCP 管聊天和建造，UDP 管实时同步。<br>本软件<b>两种都开着</b>，你不用管。'],
        ['版本要一致',
         '两边游戏版本要一样（比如都是 release build 143）；装了模组的话两边也得一致。']
      ]
    }
  },

  {
    // 异星工厂（Factorio）
    //
    // 🩸 **纯 UDP 单端口 34197**，没有 TCP 那一半：
    //   它自己在 UDP 之上做了可靠层（处理丢包和乱序）
    //   ⇒ `proto: 'udp'`，**不是 both** —— 别跟铁锈/像素工厂那种"两种协议"混 ✗
    //
    // 联机：游戏里「连接至地址」直接填 IP:端口，不依赖大厅 ✓
    //   大厅/局域网列表靠发现机制，跨网络靠不住 ⇒ 不作为引导重点 ✓
    name: '异星工厂', sub: 'Factorio', port: 34197, cover: 'cover-factorio',
    roomLabel: '异星工厂', proto: 'udp',

    // 纯 UDP ⇒ 只查进程
    probe: ['proc'],
    procMatch: ['factorio'],

    help: {
      host: [
        '游戏里点<b>多人游戏</b> → <b>新建游戏</b>（端口默认 <b>34197</b>，可在配置文件里改）',
        '回到本软件 →「创建房间」→ 端口填 <b>34197</b> → 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里点<b>多人游戏</b> → <b>连接至地址</b> → 填 <b>127.0.0.1:34197</b>'
      ],
      tips: [
        ['用「连接至地址」，别等大厅',
         '大厅和局域网列表靠游戏的发现机制，跨网络时基本收不到。<br>直接点 <b>连接至地址</b> 填地址，一定能连上 ✓'],
        ['只有 UDP',
         '异星工厂不用 TCP —— 它自己在 UDP 上做了可靠传输。<br>本软件只转 UDP 那一路就够，不用管别的。'],
        ['版本和 Mod 必须完全一致',
         '这是硬要求：游戏版本不同、装的 Mod 不同，都连不上。<br>房主装了什么，朋友也得装一模一样的。']
      ]
    }
  },

  {
    // 七日杀（7 Days to Die）
    //
    // 🩸 主端口 26900 是**双协议**（跟上一条的铁锈/像素工厂同型）：
    //   TCP 26900 = 查询 / 初始连接握手
    //   UDP 26900 = 实际玩家数据传输
    //   ⇒ `proto: 'both'` 管它 ✓
    //
    // 🩸 另外还有辅助端口 **26901-26903**（UDP，Steam 通信 / 服务器可见性）
    //   我们**只能再开一路** ⇒ 给 26901（DualPort）
    //   26902 / 26903 这版没转 —— 它们不是直连必经之路，缺了不影响连进去 ✓
    //
    // ⚠️ 8080-8082（Web 管理面板 / Telnet）**绝不能碰** ✓ 本软件不传它们
    name: '七日杀', sub: '7 Days to Die', port: 26900, cover: 'cover-7dtd',
    roomLabel: '七日杀', proto: 'both',
    extraPort: 26901,

    // 有 TCP 可探，先 TCP 再进程
    probe: ['tcp', 'proc'],
    // 进程是 7DaysToDie.exe ⇒ 两个关键词都写上（老版本/启动器写法可能不同）
    procMatch: ['7daystodie', '7dtd'],

    help: {
      host: [
        '游戏里点<b>新建游戏</b>时勾上多人（或起专用服务器），端口保持默认 <b>26900</b>',
        '回到本软件 →「创建房间」→ 端口填 <b>26900</b> → 把房间 ID 发给朋友',
        '📌 本软件已经把 <b>26901</b> 也一起转了（辅助端口），你不用管'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里点<b>加入游戏</b> → <b>连接到 IP</b> → 填 <b>127.0.0.1:26900</b>',
        '📌 <b>别等 LAN 列表</b> —— 那个靠局域网广播，跨网络收不到'
      ],
      tips: [
        ['用「连接到 IP」，别等 LAN 列表',
         '房主可以开「局域网可见」，但那个列表靠广播，跨网络的朋友根本收不到 ✗<br>直接点 <b>连接到 IP</b> 填地址，一定能连上 ✓'],
        ['两个协议 + 辅助端口',
         '26900 上 TCP 管查询和握手、UDP 管游戏数据，本软件<b>两种都开着</b>。<br>另外 26901 是辅助端口，也已经一起转了。<br>26902/26903 这版没转，不影响玩家连进去。'],
        ['管理端口我们不碰',
         '8080-8082 是服务器的 Web 管理面板和 Telnet，跟联机无关。<br>本软件<b>不会转发它们</b> —— 那些别往外暴露。']
      ]
    }
  },

  {
    // 夜族崛起（V Rising）
    //
    // 🩸 它的问题不是"几个端口"，而是**端口有新有旧两套** —— 这是最容易配错的：
    //   新版（ServerHostSettings.json 里的 Port / QueryPort）：
    //     27015 = 游戏主端口，27016 = 查询端口
    //   旧版（部分教程还在用）：
    //     9876 / 9877
    //   ⇒ 默认给新版；用户要是照着旧教程配的，就按自己的填 ✓ 帮助里写清了
    //
    // 🩸 RCON（25575 新 / 9878 旧）**绝不碰** ✓ 那是服主的远程管理口
    name: '夜族崛起', sub: 'V Rising', port: 27015, cover: 'cover-vrising',
    roomLabel: '夜族崛起', proto: 'udp',
    extraPort: 27016,

    // 纯 UDP ⇒ 只查进程
    probe: ['proc'],
    procMatch: ['vrising'],

    help: {
      host: [
        '把服务器开起来（游戏内「开新服务器」或专用服务器），端口保持默认 <b>27015</b>',
        '回到本软件 →「创建房间」→ 端口填 <b>27015</b> → 把房间 ID 发给朋友',
        '📌 本软件已经把查询端口 <b>27016</b> 也一起转了'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏里 <b>线上游戏</b> → <b>显示所有服务器</b> → <b>直接连接</b> → 填 <b>127.0.0.1:27015</b>',
        '📌 如果连不上，试试勾上 <b>LAN Server</b>（局域网服务器）那个选项'
      ],
      tips: [
        ['端口有新旧两套，别配错',
         '新版是 <b>27015</b>（游戏）+ <b>27016</b>（查询）；<br>旧版是 <b>9876</b> + <b>9877</b>，有些老教程还在用。<br>按你自己服务器配置里写的那个填，两边必须一致。'],
        ['连不上就先勾 LAN Server',
         '有些情况下「直接连接」需要勾上 <b>LAN Server</b>（局域网服务器）才认这个地址。<br>先试不勾，不行就勾上再试一次。'],
        ['管理端口我们不碰',
         'RCON（新版 25575 / 旧版 9878）是服主远程管理用的，跟联机无关。<br>本软件<b>不会转发它</b>，也别往外暴露。']
      ]
    }
  },

  {
    // 饥荒联机版（Don't Starve Together）
    //
    // 🩸 三条跟别的游戏不一样，都是老板给的信息核实过的：
    //   ① 端口**固定** UDP 10999（主世界）⇒ 朋友端从 10999 起找空闲，
    //      正常情况就是这个数 ⇒ c_connect("127.0.0.1", 10999) 直接能用 ✓
    //   ② **只听 UDP** —— 只开 TCP 的话服务端能起来，但客户端一直连不上 ✗
    //   ③ 靠**局域网广播**找房间，广播出不了跨网 ⇒ 列表里常常刷不出来
    //      ⇒ 控制台 `c_connect` 是可靠兜底，帮助里直接写出来 ✓
    name: '饥荒联机版', sub: "Don't Starve Together", port: 10999, cover: 'cover-dst',
    roomLabel: '饥荒联机版', proto: 'udp',

    // 洞穴那个端口（主世界 10999 的邻居 10998）。
    // 📌 有了它，"下洞穴会掉线"才治得住 —— 隧道那边还要另做一路（见 TODO）✓
    extraPort: 10998,

    // 🩸 探测只给 'proc'：它是纯 UDP，TCP 端口根本连不上 ⇒ 探 TCP 是白等 ✗
    // （星露谷那种 `['tcp','proc']` 在这儿不适用）
    probe: ['proc'],
    procMatch: ['dontstarve'],

    help: {
      host: [
        '在游戏里开世界：新建世界时勾上<b>局域网</b>（默认就开着），进游戏后世界就在跑了',
        '回到本软件 →「创建房间」→ 端口填 <b>10999</b> → 把房间 ID 发给朋友'
      ],
      guest: [
        '拿房主的房间 ID →「加入房间」→ 等状态变成「隧道就绪」',
        '游戏主菜单点<b>浏览游戏</b>，LAN 标签页里应该能看到房间',
        '看不见就按 <b>~</b> 开控制台，输入 <b>c_connect("127.0.0.1", 10999)</b> 直接连'
      ],
      tips: [
        ['为什么刷不出房间？',
         '《饥荒》靠<b>局域网广播</b>找房间，广播发不出跨网 —— 所以跨网联机时列表里刷不出来是正常的。<br>用控制台那条 <b>c_connect</b> 指令一定连得上。'],
        ['端口是固定的',
         '主世界默认 <b>UDP 10999</b>。开了洞穴的话游戏会另用一个端口（10998），这一版<b>只转发主世界</b>。'],
        ['版本要一致',
         '两边都要有同一个游戏版本；装了 Mod 的话，房主装了哪些，朋友也得装。']
      ]
    }
  }
];

// 还没写过专属文案的游戏走这套。**只讲通用套路**，不冒充任何具体游戏 ✓
const GAME_FALLBACK = {
  intro: '这个游戏的专属说明还在补。<b>不过套路是一样的</b>，照下面两步来就行 —— ' +
         '菜单叫法每个游戏不同，翻一下游戏里的「多人游戏」菜单就能对上。',
  host: [
    '在游戏里建一个「允许别人加入」的房间，<b>记住它用的端口</b>',
    '「创建房间」→ 端口填<b>刚才游戏里那个</b> → 把房间 ID 发给朋友'
  ],
  guest: [
    '「加入房间」→ 等状态变成「隧道就绪」',
    '在游戏里连<b>界面上给的地址和端口</b>'
  ],
  tips: [
    ['连不上先看哪儿？',
     '先核对<b>端口</b>：游戏里开的端口，和本软件里填的，必须是同一个。']
  ],
  notice: {
    port:    '端口要跟游戏里开房时用的那个<strong>保持一致</strong>，不然连不上。',
    version: '<strong>版本要一致</strong>，装了什么额外内容也要一样，差一点都进不去。',
    fail:    '连不上先核对<strong>端口</strong>填对没 —— 要和游戏里开房时用的那个一致。'
  }
};

// 取某个游戏的"文案包"：它自己有就用，没有就走兜底 ✓
function gamePack(name) {
  const g = GAMES.find(x => x.name === name);
  const has = !!(g && g.help);
  return {
    intro: has ? '' : GAME_FALLBACK.intro,
    host:  (has && g.help.host)  || GAME_FALLBACK.host,
    guest: (has && g.help.guest) || GAME_FALLBACK.guest,
    tips:  (has && g.help.tips)  || GAME_FALLBACK.tips,
    notice: (g && g.notice) || GAME_FALLBACK.notice
  };
}

let curPage = 'games';
let curGameName = '';
// 帮助页**自己**挑的游戏。跟 curGameName 分开管 ——
// 帮助页不该被"当前在玩哪个"锁死：还没选游戏的人也能看，
// 或者想先看看别的游戏怎么弄再决定 ✓
let helpGame = '';
let curMaxSize = 4;
let curJoinCode = '';   // 玩家填的房间 ID
let roomIsHost = false; // 当前房间我是房主吗（决定按钮是「关闭」还是「退出」）
// 会话是否开着（进房间页置 true，会话结束置 false）。
// 教程页的「返回」靠它决定回房间页还是回创建/加入页 —— 见下面 .back 的绑定。
let inRoom = false;
// 联机流程里最后待的页。点侧边栏「联机」时回到这儿 ——
// 房间开着的时候点去设置/帮助再点回来，不能把人踢回"选择游戏"。
let lastLinkPage = 'games';

/* ---------- 面包屑 ---------- */
// 当前选的游戏在列表里的那条。没选或名字对不上就返回 null。
function curGame() {
  return GAMES.find(g => g.name === curGameName) || null;
}

// 界面各处要显示"在玩哪个游戏"时都走这儿 —— 别再各写各的 ✗
function gameLabel() {
  const g = curGame();
  return (g && g.roomLabel) || curGameName || '未选游戏';
}

// 当前游戏的默认端口。给后端用 —— 玩家加入房间时，本机隧道入口
// **从这个端口开始**找空闲的 ✓（这样泰拉瑞亚显示的就是 127.0.0.1:7777 ✓）
//
// 🩸 为什么得由页面来告诉后端：**Go 那边看不到当前选了哪个游戏**
// （curGameName 是前端的状态）✓ 跟 RememberGamePort 是同一个原因
function gamePortHint() {
  const g = curGame();
  return String((g && g.port) || 25565);
}

// 这个游戏要不要开 UDP 那一路 ✓
//
// 📌 只有 'udp' / 'both' 才开。我的世界 Java 版只走 TCP ⇒ 不开 ✓
// 📌 加游戏时把 proto 写对就行 —— 界面和后端都不用动 ✓
//
// ⚠️ 现在 'udp' 和 'both' 的**实际行为是一样的**：TCP 那一路是一直开着的
// （游戏连过来之前，我们也不知道它会用哪个），proto 只控制"要不要**多**开 UDP"。
// 留三档是为了语义清楚 —— 真要做"纯 UDP、不开 TCP"那天才有区别 ✓
function gameNeedsUDP(name) {
  const g = GAMES.find(x => x.name === name);
  return !!g && (g.proto === 'udp' || g.proto === 'both');
}

// 当前游戏的协议。给后端用 —— 一个参数顶两件事：
//   ① 要不要**多**开一路 UDP（"udp" / "both" 才开）
//   ② 那个"游戏服务端开没开"的状态灯**探不探得到**
//      （探测手段是连 TCP ⇒ 纯 UDP 游戏永远探不到 ⇒ 别探，如实说"测不了"）
//
// 🩸 还是那个老原因：**Go 看不到当前选了哪个游戏**，只能由页面告诉它 ✓
function gameProto() {
  const g = curGame();
  return (g && g.proto) || 'tcp';
}

// 这个游戏**用什么方式探**"开没开"。
//
// 返回一个列表，按顺序试、任一成功就算开着 ✓
//
//	['tcp']          连一下 TCP 端口（**默认**——能 TCP 就 TCP ✓）
//	['tcp', 'proc']  先 TCP，不通再查本机进程（星露谷这种 ✓）
//	[]               什么都不试 ⇒ 界面如实说"测不了"
//
// 🩸 **跟 proto 分开**：星露谷就是活例子 —— 转发要 both（保险），
// 但它实际只听 UDP ⇒ 探测得另配 ✗
// 拿 proto 推的话 `both !== 'udp'` ⇒ 判定可探 ⇒ 白忙一场（2026-10-15 栽过 ✓）
//
// 📌 老板定的原则：**能 TCP 就 TCP，进程是没办法了才看** ✓
// 所以默认只有 'tcp'，要加 'proc' 得明确写出来 ✓
function probeModesOf(name) {
  const g = GAMES.find(x => x.name === name);
  if (!g || !g.probe) return ['tcp']; // 没写 = 默认走 TCP ✓
  return Array.isArray(g.probe) ? g.probe : [g.probe]; // 容错：写成单值也认
}

// 查进程时用哪些关键词（**任意一个命中**就算 ✓）
function procMatchOf(name) {
  const g = GAMES.find(x => x.name === name);
  if (!g || !g.procMatch) return [];
  return Array.isArray(g.procMatch) ? g.procMatch : [g.procMatch];
}

// 这个游戏**能不能给出**"开没开"的判断 ✓（一种方式都没配 ⇒ 测不了）
function gameProbeable(name) {
  return probeModesOf(name).length > 0;
}

// 给用户看的"连接地址"要不要带端口号 ✓
//
// 🩸 有些游戏**只要 IP**：星露谷的端口是**内置**的（写死 24642），
// 填地址时带上 `:24642` 游戏那边会当成无效地址 ✗
// （官方说明里明确标了 ❌：Do NOT include port number）
//
// ⚠️ **只影响显示和复制**，内部（隧道监听、探测）永远用带端口那份 ——
// 内部少一个端口号就连不上了 ✗ 所以转换只发生在"给用户看"这一步 ✓
function gameAddrHasPort(name) {
  const g = GAMES.find(x => x.name === name);
  return !g || g.addrStyle !== 'host-only';
}

// 把一个 "主机:端口" 转成"该给用户看的样子" ✓
function addrForGame(addr, name) {
  if (!addr || gameAddrHasPort(name)) return addr;
  const i = addr.lastIndexOf(':');
  return i > 0 ? addr.slice(0, i) : addr;
}

/* ---------- 把"按游戏变"的内容渲染出来 ----------

   🔑 **所有按游戏变的地方，都只在这儿渲染一次。**
      加游戏 = 往 GAMES 加一条数据 —— HTML 不用动，这里也不用动 ✓

   （以前是 HTML 里一个游戏抄一份、靠 data-game 显示/隐藏 ⇒
     每加一个游戏都可能漏掉某处，2026-10-15 连漏四次 ✗）
   -------------------------------------------------------- */

// 一张步骤卡（房主 / 朋友各一张）
function stepCard(n, title, lines, icon) {
  return '<div class="step"><div class="stepnum">' + n + '</div>' +
    '<div class="stepbody"><div class="steptitle">' + title + '</div>' +
      '<div class="infobox"><span class="ic">' + icon + '</span>' +
      '<div class="tx">' + lines.join('<br>') + '</div></div>' +
    '</div></div>';
}

// 一条常见问题
function faqCard(q, a) {
  return '<div class="faq"><div class="q"><span class="qi">?</span>' + q + '</div>' +
         '<div class="a">' + a + '</div></div>';
}

// 帮助页「第一次联机」整块。
// 用的是**帮助页自己挑的那个游戏**（顶上那个下拉），不是"当前在玩的" ——
// 还没选游戏的人也得能看 ✓
function renderHelpFirst() {
  const box = $('helpFirstGame');
  if (!box) return;
  const p = gamePack(helpGame || GAMES[0].name);
  let h = '';
  if (p.intro) {   // 走兜底时，先说一句"这个游戏的说明还在补"
    h += '<div class="infobox"><span class="ic">' + ICON_INFO + '</span>' +
         '<div class="tx">' + p.intro + '</div></div>';
  }
  h += stepCard(1, '房主怎么做', p.host, ICON_HOST);
  h += stepCard(2, '朋友怎么做', p.guest, ICON_GUEST);
  h += '<h3>几个容易忘的</h3>';
  COMMON_TIPS.concat(p.tips).forEach(t => { h += faqCard(t[0], t[1]); });
  box.innerHTML = h;
}

// 房主页「注意事项」里按游戏变的那三条。跟着**当前在玩的游戏** ✓
function renderRoomNotices() {
  const p = gamePack(curGameName);
  if ($('noticePort'))    $('noticePort').innerHTML    = p.notice.port;
  if ($('noticeVersion')) $('noticeVersion').innerHTML = p.notice.version;
  if ($('noticeFail'))    $('noticeFail').innerHTML    = p.notice.fail;
}

// 两处一起刷。切页、换游戏、换帮助页挑的游戏 —— 都走这一个入口 ✓
function renderGameTexts() {
  renderHelpFirst();
  renderRoomNotices();
}

function setCrumb() {
  const c = $('crumb');
  const g = curGameName || '未选游戏';
  if (curPage === 'games')       c.innerHTML = '<b>联机</b> / 选择游戏';
  else if (curPage === 'mode')   c.innerHTML = '<b>联机</b> / ' + g + ' / 选择方式';
  else if (curPage === 'create') c.innerHTML = '<b>联机</b> / ' + g + ' / 创建房间';
  else if (curPage === 'join')     c.innerHTML = '<b>联机</b> / ' + g + ' / 加入房间';
  else if (curPage === 'room')     c.innerHTML = '<b>联机</b> / ' + g + ' / 房间';
  else if (curPage === 'tutorial') c.innerHTML = '<b>联机</b> / ' + g + ' / 开房教程';
  else if (curPage === 'tutorial-join') c.innerHTML = '<b>联机</b> / ' + g + ' / 加入房间教程';
  else if (curPage === 'profile')   c.innerHTML = '<b>个人信息</b>';
  else if (curPage === 'help')     c.innerHTML = '<b>帮助</b>';
  else if (curPage === 'settings') c.innerHTML = '<b>设置</b>';
  // 🩸 新加的页面必须在这儿登记一句，否则面包屑会**停在上一个页面上** ——
  //    人看着像"没点动"（2026-10-18 加网络自检页时补的）✗
  else if (curPage === 'netcheck') c.innerHTML = '<b>网络自检</b>';
  else if (curPage === 'about')  c.innerHTML = '<b>关于</b>';
}

function show(page) {
  document.querySelectorAll('.page').forEach(p => p.classList.toggle('active', p.id === 'page-' + page));
  curPage = page;
  if (['games', 'mode', 'create', 'join', 'room'].includes(page)) lastLinkPage = page;
  document.querySelectorAll('.nav-item').forEach(n => {
    const nav = n.dataset.nav;
    n.classList.toggle('active',
      (nav === 'games' && ['games', 'mode', 'create', 'join', 'room'].includes(page)) || (nav === page));
  });
  setCrumb();
  if (page === 'settings') refreshSettings();
  // 进个人信息页时用已保存的值刷一遍 —— 上次没保存的改动就丢掉，别让它看起来像存上了
  if (page === 'profile') applyMe();
  // 🩸 房间页顶上那个游戏名原来是**写死的**「我的世界 Java 版」✗
  // 于是选了泰拉瑞亚进房间，标题还是我的世界 —— 现在跟着实际选的游戏走 ✓
  // 封面同理：原来是写死的一整块我的世界封面 ✗
  if (page === 'room') {
    $('roomGame').textContent = gameLabel();
    const g = curGame();
    // MC 在 GAMES 里 cover 是空的（它本来就是默认那张）⇒ 兜底成 cover-mc ✓
    $('roomCoverBox').className = 'roomcover ' + ((g && g.cover) || 'cover-mc');
  }
  // 进帮助页：第一次默认就是当前在玩的那个游戏（贴心一点），
  // 之后保留他自己在下拉里挑的 —— 别每进一次就给他重置回去 ✗
  if (page === 'help') {
    if (!helpGame) helpGame = curGameName || GAMES[0].name;
    if ($('helpGameSel')) $('helpGameSel').value = helpGame;
  }
  // 每切一次页就把"按游戏变"的内容重新渲染一遍 ——
  // 这是**唯一**的入口，不再有第二个地方要改 ✓
  renderGameTexts();
}

// 侧边栏「联机」回的是「联机流程里最后待的那页」，不是死板的"选择游戏"
document.querySelectorAll('.nav-item').forEach(n => {
  n.onclick = () => show(n.dataset.nav === 'games' ? lastLinkPage : n.dataset.nav);
});

// （原来这里直接 show(nav)，现在统一在上面那个 forEach 里 ——
//  点「联机」要回"最后待的那页"，不能死板地回选择游戏）
// 「返回」按钮回哪儿 —— 多数是写死的（选择方式页回游戏列表之类），
// 但教程页要**回你进来的那一页**：
// 🩸 原来它写死 data-back="create"（加入教程是 "join"），结果从房间页点
// 「不会？点我查看」进来后，返回会跳到创建房间页 —— 房间明明还开着。
document.querySelectorAll('.back').forEach(b => {
  b.onclick = () => {
    const to = b.dataset.back;
    if (inRoom && (to === 'create' || to === 'join')) return show('room');
    show(to);
  };
});

/* ---------- 游戏列表 ---------- */
GAMES.forEach(g => {
  const b = document.createElement('button');
  b.className = 'tile' + (g.cover ? ' ' + g.cover : '');
  // 一条一行：左小缩略图 + 中间名字 + 右边「选择」。
  // 端口号不显示在卡片上 —— 点进去会自动填好，摆在这儿只是噪音。
  b.innerHTML = '<span class="thumb"></span>' +
    '<span class="tiletext"><span class="t1">' + g.name + '</span>' +
    '<div class="t2">' + g.sub + '</div></span>' +
    '<span class="pick">选择</span>';
  b.onclick = () => {
    // 端口怎么填 —— 一句话：**它属于游戏，不属于上次** ✓
    //
    // 🩸 原来这里优先用「记住上次用过的端口」，于是选了泰拉瑞亚、框里却是
    //   上次我的世界那边的 25566 —— 看着就像没生效 ✗
    // 现在：换了游戏就用**这款游戏自己的**默认端口；
    //       没换（点回同一个）就保留框里现在的值，他手改的不会被冲掉 ✓
    const gameChanged = curGameName !== g.name;
    curGameName = g.name;
    $('modeGameName').textContent = g.name + ' · ' + g.sub;
    if (gameChanged || !$('port').value) {
      // 用**这个游戏自己**上次记过的端口（只在开着「记住」时）；
      // 没记过就用它的默认端口 ✓
      // 🩸 记的是每个游戏各一份：给泰拉瑞亚改成 7777，下次选泰拉瑞亚还是 7777；
      // 绝不会把上一款游戏的数字带过来 ✗（老板截图里那个 25566 就是这个问题）
      const saved = (appSettings && appSettings.rememberPort && appSettings.portByGame)
        ? appSettings.portByGame[g.name] : '';
      $('port').value = saved || g.port;
    }
    syncExtraPortField();
    show('mode');
  };
  $('gameGrid').appendChild(b);
});

$('pickCreate').onclick = () => show('create');
$('pickJoin').onclick   = () => show('join');
$('goTutorial').onclick     = () => show('tutorial');
$('goTutorialJoin').onclick = () => show('tutorial-join');


/* ---------- 建房页的「额外端口」那一行 ----------
 *
 * 🩸 为什么要让用户看见：有些游戏**占两个端口**（饥荒洞穴 / 方舟 7778 /
 *    七日杀 26901 / 夜族 27016）。这个号码以前是写死在 GAMES 里的，
 *    用户既看不到也改不了 ⇒ 他要是把主端口改了，第二路还是老号 ✗
 *
 * 📌 只有 `extraPort` 存在的游戏才显示这一行；单端口游戏一直 hidden，
 *    不占位、不增加噪音 ✓ */
function syncExtraPortField() {
  const field = $('extraPortField');
  const box = $('extraPort');
  if (!field || !box) return;

  const g = curGame();
  const extra = g && g.extraPort;
  if (!extra) {
    field.hidden = true;
    box.value = '';
    delete field.dataset.game;
    return;
  }
  field.hidden = false;
  // 🩸 换游戏时必须重置：不然从饥荒（10998）切到方舟，框里还留着 10998 ✗
  //    同一个游戏内则保留用户手改的值（别每点一次就刷掉）✓
  if (field.dataset.game !== g.name) {
    box.value = String(extra);
    field.dataset.game = g.name;
  }
}
