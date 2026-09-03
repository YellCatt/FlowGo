#!/bin/sh
# ============================================================
# FlowGo 守护脚本 (startup.sh)
#
# 职责：下载/更新 -> 启动 -> HTTP 健康检查 -> 崩溃自动重启 -> 邮件通知
# 适用：OpenWrt / 嵌入式 Linux（BusyBox ash），遵循 POSIX sh
# 用法：sh startup.sh                前台运行
#       nohup sh startup.sh &        后台运行
#
# 与 gwatch 守护脚本的主要差异（针对 FlowGo 的适配）：
#   1. 发布物是 tar.gz 压缩包（内含 release/flowgo_<平台>），需解包后取二进制；
#   2. FlowGo 以「当前工作目录」定位 config/config.yaml 与 data.db，
#      因此必须先 cd 到插件目录再启动，且更新时只替换二进制，绝不触碰配置与数据库；
#   3. 增加 HTTP /health 健康探测（进程存活 + 接口可服务双重判断）；
#   4. 更新前自动备份旧二进制，新版本启动失败自动回滚。
# ============================================================

# ============ 配置区 ============
PLUGIN_DIR="/plugins/data/flowgo"
BINARY_NAME="flowgo"                 # 安装后的固定文件名（与平台后缀解耦）
BAK_NAME="flowgo.bak"                # 更新前的备份，用于启动失败回滚
TMP_DIR="$PLUGIN_DIR/.tmp_update"    # 下载/解包临时目录
NEW_BINARY="$TMP_DIR/flowgo.new"     # 解包后待安装的新二进制
LOG_FILE="$PLUGIN_DIR/logs/flowgo-daemon.log"
PID_FILE="$PLUGIN_DIR/flowgo-daemon.pid"
CONFIG_FILE="$PLUGIN_DIR/config/config.yaml"
CHECK_FILE="$PLUGIN_DIR/.last_update_check"

# 发布包下载地址（tar.gz，内部结构为 release/flowgo_<平台>）
DOWNLOAD_URL="https://github.com/YellCatt/FlowGo/releases/download/dev-latest/flowgo_linux_mipsle.tar.gz"
ARCHIVE_NAME="${DOWNLOAD_URL##*/}"    # 自动截取 URL 末段作为包名：flowgo_linux_mipsle.tar.gz

# 平台后缀：必须与压缩包内的二进制名一致，即 release/flowgo_linux_mipsle
# 换平台时请同时修改 DOWNLOAD_URL 与本项，例如 amd64：
#   DOWNLOAD_URL=.../flowgo_linux_amd64.tar.gz    TARGET_SUFFIX="linux_amd64"
TARGET_SUFFIX="linux_mipsle"

# 更新与网络
UPDATE_INTERVAL=10800                # 更新检查间隔（秒）：10800=3小时 86400=24小时
MAX_RETRY=20                         # 下载最大重试次数
CONNECT_TIMEOUT=120                  # 连接超时（秒）
MAX_DOWNLOAD_TIME=1200               # 单次下载最大耗时（秒）
NETWORK_PROBE_HOST="8.8.8.8"         # 网络就绪探测地址

# 进程守护
RESTART_DELAY=5                      # 初始重启延迟（秒）
MAX_RESTART_DELAY=300                # 指数退避上限（秒）
GRACEFUL_SHUTDOWN_TIMEOUT=15         # 优雅退出等待，需大于 FlowGo 内部 shutdownTimeout(10s)
HEALTH_CHECK_INTERVAL=30             # 健康探测间隔（秒）
HEALTH_FAIL_THRESHOLD=3              # 连续健康检查失败几次后重启
STARTUP_GRACE=20                     # 启动后等待健康检查通过的最长时间（秒）

# 日志
MAX_LOG_SIZE=2097152                 # 守护日志超过 2MB 自动轮转

# 邮件通知开关，0关闭 1开启
ENABLE_MAIL_NOTIFY=1
# mailgo 邮件发送工具的绝对路径
MAILGO_BIN="/plugins/data/mailgo/mailgo"

