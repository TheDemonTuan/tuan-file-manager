#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="${APP_DIR:-$SCRIPT_DIR}"
COMPOSE="$APP_DIR/compose.prod.yaml"
APP_ENV="$APP_DIR/.env"

log() { printf '%s  %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }
die() { log "ERROR: $*" >&2; exit 1; }

log "Starting filemgr deployment on VPS..."

cd "$APP_DIR"
docker compose --env-file "$APP_ENV" -f "$COMPOSE" pull app || true
docker compose --env-file "$APP_ENV" -f "$COMPOSE" up -d --build app

log "Waiting for filemgr-app to become healthy..."
READY=0
for i in $(seq 1 30); do
  STATUS=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' filemgr-app-1 2>/dev/null || echo "starting")
  if [ "$STATUS" = "healthy" ]; then
    READY=1
    break
  fi
  sleep 2
done

if [ "$READY" -eq 1 ]; then
  log "Deployment completed successfully! Container status: healthy."
else
  docker logs --tail 50 filemgr-app-1
  die "Deployment failed: filemgr-app-1 is not healthy."
fi
