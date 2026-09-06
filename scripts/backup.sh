#!/bin/sh
set -eu

echo "[INFO] Starting filemgr backup to Cloudflare R2 via Restic..."

DATA_DIR="/data"
SNAPSHOT_DIR="${DATA_DIR}/backup-snapshot"
TIMESTAMP=$(date -u +"%Y%m%dT%H%M%SZ")

mkdir -p "${SNAPSHOT_DIR}"
rm -rf "${SNAPSHOT_DIR:?}"/*

# 1. Snapshot SQLite database
echo "[INFO] Snapshotting database..."
if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 "${DATA_DIR}/db/app.db" ".backup '${SNAPSHOT_DIR}/app.db'"
else
    # Fallback to file copy if sqlite3 CLI not present in restic image
    cp "${DATA_DIR}/db/app.db" "${SNAPSHOT_DIR}/app.db"
fi

# 2. Hardlink / prepare objects snapshot
echo "[INFO] Preparing objects snapshot..."
mkdir -p "${SNAPSHOT_DIR}/objects"
if [ -d "${DATA_DIR}/objects" ]; then
    cp -al "${DATA_DIR}/objects/." "${SNAPSHOT_DIR}/objects/" 2>/dev/null || cp -r "${DATA_DIR}/objects/." "${SNAPSHOT_DIR}/objects/"
fi

# 3. Check / initialize restic repo
echo "[INFO] Verifying Restic repository in Cloudflare R2..."
if ! restic snapshots >/dev/null 2>&1; then
    echo "[INFO] Initializing new Restic repository..."
    restic init
fi

# 4. Perform backup
echo "[INFO] Uploading encrypted snapshot to R2..."
restic backup "${SNAPSHOT_DIR}" \
    --tag "filemgr" \
    --tag "${TIMESTAMP}" \
    --host "filemgr-vps"

# 5. Prune old snapshots (Retention: 7 daily, 4 weekly, 6 monthly)
echo "[INFO] Pruning old snapshots per retention policy..."
restic forget \
    --keep-daily 7 \
    --keep-weekly 4 \
    --keep-monthly 6 \
    --prune

# 6. Cleanup temporary snapshot files
rm -rf "${SNAPSHOT_DIR:?}"/*
echo "[SUCCESS] Backup completed successfully at $(date -u)!"