# 源码编译兜底：仅当下载全部失败且本机有 Go 工具链时尝试（0关闭 1开启）
ENABLE_SOURCE_BUILD_FALLBACK=0
SOURCE_DIR=""                        # 源码目录，例如 "/plugins/data/flowgo-src"；留空则跳过

# ============ 全局状态 ============
CHILD_PID=""
NEED_UPDATE=0
RUNNING=1
CURRENT_DELAY=$RESTART_DELAY
HEALTH_FAIL_COUNT=0
SERVER_PORT=9001

# 预创建目录，确保早期日志能写入
mkdir -p "$PLUGIN_DIR/logs" 2>/dev/null

# ============ 日志函数 ============
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}
log_info()  { log "【信息】$1"; }
log_ok()    { log "【成功】✓ $1"; }
log_warn()  { log "【警告】⚠ $1"; }
log_error() { log "【错误】✗ $1"; }
log_step()  { log "【步骤】$1"; }

# rotate_log 守护日志按大小轮转，避免嵌入式设备存储被写满。
rotate_log() {
    [ -f "$LOG_FILE" ] || return 0
    local size
    size=$(wc -c < "$LOG_FILE" 2>/dev/null | tr -d ' ')
    case "$size" in
        ''|*[!0-9]*) return 0 ;;
    esac
    if [ "$size" -gt "$MAX_LOG_SIZE" ]; then
        mv "$LOG_FILE" "$LOG_FILE.1" 2>/dev/null
        log_info "守护日志超过 ${MAX_LOG_SIZE} 字节，已轮转为 $LOG_FILE.1"
    fi
}

# ============ mailgo 邮件通知封装 ============
send_mailgo() {
    if [ "$ENABLE_MAIL_NOTIFY" -ne 1 ]; then
        return 0
    fi
    local subject="$1"
    local body="$2"
    log_info "尝试发送邮件通知，标题: $subject"
    if [ -x "$MAILGO_BIN" ]; then
        "$MAILGO_BIN" -subject "$subject" -body "$body" >> "$LOG_FILE" 2>&1
        local mail_rc=$?
        if [ "$mail_rc" -eq 0 ]; then
            log_ok "邮件通知发送成功"
        else
            log_warn "mailgo 执行返回码 $mail_rc，邮件发送失败"
        fi
    else
        log_warn "未找到 mailgo 命令，跳过邮件通知"
    fi
}

# ============ 读取 FlowGo 监听端口 ============
# FlowGo 端口来自 config/config.yaml 的 server.port，缺失时回退默认值 9001。
get_server_port() {
    local p=""
    if [ -f "$CONFIG_FILE" ]; then
        p=$(sed -n 's/^[[:space:]]*port:[[:space:]]*\([0-9]*\).*/\1/p' "$CONFIG_FILE" | head -n 1)
    fi
    if [ -n "$p" ]; then
        echo "$p"
    else
        echo 9001
    fi
}

# ============ 清理函数 ============
cleanup() {
    log_info "收到退出信号，开始清理..."
    RUNNING=0
    if [ -n "$CHILD_PID" ]; then
        kill "$CHILD_PID" 2>/dev/null
        sleep 1
        kill -9 "$CHILD_PID" 2>/dev/null
        wait "$CHILD_PID" 2>/dev/null
    fi
    [ -f "$PID_FILE" ] && rm -f "$PID_FILE"
    rm -rf "$TMP_DIR" 2>/dev/null
    log_ok "清理完成，脚本退出"
    exit 0
}
trap 'cleanup' INT TERM

# ============ 启动前强制清理残留进程 ============
killall -9 flowgo 2>/dev/null
rm -f "$PID_FILE"
log_info "已清理可能残留的 flowgo 进程和 PID 文件"
rotate_log

