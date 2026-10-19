# TI 联机

免费的点对点联机工具：让两台电脑像在同一个局域网里一样玩游戏，**游戏数据不经过任何服务器**。

官网与下载：<https://ti.tinyaii.cn/>

Windows 10 / 11。

---

## 它是怎么工作的

常见的"内网穿透"是**中转**：数据要绕一圈经过它的节点，所以免费档总限速限量。

这个工具只让服务器做一件事：**告诉你对方在哪**（交换几十字节的地址）。
之后两台机器直接打通，游戏数据走你们俩之间 —— 服务器既看不到，也存不下。

```
房主 ──────┐                    ┌────── 朋友
           ├── 信令服务器 ──────┤        （只交换地址，不碰游戏数据）
房主 ──────┘                    └────── 朋友
      └───────── 游戏数据直连 ─────────┘
```

## 支持的游戏

已经配好端口和协议的游戏：

我的世界 Java 版 · 泰拉瑞亚 · 星露谷物语 · 饥荒联机版 · 幻兽帕鲁 ·
方舟：生存进化 · 方舟：生存飞升 · 铁锈战争 · 像素工厂 · 异星工厂 ·
七日杀 · 夜族崛起 · 生存战争

**不在列表里也能用** —— 它只是开一条 TCP / UDP 通道，把游戏对外的端口填进去就行。

## 构建

需要 Go 1.25+。Windows 桌面客户端还要 CGO 和 WebView2。

```bash
# 桌面客户端 + 命令行客户端 + 安装包（安装包需要 Inno Setup 6）
bash scripts/build-client.sh

# 只编命令行客户端
GOOS=windows go build -o node.exe ./cmd/node

# 信令服务器（Linux）
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o signald ./cmd/signal
```

## 目录结构

| 目录 | 内容 |
|---|---|
| `cmd/gui` | Windows 桌面客户端（WebView2 界面） |
| `cmd/node` | 命令行客户端 |
| `cmd/signal` | 信令服务器 |
| `cmd/panel` | 管理面板（服务器管理员用） |
| `internal/node` | 加入方：打洞、建隧道、断线自动重连 |
| `internal/host` | 房主端：为每位朋友各开一条隧道 |
| `internal/punch` | UDP 打洞，以及"打不通时怎么跟用户解释" |
| `internal/tunnel` | 隧道本体（KCP + smux，TCP / UDP 分流） |
| `internal/signal` | 信令客户端 |
| `internal/proto` | 信令协议（一行一个 JSON） |
| `internal/netutil` | 候选地址收集、STUN、端口校验 |
| `internal/keyex` | 临时 ECDH 密钥协商 |
| `internal/auth` | 账号与登录令牌 |
| `internal/game` | 支持的游戏清单 |
| `site/` | 官网源文件 |
| `scripts/` | 构建、部署、打包 |

## 安全设计

- 隧道密钥用**临时 ECDH 协商**得出 —— 服务器只转发公钥，**算不出会话密钥**
- 房间密码只决定"放不放你进来"，**不参与加密**
- 每个连接都要校验登录令牌，令牌可单独停用、可强制下线

## 许可

[GPL-3.0](LICENSE)。

简单说：可以看、可以改、可以自己用，但**改了之后再分发必须也开源**。
