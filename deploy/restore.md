# Disaster Recovery & Restore Runbook

This runbook describes the procedure for restoring the file manager from Cloudflare R2 backup snapshots in the event of VPS disk loss, accidental deletion, or database corruption.

---

## 1. Prerequisites

- Access to the target VPS with Docker Compose installed.
- Restic credentials and encryption password:
  - `RESTIC_REPOSITORY`
  - `RESTIC_PASSWORD`
  - `R2_ACCESS_KEY_ID`
  - `R2_SECRET_ACCESS_KEY`

---

## 2. Step-by-Step Restoration Procedure

### Step 1: Stop the Running Web Application
To ensure zero write conflicts with SQLite and local storage:
```bash
docker compose stop app
```

### Step 2: List Available Snapshots
Run the restic tool to inspect available snapshots in your Cloudflare R2 bucket:
```bash
docker compose --profile ops run --rm backup snapshots
```

### Step 3: Execute Restore Script
To restore the latest snapshot:
```bash
docker compose --profile ops run --rm backup /scripts/restore.sh /data/restore-temp latest
```

### Step 4: Verify Restored Files
Verify SQLite database integrity before moving it to production:
```bash
sqlite3 /data/restore-temp/backup-snapshot/app.db "PRAGMA integrity_check;"
```

### Step 5: Replace Production Data
Move the verified files into place:
```bash
# Backup old database if present
mv /data/db/app.db /data/db/app.db.corrupt.$(date +%s) 2>/dev/null || true

# Copy restored database
cp /data/restore-temp/backup-snapshot/app.db /data/db/app.db

# Copy restored physical objects
mkdir -p /data/objects
cp -r /data/restore-temp/backup-snapshot/objects/* /data/objects/ 2>/dev/null || true

# Clean up restore temporary directory
rm -rf /data/restore-temp
```

### Step 6: CRITICAL — Increment `GLOBAL_SHARE_EPOCH`
In your `.env` file, increment `GLOBAL_SHARE_EPOCH`:
```env
GLOBAL_SHARE_EPOCH=2
```
> **Security Requirement:** If a public share link was revoked *after* the backup snapshot was taken, restoring the older database would inadvertently reactivate that revoked share. Incrementing `GLOBAL_SHARE_EPOCH` invalidates all pre-restore shares and active session cookies immediately.

### Step 7: Restart the Application
```bash
docker compose up -d app
docker compose logs -f app
```

Verify that health checks are passing:
```bash
docker compose exec app /app/filemgr healthcheck --url http://127.0.0.1:8080/health/ready
```