# ============ 防重复启动 ============
log "========================================"
log_info "FlowGo 守护脚本启动"
log_info "当前工作目录: $(pwd)"
log_info "插件目录: $PLUGIN_DIR"
log_info "下载地址: $DOWNLOAD_URL"
log_info "安装包名: $ARCHIVE_NAME"
log_info "目标平台后缀: $TARGET_SUFFIX"
log_info "最大下载重试: $MAX_RETRY 次"
log_info "更新检查间隔: ${UPDATE_INTERVAL} 秒"
log_info "健康检查间隔: ${HEALTH_CHECK_INTERVAL} 秒（连续失败 ${HEALTH_FAIL_THRESHOLD} 次重启）"
log_info "连接超时: ${CONNECT_TIMEOUT} 秒"
log_info "单次下载最大耗时: ${MAX_DOWNLOAD_TIME} 秒"
log_info "邮件通知: $( [ "$ENABLE_MAIL_NOTIFY" -eq 1 ] && echo "开启" || echo "关闭")"

if [ -f "$PID_FILE" ]; then
    OLD_PID=$(cat "$PID_FILE" 2>/dev/null)
    if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
        log_error "检测到已有实例在运行 (PID: $OLD_PID)，请勿重复启动"
        send_mailgo "[FlowGo守护脚本] 【告警】检测到重复实例，本次启动终止" \
            "检测到已有 FlowGo 守护实例运行，PID=${OLD_PID}，本次守护脚本直接退出。时间：$(date '+%Y-%m-%d %H:%M:%S')"
        exit 1
    else
        log_warn "发现残留 PID 文件，但对应进程已不存在，继续启动"
        rm -f "$PID_FILE"
    fi
fi
echo $$ > "$PID_FILE"
log_ok "PID 文件已写入: $PID_FILE (当前 PID: $$)"

# ============ 等待网络就绪 ============
log_step "等待网络就绪..."
NETWORK_WAIT=0
while true; do
    if ping -c 1 -W 3 "$NETWORK_PROBE_HOST" > /dev/null 2>&1; then
        log_ok "网络已就绪 (累计等待 ${NETWORK_WAIT} 秒)"
        break
    fi
    NETWORK_WAIT=$((NETWORK_WAIT + 5))
    if [ $((NETWORK_WAIT % 60)) -eq 0 ]; then
        log_warn "网络未就绪，已等待 ${NETWORK_WAIT} 秒，继续等待..."
        send_mailgo "[FlowGo守护脚本] 【警告】长时间等待网络连通" \
            "ping ${NETWORK_PROBE_HOST} 持续失败，累计等待 ${NETWORK_WAIT} 秒。时间：$(date '+%Y-%m-%d %H:%M:%S')"
    fi
    sleep 5
done
log "========================================"

# ============ 检查并创建插件目录 ============
log_step "检查插件目录..."
if [ ! -d "$PLUGIN_DIR" ]; then
    log_info "目录不存在，正在创建: $PLUGIN_DIR"
    if mkdir -p "$PLUGIN_DIR"; then
        log_ok "目录创建成功"
    else
        log_error "目录创建失败，退出"
        send_mailgo "[FlowGo守护脚本] 【告警】插件目录创建失败" \
            "目录 ${PLUGIN_DIR} 创建失败，脚本直接退出。时间：$(date '+%Y-%m-%d %H:%M:%S')"
        exit 1
    fi
else
    log_ok "目录已存在: $PLUGIN_DIR"
fi

# ============ 进入插件目录 ============
# 关键：FlowGo 以相对路径读取 config/config.yaml 与 data.db，必须在此目录启动。
cd "$PLUGIN_DIR" || {
    log_error "进入目录失败: $PLUGIN_DIR"
    send_mailgo "[FlowGo守护脚本] 【告警】无法进入工作目录" \
        "cd ${PLUGIN_DIR} 失败，脚本退出。时间：$(date '+%Y-%m-%d %H:%M:%S')"
    exit 1
}
log_ok "已进入工作目录: $PLUGIN_DIR"
SERVER_PORT=$(get_server_port)
log_info "服务监听端口: $SERVER_PORT (取自 $CONFIG_FILE，未配置则为默认 9001)"

