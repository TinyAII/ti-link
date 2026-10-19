#!/usr/bin/env bash
# 把信令服务器部署到任意一台机器上（新机 / 换机都用它）。
#
# 用法：bash scripts/deploy-signal.sh <user@host> <SSH口令> [远程端口]
#   bash scripts/deploy-signal.sh root@<你的服务器IP> '<口令>'
#
# ⚠️ **在项目根目录下跑**：下面的 LOCAL_BIN 是相对路径（dist/…），cd 进 scripts/ 就找不着了
#
# 环境变量：
#   P2P_TLS=1                 启用 TLS（要配 P2P_TLS_CERT / P2P_TLS_KEY，有默认值）
#   P2P_LOCAL_SOCK=/root/p2plink-data/local.sock  开本机端点（Unix socket，给管理面板用；不设就不开）
#   P2P_DATA=/root/p2plink-data   账号库目录（**部署之前必须先建好管理员**，
#                                 否则新版本起不来 —— 它在启动时就要读账号库）
#
# 先本地交叉编译：
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/signald-linux-amd64 ./cmd/signal
#
# 记忆里踩过的坑，这里都避开了：
#   🩸 scp 覆盖**正在运行的**二进制必然失败 → 先 stop 再传
#   🩸 pkill -f xxx 会连承载命令的远程 shell 一起杀 → 一律走 systemctl
#   🩸 传之前先把旧版备份成 .prev，出问题能滚回去
#
# 这台机器上可能还跑着别的东西（nginx / postgres 之类），脚本**只碰**：
#   · $REMOTE_BIN（默认 /root/signald-linux-amd64）
#   · /etc/systemd/system/p2plink-signal.service
# 其它一概不动。
set -euo pipefail

export PATH="/c/msys64/mingw64/bin:/usr/bin:/bin:/c/Windows/System32:$PATH"

