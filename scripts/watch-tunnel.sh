#!/bin/bash
# Cloudflare Tunnel 自动保活脚本
# 每隔 30 秒检查 cloudflared 是否还在，挂了自动重启

LOGFILE="/tmp/cf_vela_tunnel.log"
TUNNEL_URL_FILE="/tmp/cf_vela_tunnel_url.txt"
WATCH_INTERVAL=30

log() {
    echo "[$(date '+%H:%M:%S')] $*" >> "$LOGFILE"
}

ensure_tunnel() {
    if pgrep -f "cloudflared tunnel --url http://localhost:8000" > /dev/null 2>&1; then
        return 0
    fi

    log "Tunnel down — restarting..."
    nohup cloudflared tunnel --url http://localhost:8000 --no-autoupdate \
        >> "$LOGFILE" 2>&1 &
    sleep 5

    # 提取新的 URL
    URL=$(grep -o "https://[a-z0-9.-]*\.trycloudflare\.com" "$LOGFILE" | tail -1)
    if [ -n "$URL" ]; then
        echo "$URL" > "$TUNNEL_URL_FILE"
        log "New tunnel: $URL"
    else
        log "WARNING: tunnel started but URL not yet available"
    fi
}

log "=== Tunnel watcher started ==="
ensure_tunnel

while true; do
    sleep "$WATCH_INTERVAL"
    ensure_tunnel
done