# ============ 下载安装包 ============
# 成功时把 tar.gz 保存到 $TMP_DIR/$ARCHIVE_NAME 并返回 0。
download_binary() {
    log_step "尝试从 GitHub 下载最新版本..."
    log_info "下载地址: $DOWNLOAD_URL"

    # 清理残留的旧下载 curl 进程
    log_info "检查是否存在残留的旧下载 curl 进程..."
    OLD_CURL_PIDS=$(ps | grep "$DOWNLOAD_URL" | grep curl | grep -v grep | awk '{print $1}')
    if [ -n "$OLD_CURL_PIDS" ]; then
        log_warn "发现残留下载进程: $OLD_CURL_PIDS，准备终止"
        for pid in $OLD_CURL_PIDS; do
            kill "$pid" 2>/dev/null
            sleep 0.5
            kill -9 "$pid" 2>/dev/null
        done
        log_info "旧下载进程已清理"
    fi

    rm -rf "$TMP_DIR"
    mkdir -p "$TMP_DIR"

    local retry=0
    while [ "$retry" -lt "$MAX_RETRY" ]; do
        retry=$((retry + 1))
        log_info "第 $retry / $MAX_RETRY 次下载尝试 (连接超时 ${CONNECT_TIMEOUT}s, 最大耗时 ${MAX_DOWNLOAD_TIME}s)..."
        curl -L -k --connect-timeout "$CONNECT_TIMEOUT" --max-time "$MAX_DOWNLOAD_TIME" \
             -o "$TMP_DIR/$ARCHIVE_NAME" "$DOWNLOAD_URL"
        local curl_exit=$?

        # 同时校验退出码、非空与 tar.gz 完整性，避免把 HTML 错误页当成安装包
        if [ "$curl_exit" -eq 0 ] && [ -s "$TMP_DIR/$ARCHIVE_NAME" ] \
           && tar -tzf "$TMP_DIR/$ARCHIVE_NAME" > /dev/null 2>&1; then
            local size
            size=$(ls -lh "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $5}')
            log_ok "下载成功，压缩包大小: $size"
            return 0
        fi

        log_error "下载失败 (curl 退出码: $curl_exit，或文件为空/不是合法 tar.gz)"
        rm -f "$TMP_DIR/$ARCHIVE_NAME"
        if [ "$retry" -lt "$MAX_RETRY" ]; then
            log_info "等待 10 秒后重试..."
            sleep 10
        fi
    done

    log_error "已达到最大重试次数 ($MAX_RETRY)，下载失败"
    return 1
}

# ============ 解压安装包并取出二进制 ============
# 发布包内部结构为 release/flowgo_<平台>；
# 成功时把二进制放到 $NEW_BINARY 并返回 0。
extract_binary() {
    log_step "开始解压安装包: $ARCHIVE_NAME"

    if [ ! -f "$TMP_DIR/$ARCHIVE_NAME" ]; then
        log_error "安装包不存在，无法解压: $TMP_DIR/$ARCHIVE_NAME"
        return 1
    fi

    # 用子 shell 切目录而非 tar -C，兼容部分 BusyBox tar
    if ! (cd "$TMP_DIR" && tar -xzf "$ARCHIVE_NAME") 2>/dev/null; then
        log_error "解压失败，安装包可能已损坏"
        return 1
    fi
    log_ok "解压完成"

    local candidate="$TMP_DIR/release/flowgo_${TARGET_SUFFIX}"
    if [ ! -f "$candidate" ]; then
        # 兜底：在解包目录中寻找 flowgo 开头的可执行文件
        log_warn "未找到预期的 release/flowgo_${TARGET_SUFFIX}，尝试自动查找"
        candidate=$(find "$TMP_DIR" -type f -name 'flowgo*' ! -name '*.tar.gz' | head -n 1)
    fi
    if [ -z "$candidate" ] || [ ! -f "$candidate" ]; then
        log_error "解压后未找到 flowgo 二进制文件"
        return 1
    fi

    mv -f "$candidate" "$NEW_BINARY"
    chmod +x "$NEW_BINARY"
    rm -f "$TMP_DIR/$ARCHIVE_NAME"

    local bsize
    bsize=$(ls -lh "$NEW_BINARY" | awk '{print $5}')
    log_ok "新版本二进制已就绪: $NEW_BINARY (大小: $bsize)"
    return 0
}

# ============ 获取新版本二进制：下载 + 解压 ============
prepare_new_binary() {
    download_binary || return 1
    extract_binary
}

# ============ 源码编译兜底 ============
# 仅当下载全部失败且开启了开关、且本机存在 Go 工具链时使用。
build_from_source() {
    if [ "$ENABLE_SOURCE_BUILD_FALLBACK" -ne 1 ] || [ -z "$SOURCE_DIR" ]; then
        return 1
    fi
    if ! command -v go > /dev/null 2>&1; then
        log_warn "未找到 go 命令，跳过源码编译兜底"
        return 1
    fi
    if [ ! -d "$SOURCE_DIR" ]; then
        log_warn "源码目录不存在: $SOURCE_DIR，跳过源码编译兜底"
        return 1
    fi
    log_step "下载失败，尝试从源码编译: $SOURCE_DIR"
    rm -rf "$TMP_DIR"
    mkdir -p "$TMP_DIR"
    if (cd "$SOURCE_DIR" && env $GO_BUILD_FLAGS go build -o "$NEW_BINARY" .) >> "$LOG_FILE" 2>&1; then
        chmod +x "$NEW_BINARY"
        log_ok "源码编译成功: $NEW_BINARY"
        return 0
    fi
    log_error "源码编译失败"
    return 1
}

# ============ 获取可用二进制（下载优先，源码兜底） ============
fetch_binary() {
    if prepare_new_binary; then
        return 0
    fi
    build_from_source
}

# ============ 程序控制函数 ============
is_process_alive() {
    [ -n "$CHILD_PID" ] && [ -d "/proc/$CHILD_PID" ]
}

# health_probe 通过 GET /health 判断服务是否真正可服务。
# 若环境无 curl/wget，则退化为「进程存活即健康」。
health_probe() {
    # 每次实时读取端口，配置里改了端口也能正确探测
    local port code=""
    port=$(get_server_port)
    if command -v curl > /dev/null 2>&1; then
        code=$(curl -s -m 10 -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/health" 2>/dev/null)
    elif command -v wget > /dev/null 2>&1; then
        if wget -q -T 10 -O /dev/null "http://127.0.0.1:${port}/health" > /dev/null 2>&1; then
            code="200"
        fi
    else
        # 无 HTTP 探测工具，退化为进程存活检测
        return 0
    fi
    [ "$code" = "200" ]
}

start_program() {
    if [ ! -f "$BINARY_NAME" ]; then
        log_error "二进制文件不存在，无法启动: $BINARY_NAME"
        return 1
    fi
    "./$BINARY_NAME" >> "$LOG_FILE" 2>&1 &
    CHILD_PID=$!
    log_ok "程序已启动 (PID: $CHILD_PID)"

    # 等待健康检查通过，避免把「启动即崩溃」误判为运行正常
    local waited=0
    while [ "$waited" -lt "$STARTUP_GRACE" ]; do
        if ! is_process_alive; then
            log_error "程序启动后立即退出"
            CHILD_PID=""
            return 1
        fi
        if health_probe; then
            log_ok "服务健康检查通过 (等待 ${waited}s，端口 ${SERVER_PORT})"
            HEALTH_FAIL_COUNT=0
            return 0
        fi
        sleep 2
        waited=$((waited + 2))
    done
    log_warn "启动后 ${STARTUP_GRACE}s 内健康检查未通过，视为启动失败"
    return 1
}

stop_program() {
    if [ -z "$CHILD_PID" ] || [ ! -d "/proc/$CHILD_PID" ]; then
        CHILD_PID=""
        return 0
    fi
    log_info "正在停止程序 (PID: $CHILD_PID)..."
    kill "$CHILD_PID" 2>/dev/null
    local count=0
    while [ -d "/proc/$CHILD_PID" ] && [ "$count" -lt "$GRACEFUL_SHUTDOWN_TIMEOUT" ]; do
        sleep 1
        count=$((count + 1))
    done
    if [ -d "/proc/$CHILD_PID" ]; then
        log_warn "程序未在 ${GRACEFUL_SHUTDOWN_TIMEOUT} 秒内退出，强制终止"
        kill -9 "$CHILD_PID" 2>/dev/null
        sleep 1
    fi
    wait "$CHILD_PID" 2>/dev/null
    local stop_exit=$?
    CHILD_PID=""
    return $stop_exit
}

# ============ 安装新版本（含备份）与回滚 ============
# 只替换二进制本身，config/ 与 data.db 不受影响。
install_new_binary() {
    if [ -f "$BINARY_NAME" ]; then
        cp -f "$BINARY_NAME" "$BAK_NAME" 2>/dev/null
        log_info "已备份当前版本为 $BAK_NAME"
    fi
    mv -f "$NEW_BINARY" "$BINARY_NAME"
    chmod +x "$BINARY_NAME"
    log_ok "新版本已就位: $BINARY_NAME"
}

rollback_binary() {
    if [ -f "$BAK_NAME" ]; then
        log_warn "正在回滚到更新前的版本..."
        mv -f "$BAK_NAME" "$BINARY_NAME"
        chmod +x "$BINARY_NAME"
        return 0
    fi
    log_error "未找到备份文件 $BAK_NAME，无法回滚"
    return 1
}

# ============ 将秒数转换为人类可读的时长 ============
format_duration() {
    local total=$1
    local days=$((total / 86400))
    local hours=$(((total % 86400) / 3600))
    local mins=$(((total % 3600) / 60))
    local secs=$((total % 60))
    local result=""
    [ $days -gt 0 ] && result="${result}${days}天"
    [ $hours -gt 0 ] && result="${result}${hours}小时"
    [ $mins -gt 0 ] && result="${result}${mins}分"
    [ $secs -gt 0 ] || [ -z "$result" ] && result="${result}${secs}秒"
    echo "$result"
}

# ============ 时间戳转人类可读时间（兼容 GNU/BusyBox/BSD） ============
format_timestamp() {
    local ts=$1
    local fmt
    fmt=$(date -d "@$ts" '+%Y-%m-%d %H:%M:%S' 2>/dev/null)
    if [ -n "$fmt" ]; then
        echo "$fmt"
        return
    fi
    fmt=$(date -r "$ts" '+%Y-%m-%d %H:%M:%S' 2>/dev/null)
    if [ -n "$fmt" ]; then
        echo "$fmt"
        return
    fi
    echo "时间戳 $ts"
}

# ============ 打印本次与下次检查时间 ============
log_update_schedule() {
    local check_ts=$1
    local next_ts=$((check_ts + UPDATE_INTERVAL))
    log_info "本次更新检查时间: $(format_timestamp "$check_ts")"
    log_info "下次更新预计检查时间: $(format_timestamp "$next_ts") (间隔 $(format_duration $UPDATE_INTERVAL))"
}

# ============ 更新检查（按间隔下载对比，不调用 GitHub API） ============
check_and_update() {
    local now last_check elapsed
    now=$(date +%s)
    last_check=0
    if [ -f "$CHECK_FILE" ]; then
        last_check=$(cat "$CHECK_FILE" | cut -d'|' -f1)
    fi
    elapsed=$((now - last_check))
    if [ "$elapsed" -lt "$UPDATE_INTERVAL" ]; then
        return 0
    fi

    log_step "距离上次更新已 $(format_duration $elapsed)（${elapsed}秒），开始下载最新版本..."
    # 先下载，只有成功后才记录检查时间
    if fetch_binary; then
        echo "$now|$(date '+%Y-%m-%d %H:%M:%S %Z (UTC%z)')|$(format_timestamp $((now + UPDATE_INTERVAL)))" > "$CHECK_FILE"
        log_update_schedule "$now"

        if [ -f "$BINARY_NAME" ] && cmp -s "$NEW_BINARY" "$BINARY_NAME"; then
            log_info "下载的文件与当前版本一致，无需替换"
            rm -f "$NEW_BINARY"
            return 0
        fi
        if [ -f "$BINARY_NAME" ]; then
            log_info "下载的文件与当前版本不同，准备替换"
        else
            log_info "当前无旧版本，直接启用新版本"
        fi
        NEED_UPDATE=1
        return 0
    else
        log_warn "更新检查失败，继续使用当前版本"
        # 不更新时间戳，下次进入时仍会尝试
        return 1
    fi
}

# ============ 热更新（含失败回滚） ============
do_hot_update() {
    NEED_UPDATE=0
    log_step "执行热更新... (当前时间: $(date '+%Y-%m-%d %H:%M:%S'))"

    stop_program
    install_new_binary

    if start_program; then
        log_ok "热更新完成 (当前时间: $(date '+%Y-%m-%d %H:%M:%S'))"
        send_mailgo "[FlowGo守护脚本] 【通知】FlowGo 完成热更新" \
            "已下载新版本并完成热更新重启，时间：$(date '+%Y-%m-%d %H:%M:%S')"
        rm -rf "$TMP_DIR" 2>/dev/null
        CURRENT_DELAY=$RESTART_DELAY
        # 打印下次预计检查时间（基于上次成功检查的时间戳）
        if [ -f "$CHECK_FILE" ]; then
            local last_check next_ts
            last_check=$(cat "$CHECK_FILE" | cut -d'|' -f1)
            next_ts=$((last_check + UPDATE_INTERVAL))
            log_info "下次预计检查时间: $(format_timestamp "$next_ts") (间隔 $(format_duration $UPDATE_INTERVAL))"
        fi
        return 0
    fi

    log_error "新版本启动失败，尝试回滚"
    rollback_binary
    if start_program; then
        log_warn "已回滚到旧版本并恢复运行"
        send_mailgo "[FlowGo守护脚本] 【告警】FlowGo 新版本启动失败，已回滚" \
            "新版本启动失败，已自动回滚到上一版本并恢复服务。时间：$(date '+%Y-%m-%d %H:%M:%S')"
        rm -rf "$TMP_DIR" 2>/dev/null
        return 1
    fi

    log_error "回滚后依然无法启动，守护循环终止"
    send_mailgo "[FlowGo守护脚本] 【告警】FlowGo 更新失败且回滚后无法启动" \
        "新版本启动失败，回滚后仍无法拉起 FlowGo，守护循环终止。时间：$(date '+%Y-%m-%d %H:%M:%S')"
    return 1
}

# ============ 健康守护：连续失败达到阈值则重启 ============
watchdog_health_check() {
    if ! health_probe; then
        HEALTH_FAIL_COUNT=$((HEALTH_FAIL_COUNT + 1))
        log_warn "健康检查失败 ($HEALTH_FAIL_COUNT/$HEALTH_FAIL_THRESHOLD)，端口 ${SERVER_PORT}"
        if [ "$HEALTH_FAIL_COUNT" -ge "$HEALTH_FAIL_THRESHOLD" ]; then
            log_error "连续健康检查失败达到阈值，重启服务"
            send_mailgo "[FlowGo守护脚本] 【告警】FlowGo 健康检查失败，已重启" \
                "连续 ${HEALTH_FAIL_THRESHOLD} 次访问 http://127.0.0.1:${SERVER_PORT}/health 失败，已重启服务。时间：$(date '+%Y-%m-%d %H:%M:%S')"
            HEALTH_FAIL_COUNT=0
            stop_program
            if ! start_program; then
                log_error "健康检查触发的重启失败，交由主循环退避重试"
                return 1
            fi
        fi
    else
        if [ "$HEALTH_FAIL_COUNT" -ne 0 ]; then
            log_ok "健康检查恢复正常"
        fi
        HEALTH_FAIL_COUNT=0
    fi
    return 0
}

# ============ 主守护循环 ============
main_loop() {
    # 启动时优先拉起本地版本，避免下载阻塞服务上线
    if ! start_program; then
        log_warn "本地版本不存在或启动失败，尝试获取..."
        if fetch_binary; then
            mv -f "$NEW_BINARY" "$BINARY_NAME"
            chmod +x "$BINARY_NAME"
            if ! start_program; then
                log_error "程序启动失败，守护循环终止"
                send_mailgo "[FlowGo守护脚本] 【告警】FlowGo 启动失败，守护循环终止" \
                    "获取二进制完成后依然无法启动 FlowGo，守护脚本退出。时间：$(date '+%Y-%m-%d %H:%M:%S')"
                return 1
            fi
        else
            log_error "获取二进制失败且无本地版本，无法启动"
            send_mailgo "[FlowGo守护脚本] 【告警】FlowGo 二进制获取失败，服务无法启动" \
                "已用尽最大重试次数 ${MAX_RETRY}，且无本地二进制，FlowGo 完全无法运行。时间：$(date '+%Y-%m-%d %H:%M:%S')"
            return 1
        fi
    fi
    CURRENT_DELAY=$RESTART_DELAY

    # 标记首次检查：程序启动后立即检查新版本
    FIRST_CHECK=1
    while [ "$RUNNING" -eq 1 ]; do
        if is_process_alive; then
            # 程序正常运行中 —— 检查更新
            if [ "$FIRST_CHECK" -eq 1 ]; then
                log_step "程序已启动，立即检查新版本..."
                FIRST_CHECK=0
                if fetch_binary; then
                    local now_ts next_ts
                    now_ts=$(date +%s)
                    next_ts=$((now_ts + UPDATE_INTERVAL))
                    echo "$now_ts|$(date '+%Y-%m-%d %H:%M:%S %Z (UTC%z)')|$(format_timestamp "$next_ts")" > "$CHECK_FILE"
                    log_update_schedule "$now_ts"
                    if [ -f "$BINARY_NAME" ] && cmp -s "$NEW_BINARY" "$BINARY_NAME"; then
                        log_info "当前已是最新版本，无需替换"
                        rm -f "$NEW_BINARY"
                    else
                        log_ok "发现新版本，准备热更新"
                        NEED_UPDATE=1
                    fi
                else
                    log_warn "启动后更新检查失败，继续使用当前版本"
                fi
            else
                check_and_update
            fi

            if [ "$NEED_UPDATE" -eq 1 ]; then
                if ! do_hot_update; then
                    # 回滚成功会继续运行；返回失败且服务未拉起时退出循环
                    if ! is_process_alive; then
                        log_error "热更新后服务未运行，守护循环终止"
                        break
                    fi
                fi
            fi

            # 健康探测 + 间隔等待
            watchdog_health_check
            rotate_log
            sleep "$HEALTH_CHECK_INTERVAL"
        else
            # 程序已退出（异常或正常）
            if [ -n "$CHILD_PID" ]; then
                wait "$CHILD_PID" 2>/dev/null
                EXIT_CODE=$?
                log "========================================"
                log_info "程序已退出，退出码: $EXIT_CODE"
                if [ "$EXIT_CODE" -eq 0 ]; then
                    log_info "状态: 正常退出"
                else
                    log_error "状态: 异常退出"
                    send_mailgo "[FlowGo守护脚本] 【告警】FlowGo 服务异常退出" \
                        "FlowGo 子进程异常退出，退出码=${EXIT_CODE}，即将指数退避重启。时间：$(date '+%Y-%m-%d %H:%M:%S')"
                fi
                CHILD_PID=""
            fi

            # 退出后先尝试更新
            check_and_update
            if [ "$NEED_UPDATE" -eq 1 ]; then
                install_new_binary
                log_ok "已更新到新版本 (当前时间: $(date '+%Y-%m-%d %H:%M:%S'))"
                NEED_UPDATE=0
            fi

            # 指数退避重启
            log_info "等待 ${CURRENT_DELAY} 秒后重启..."
            sleep "$CURRENT_DELAY"
            CURRENT_DELAY=$((CURRENT_DELAY * 2))
            if [ "$CURRENT_DELAY" -gt "$MAX_RESTART_DELAY" ]; then
                CURRENT_DELAY=$MAX_RESTART_DELAY
            fi
            if ! start_program; then
                log_error "重启失败，守护循环终止"
                send_mailgo "[FlowGo守护脚本] 【告警】FlowGo 多次重启失败，守护循环终止" \
                    "FlowGo 多次重启失败，守护脚本不再尝试拉起，服务停止。时间：$(date '+%Y-%m-%d %H:%M:%S')"
                break
            fi
            CURRENT_DELAY=$RESTART_DELAY
        fi
    done
}

# ============ 启动 ============
main_loop
cleanup
