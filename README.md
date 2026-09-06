# Self-Hosted File Manager (Docker Compose + Cloudflare Native)

Production-grade, lightweight, single-VPS file management system built as a Go modular monolith with an embedded React UI, SQLite WAL storage, tus resumable uploads, and Cloudflare Zero Trust authentication.

---

## Highlights

- **Lightweight Monolith:** Single Go binary with embedded React/Vite frontend (< 50MB RAM usage).
- **Crash-Safe 3-Phase Uploads:** Standard tus 1.0 protocol supporting multi-gigabyte files (up to 10 GiB), chunked at 16 MiB to stay safely beneath Cloudflare's 100 MB request limit.
- **SQLite Strict + WAL:** Strict typing, connection PRAGMAs, foreign key enforcement, and zero runtime dependencies (no external Postgres or Redis required).
- **Opaque Storage Layout:** Sharded physical objects (`/data/objects/xx/<key>`) decoupled from user filenames; zero path traversal vulnerabilities.
- **Dual-Host Security Routing:**
  - `files.yourdomain.com`: Admin interface protected by Cloudflare Access JWT verification with local JWKS caching and key rotation.
  - `share.yourdomain.com`: Public sharing surface strictly restricted to `/s/{token}` endpoints with 256-bit CSPRNG tokens, Argon2id passwords, and Global Share Epoch invalidation.
- **Zero-Trust Off-Site Backup:** Separate Restic container for encrypted snapshots to Cloudflare R2 without exposing storage keys to the web process.

---

## Quickstart (Development)

### 1. Run Backend Locally
```bash
# Copy development environment
cp .env.example .env

# Set DEV_AUTH_BYPASS=true in .env for local testing
go run ./cmd/server
```
The server will start at `http://localhost:8080`.

### 2. Run Frontend Dev Server (Vite)
```bash
cd web
npm install
npm run dev
```
Open `http://localhost:3000` in your browser. All API calls will proxy to `http://localhost:8080`.

---

## Production Deployment (Docker Compose)

### 1. Setup Configuration
```bash
cp .env.example .env
# Edit .env and configure your domain names, Cloudflare Access AUD, and pepper keys
```

### 2. Start Application
```bash
docker compose up -d --build app
```

### 3. Verification
```bash
# Check container logs
docker compose logs -f app

# Run container internal health check
docker compose exec app /app/filemgr healthcheck
```

---

## Off-Site Backup to Cloudflare R2

Run an on-demand encrypted backup to Cloudflare R2:
```bash
docker compose --profile ops run --rm backup
```

To schedule automated daily backups using host systemd:
```bash
sudo cp deploy/systemd/filemgr-backup.* /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now filemgr-backup.timer
```

See [deploy/restore.md](deploy/restore.md) for disaster recovery procedures and [deploy/cloudflare.md](deploy/cloudflare.md) for Cloudflare Tunnel & Zero Trust setup.