if [ $# -lt 2 ]; then
  echo "用法: bash scripts/deploy-signal.sh <user@host> <SSH口令> [远程端口]"
  exit 1
fi

TARGET="$1"
PASS="$2"
PORT="${3:-39000}"
SERVICE=p2plink-signal
REMOTE_BIN="/root/signald-linux-amd64"
DATA_DIR="${P2P_DATA:-/root/p2plink-data}"
LOCAL_BIN=dist/signald-linux-amd64

# TLS：**默认就开**（生产必须开）。
#
# 🩸🩸 2026-10-17 事故：这里原来是 `P2P_TLS:-0`（默认关），
#    而部署脚本**每次重写 service 文件** ⇒ 正常部署就把证书参数弄丢 ⇒
#    服务在跑、39000 在听，但客户端发 TLS ClientHello（首字节 0x16）时
#    服务端按明文去解 ⇒ 直接关连接 ⇒ 客户端看到 **EOF**、开不了房 ✗
#    ⇒ 默认值必须等于"生产实际要的"：**开** ✓
#    本地想跑明文就显式 P2P_TLS=0 ✓
TLS_CERT="${P2P_TLS_CERT:-/etc/letsencrypt/live/mc.tinyaii.cn/fullchain.pem}"
TLS_KEY="${P2P_TLS_KEY:-/etc/letsencrypt/live/mc.tinyaii.cn/privkey.pem}"
if [ "${P2P_TLS:-1}" = "0" ]; then
  TLS_ARGS=""
else
  TLS_ARGS="-tls-cert $TLS_CERT -tls-key $TLS_KEY"
fi

# 本机端点（给管理面板用）：一个 **Unix socket**，不是 TCP 端口了。
#
# 🩸 2026-10-16 从"监听 127.0.0.1"换成 socket 的，起因是老板一句问：
#   回环地址挡住的是外网和别的机器，**挡不住同机上的其它进程** ✗
#   那台机器上还跑着 nginx / postgres / 博客，谁都能 curl 一下 ——
#   读接口无所谓，但"从后台关房间"是**能踢人**的写接口 ⇒ 权限得由操作系统判 ✓
#
# socket 文件权限设成 0600：只有 root（也就是这个服务 + 面板）连得上 ✓
# 🩸🩸 默认就要带！2026-10-17 事故：
#    这段原来是 `STATS_ARGS=""`，只有设了环境变量才带 `-local` ⇒
#    **正常部署时每次都把 socket 参数弄丢** ⇒ 服务照跑、39000 照听，
#    但面板连不上 socket ⇒ 「穿透服务器那边取不到」、开房报错 ✗
#    这种"服务是活的、只是少了一个参数"最难查 —— 所以默认值必须是对的 ✓
STATS_ARGS="-local ${P2P_LOCAL_SOCK:-$DATA_DIR/local.sock}"

# 🩸 指标落盘要**单独一个参数**（-local 只管那个 socket）——
# 忘了传它的话，服务照跑、socket 照好，只是**那个 CSV 文件永远不出现** ✗
# （2026-10-16 就漏了这一次，是下面那条"文件权限"检查当场抓到的 ✓）
# 🩸🩸 同 `-local` / `-tls` 的老毛病：默认空 ⇒ 正常部署就丢 ⇒ CSV 永远不出现
#    （这段旁边原来的注释就写着"2026-10-16 就漏了这一次"，**今天又漏了一次** ✗）
#    ⇒ 默认值必须等于生产值 ✓
METRICS_ARGS="-metrics ${P2P_METRICS:-/root/p2plink-metrics/metrics.csv}"

if [ ! -f "$LOCAL_BIN" ]; then
  echo "找不到 $LOCAL_BIN —— 先交叉编译：
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $LOCAL_BIN ./cmd/signal"
  exit 1
fi

# 🩸 上传前先验**它真的是 Linux 二进制**。
#
# 2026-10-16 踩的：交叉编译时忘了带 GOOS=linux GOARCH=amd64
# ⇒ 传上去的是个 Windows 的 exe ⇒ 线上报
#   `cannot execute binary file: Exec format error`
# 而那一刻服务是**停着的**（脚本先停再传）—— 停服时间白白拉长 ✗
#
# Windows 的 Git Bash 里没有 `file` 命令，所以直接看开头 4 个字节：
#   ELF 是 7f 45 4c 46（\x7fELF）；Windows 的 exe 是 4d 5a（MZ）
MAGIC=$(head -c 4 "$LOCAL_BIN" | od -An -tx1 | tr -d ' \n')
if [ "$MAGIC" != "7f454c46" ]; then
  echo "✗ $LOCAL_BIN 不是 Linux 二进制（开头是 $MAGIC）" >&2
  echo "  多半是编译时漏了 GOOS/GOARCH。重新编：" >&2
  echo "    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $LOCAL_BIN <那个包>" >&2
  exit 1
fi
echo "   二进制格式：ELF ✓"

ASK=$(mktemp -t askpass.XXXXXX)
cleanup() { rm -f "$ASK"; }
trap cleanup EXIT
printf '#!/bin/sh\necho %s\n' "$PASS" > "$ASK"
chmod +x "$ASK"
export SSH_ASKPASS="$ASK" SSH_ASKPASS_REQUIRE=force DISPLAY=:0

SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null
          -o ConnectTimeout=20 -o LogLevel=ERROR)
ssh_run() { ssh "${SSH_OPTS[@]}" "$TARGET" "$@"; }

# 中途挂了别把服务留在"停着"的状态：新版起不来就换回 .prev。
rescue() {
  rc=$?
  trap - EXIT
  if [ $rc -ne 0 ]; then
    echo
    echo "!! 部署失败（退出码 $rc），抢救中…"
    ssh_run "systemctl is-active $SERVICE >/dev/null 2>&1 || { \
               cp -f '${REMOTE_BIN}.prev' '$REMOTE_BIN' 2>/dev/null || true; \
               systemctl start $SERVICE 2>/dev/null || true; \
             }; echo 当前状态: \$(systemctl is-active $SERVICE 2>/dev/null || echo 无此服务)" || true
  fi
  exit $rc
}
trap rescue EXIT

