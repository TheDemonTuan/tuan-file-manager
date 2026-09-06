#!/bin/sh
set -eu

echo "[INFO] Starting filemgr disaster recovery restore from Cloudflare R2..."

TARGET_DIR="${1:-/data/restore-temp}"
RESTORE_SNAPSHOT="${2:-latest}"

mkdir -p "${TARGET_DIR}"
rm -rf "${TARGET_DIR:?}"/*

echo "[INFO] Restoring snapshot '${RESTORE_SNAPSHOT}' to ${TARGET_DIR}..."
restic restore "${RESTORE_SNAPSHOT}" --target "${TARGET_DIR}"

echo "[INFO] Snapshot content restored to ${TARGET_DIR}."
echo "--------------------------------------------------------"
echo "ATTENTION REQUIRED FOR RECOVERY:"
echo "1. Verify database and objects in ${TARGET_DIR}."
echo "2. Copy ${TARGET_DIR}/backup-snapshot/app.db to /data/db/app.db."
echo "3. Copy ${TARGET_DIR}/backup-snapshot/objects/* to /data/objects/."
echo "4. CRITICAL: Increment GLOBAL_SHARE_EPOCH in your .env file to"
echo "   invalidate any public shares revoked after this snapshot was taken!"
echo "5. Restart filemgr application container: docker compose restart app"
echo "--------------------------------------------------------"
