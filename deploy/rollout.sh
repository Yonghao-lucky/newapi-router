#!/bin/bash
# 13000 视频抽帧网关（new-api-video-router）双容器滚动更新。
#
# 结构：1Panel openresty（host 网络）监听 172.17.0.1/127.0.0.1:13001（迁移完成后为 13000），
# upstream video_router → app-a(127.0.0.1:13003) / app-b(127.0.0.1:13002)。
# 编排文件：/root/newapi-router-deploy/compose.yml（pg、redis 同项目，勿执行 down / --remove-orphans）。
#
# 用法：
#   deploy/rollout.sh [镜像名]          # 默认 new-api-video-router:next
#   deploy/rollout.sh --build           # 先用当前仓库 HEAD 构建（复用 prev 镜像的运行层，无需重装 ffmpeg）
#
# 每台先在 upstream 中标 down、等待进行中的请求结束（stop_grace_period 240s，
# SHUTDOWN_TIMEOUT_SECONDS=220），重建并健康后再挂回，任一步失败自动把 upstream 恢复为双后端。
set -euo pipefail

PROJ=/root/newapi-router-deploy
COMPOSE_FILE=compose.yml
UP=/opt/1panel/www/conf.d/00-video-router-upstream.conf
UPSTREAM=video_router
SERVICES=(app-a app-b)
PORTS=(13003 13002)
IMG=new-api-video-router:next
DRAIN_SECONDS="${DRAIN_SECONDS:-15}"
CTR="${OPENRESTY_CTR:-$(docker ps --format '{{.Names}}' | grep -m1 '^1Panel-openresty-' || true)}"
REPO_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [ "${1:-}" = "--build" ]; then
  SHA=$(git -C "$REPO_ROOT" rev-parse --short HEAD)
  TMP_DF=$(mktemp)
  # 运行层复用 prev 镜像（已含 ffmpeg），只替换二进制；Go 依赖走 goproxy.cn。
  python3 - "$REPO_ROOT/Dockerfile" "$TMP_DF" <<'PY'
import sys
s=open(sys.argv[1]).read()
s=s.replace("ENV GO111MODULE=on CGO_ENABLED=0 GOWORK=off","ENV GO111MODULE=on CGO_ENABLED=0 GOWORK=off GOPROXY=https://goproxy.cn,https://proxy.golang.org,direct",1)
i=s.index("FROM debian:bookworm-slim"); j=s.index("COPY --from=builder2 /build/new-api /")
s=s[:i]+"FROM new-api-video-router:prev\n\n"+s[j:]
open(sys.argv[2],"w").write(s)
PY
  echo "=== 构建 new-api-video-router:$SHA"
  docker build -f "$TMP_DF" --build-arg VCS_REF="$SHA" -t "new-api-video-router:$SHA" -t "$IMG" "$REPO_ROOT"
  rm -f "$TMP_DF"
elif [ -n "${1:-}" ]; then
  IMG="$1"
fi

docker image inspect "$IMG" >/dev/null || { echo "镜像不存在: $IMG" >&2; exit 1; }
[ -f "$UP" ] || { echo "upstream 不存在: $UP" >&2; exit 1; }
[ -n "$CTR" ] || { echo "找不到 openresty 容器" >&2; exit 1; }

set_upstream() {  # $1 = 保持服务的端口；留空 = 双后端
  local keep="${1:-}" lines="" i
  for i in 0 1; do
    if [ -z "$keep" ] || [ "$keep" = "${PORTS[$i]}" ]; then
      lines+="    server 127.0.0.1:${PORTS[$i]} max_fails=0;"$'\n'
    else
      lines+="    server 127.0.0.1:${PORTS[$i]} down;"$'\n'
    fi
  done
  local backup; backup=$(mktemp); cp "$UP" "$backup"
  printf 'upstream %s {\n%s    keepalive 256;\n}\n' "$UPSTREAM" "$lines" > "$UP"
  if ! docker exec "$CTR" nginx -t >/dev/null 2>&1; then
    cp "$backup" "$UP"; rm -f "$backup"; echo "nginx -t 失败，已恢复 upstream" >&2; return 1
  fi
  rm -f "$backup"; docker exec "$CTR" nginx -s reload
}

wait_healthy() {
  local i; for i in $(seq 1 80); do
    curl -sf -m 5 "http://127.0.0.1:$1/api/status" >/dev/null && { echo "healthy: $1"; return 0; }; sleep 3
  done; echo "健康检查失败: $1" >&2; return 1
}

trap 'echo "!!! 失败，恢复双后端" >&2; set_upstream "" || true' ERR

for i in 0 1; do
  curl -sf -m 5 "http://127.0.0.1:${PORTS[$i]}/api/status" >/dev/null || { echo "预检失败：${SERVICES[$i]} 不健康，拒绝滚动" >&2; exit 2; }
done

PREV=$(docker inspect --format '{{.Image}}' new-api-video-router-a)
docker tag "$PREV" new-api-video-router:prev
docker tag "$IMG" new-api-video-router:latest
echo "=== latest → $IMG（回滚点 new-api-video-router:prev）"

for i in 0 1; do
  peer=${PORTS[$(( (i + 1) % 2 ))]}
  echo "=== 更新 ${SERVICES[$i]} (${PORTS[$i]})，流量只留在 $peer"
  set_upstream "$peer"
  sleep "$DRAIN_SECONDS"
  ( cd "$PROJ" && docker compose -f "$COMPOSE_FILE" stop "${SERVICES[$i]}" && docker compose -f "$COMPOSE_FILE" up -d --no-deps --force-recreate "${SERVICES[$i]}" )
  wait_healthy "${PORTS[$i]}"
  set_upstream ""
  echo "=== ${SERVICES[$i]} 已恢复"
done
echo "=== 滚动更新完成，双后端已恢复"