echo "== 1/6 连上并看现状 =="
ssh_run "uname -srm"
if ssh_run "systemctl list-unit-files | grep -q '^$SERVICE'"; then
  echo "   已有服务，当前状态：$(ssh_run "systemctl is-active $SERVICE")"
  OLD_BIN=$(ssh_run "systemctl show -p ExecStart --value $SERVICE | sed -n 's/.*path=\\([^ ;]*\\).*/\\1/p'")
  [ -n "$OLD_BIN" ] && REMOTE_BIN="$OLD_BIN"
  echo "   远程二进制：$REMOTE_BIN"
else
  echo "   这台机器上还没有 $SERVICE，等下新建"
fi
echo "   本地新包：$LOCAL_BIN ($(stat -c %s "$LOCAL_BIN") 字节)"

echo "== 2/6 停服务（覆盖正在运行的二进制会失败）=="
ssh_run "systemctl stop $SERVICE 2>/dev/null || true"

echo "== 3/6 备份旧版 =="
ssh_run "if [ -f '$REMOTE_BIN' ]; then cp -f '$REMOTE_BIN' '${REMOTE_BIN}.prev' && echo '   已备份'; else echo '   （没有旧版，首次部署）'; fi"

echo "== 4/6 上传新二进制 =="
scp "${SSH_OPTS[@]}" "$LOCAL_BIN" "$TARGET:$REMOTE_BIN"
ssh_run "chmod +x '$REMOTE_BIN'"

echo "== 5/6 写 systemd 单元（已有就覆盖成同一份）=="
ssh_run "cat > /etc/systemd/system/$SERVICE.service <<'UNIT'
[Unit]
Description=P2P Link Signal Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$REMOTE_BIN -listen :$PORT -data $DATA_DIR $TLS_ARGS $STATS_ARGS $METRICS_ARGS
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload && systemctl enable $SERVICE >/dev/null 2>&1 && echo '   单元已就位'"

echo "== 6/6 启动 + 验证（$([ -n "$TLS_ARGS" ] && echo TLS || echo 明文)）=="
ssh_run "systemctl restart $SERVICE"
sleep 1.5
ssh_run "systemctl is-active $SERVICE; echo '---- 最近日志 ----'; journalctl -u $SERVICE -n 6 --no-pager | tail -6"

# 🩸🩸 **必查**：那个 socket 的权限就是这套东西的"鉴权"。
#
# 为什么要在这儿查、而不是靠单测：本地开发机是 Windows，没有 POSIX 权限位
# （os.Chmod 只切只读位，socket 一律报 0666）⇒ 那条单测在本地只能跳过 ✗
# 而**只有真正的 Linux 上**才验得了 —— 这儿就是那个"真正的 Linux" ✓
#
# 权限松了意味着：同机任何一个进程（这台机器上还跑着 nginx/postgres/博客）
# 都能 POST 一下把所有房间踢掉 ✗
# 指标落盘文件的权限：里面是这台机器的资源画像，同机别的用户不该能读
if [ -n "${P2P_METRICS:-}" ]; then
  MP=$(ssh_run "stat -c '%a' $P2P_METRICS 2>/dev/null || echo 没有")
  if [ "$MP" = "600" ]; then
    echo "   $P2P_METRICS 权限 600 ✓"
  else
    echo "   ✗ 指标文件权限是「$MP」，该是 600（同机别的用户不该能读）"
    exit 1
  fi
fi

if [ -n "${P2P_LOCAL_SOCK:-}" ]; then
  echo "== 附：查本机 socket 的权限（这个就是它的鉴权）=="
  PERM=$(ssh_run "stat -c '%a' $P2P_LOCAL_SOCK 2>/dev/null || echo 没有")
  if [ "$PERM" = "600" ]; then
    echo "   $P2P_LOCAL_SOCK 权限 600 ✓（只有 root 连得上）"
  else
    echo "   ✗ 权限是「$PERM」，该是 600 —— 同机其它进程能连上，能踢人 ✗"
    echo "   先别继续：查一下 socket 出现在哪个目录、那个目录谁可写"
    exit 1
  fi
fi

echo
echo "回滚办法："
echo "  bash scripts/sshx.sh $TARGET '<口令>' \"systemctl stop $SERVICE && cp -f ${REMOTE_BIN}.prev $REMOTE_BIN && systemctl start $SERVICE\""
