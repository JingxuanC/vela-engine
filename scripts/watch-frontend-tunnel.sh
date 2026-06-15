#!/usr/bin/env bash
# watch-frontend-tunnel.sh — 保活 cloudflared tunnel for Shopify frontend (port 5173)
# 由 Hermes 自动生成

TUNNEL_LOG="/tmp/cf_frontend_tunnel.log"
TUNNEL_PID_FILE="/tmp/cf_frontend_tunnel.pid"

while true; do
  # 检查 tunnel 进程是否活着
  if [ -f "$TUNNEL_PID_FILE" ]; then
    OLD_PID=$(cat "$TUNNEL_PID_FILE")
    if kill -0 "$OLD_PID" 2>/dev/null; then
      sleep 30
      continue
    fi
  fi

  echo "[$(date)] Tunnel 挂了，重启中..." >> "$TUNNEL_LOG"
  cloudflared tunnel --url http://localhost:5173 --no-autoupdate &>> "$TUNNEL_LOG" &
  echo $! > "$TUNNEL_PID_FILE"
  sleep 5
done
