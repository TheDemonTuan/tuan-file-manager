# Self-hosted File Manager — Production Implementation Plan V2.1 (Docker Compose + Cloudflare Native)

> Technical implementation specification for a lightweight, secure, single-VPS file management system behind Cloudflare Tunnel.
>
> Target: 1 administrator, VPS 2 vCPU / 2–4 GB RAM, local storage up to ~500 GB, Cloudflare Free ecosystem, Docker Compose deployment.

---

## 0. Executive decision

Build a **modular monolith** consisting of one Go application binary with an embedded React frontend. Use **SQLite + local filesystem** as the primary data plane. Keep Cloudflare R2 strictly as an encrypted off-site backup target, not as primary storage.

Do **not** introduce PostgreSQL, Redis, RabbitMQ, Kafka, Kubernetes, microservices, distributed locks, or multiple app replicas in V1.

The architecture optimizes for:

1. operational simplicity on one VPS;
2. predictable RAM consumption while transferring multi-GB files;
3. crash recovery and data integrity;
4. a small attack surface;
5. a clear migration path if the single-VPS model is eventually outgrown.

### Core stack

| Layer | Decision |
|---|---|
| Backend | Go stable supported release, `net/http`, stdlib-first |
| Frontend | React + TypeScript + Vite |
| Frontend delivery | Build static assets and embed into Go binary |
| Metadata DB | SQLite, WAL, foreign keys, migrations, one writer application |
| File storage | Local filesystem, opaque immutable object IDs |
| Resumable upload | tus protocol + `tus-js-client` |
| Reverse proxy | **None by default**; keep existing Caddy only if the VPS already requires it for unrelated/shared ingress features |
| Edge | Existing Cloudflare Tunnel + Cloudflare Access for admin hostname |
| Tunnel runtime | Prefer existing **remotely-managed `cloudflared` container** on a shared external Docker network |
| Backup | Dedicated backup container → Restic → private Cloudflare R2 bucket |
| Deployment | **Docker Compose is the required production deployment model** |
| Scheduling | Host systemd timer invokes short-lived Compose job containers; do not add a Docker-socket scheduler |
| Monitoring | structured logs + internal metrics + lightweight alerting |

---

# 1. Scope and non-goals

## 1.1 V1 features

- One administrator identity authorized by Cloudflare Access.
- Logical directory tree independent of physical filesystem paths.
- Create folder, rename, move, search and sort.
- Multi-file drag-and-drop upload.
- Upload queue with progress, pause, resume and retry.
- Resume after page reload, browser reconnect or application restart.
- Configurable maximum file size, default 10 GiB.
- Storage quota, upload reservation and physical free-space guard.
- Trash, restore and permanent purge.
- Normal download and HTTP Range resume.
- Read-only public file/folder sharing.
- Optional share password.
- Share expiration and immediate revoke.
- Select files for off-site backup.
- Backup status and restore verification.
- Audit log for sensitive actions.
- Internal health/readiness/metrics.
- Desktop and mobile responsive UI.

## 1.2 Explicit non-goals for V1

- multi-tenant accounts;
- RBAC/groups;
- WebDAV;
- desktop sync client;
- online file editing;
- media transcoding;
- thumbnails/previews;
- full-text file-content search;
- file version history;
- content deduplication;
- dynamic ZIP streaming of shared folders;
- antivirus scanning;
- R2 as primary object store;
- horizontally scaled application replicas.

These features should not influence the V1 architecture unless doing so costs almost nothing.

---

# 2. Architecture principles

## 2.1 Modular Monolith

Split code by business capability, not by technical layer alone.

Recommended modules:

```text
internal/
  auth/
  files/
  uploads/
  downloads/
  shares/
  trash/
  backup/
  audit/
  jobs/
  storage/
  db/
  httpapi/
```

Rules:

- no circular module imports;
- HTTP handlers do not directly issue SQL;
- domain/use-case code does not depend on HTTP;
- modules expose small public interfaces;
- infrastructure adapters remain replaceable without pretending the system is distributed.

## 2.2 Ports and Adapters / Hexagonal boundary

Use interfaces only at boundaries that have a meaningful alternate implementation or are useful for deterministic testing.

Recommended ports:

```go
type MetadataStore interface { ... }
type BlobStore interface { ... }
type Clock interface { ... }
type TokenGenerator interface { ... }
type AuditSink interface { ... }
```

Avoid interfaces for every struct. In Go, concrete types should remain the default unless polymorphism or isolation is useful.

## 2.3 Transaction Script / Application Service

Most operations are workflows rather than complex object graphs:

- rename node;
- move subtree;
- trash subtree root;
- restore;
- create share;
- revoke share;
- reserve upload;
- finalize upload.

Implement each as an explicit application service/use-case function with one short SQLite transaction where possible.

Example:

```text
MoveNode(actor, nodeID, targetParentID)
  -> validate system-node rules
  -> reject cycle
  -> verify target parent
  -> check naming conflict
  -> transaction update
  -> append audit event
```

## 2.4 State Machine

Use explicit state machines for workflows that survive process crashes.

### Upload state

```text
created
  ↓
receiving
  ↓
verifying
  ↓
finalizing
  ↓
complete

Any non-terminal state may transition to:
failed | cancelled | expired
```

Every transition must be validated and idempotent.

### Backup run state

```text
queued → snapshotting → uploading → verifying → complete
                         ↘ failed
```

## 2.5 Reconciliation instead of distributed queues

Use a periodic reconciliation loop to repair incomplete local workflows.

Good candidates:

- upload sessions stuck in `verifying`/`finalizing`;
- DB upload records with missing staging files;
- orphan staging files;
- orphan physical objects not referenced by DB;
- stale reservations;
- interrupted purge jobs;
- old backup staging snapshots.

This replaces the need for RabbitMQ/Kafka/outbox in a one-process deployment.

## 2.6 Idempotency

Mutation APIs where browser retry is plausible should accept an idempotency key.

Especially:

- create folder;
- create upload reservation;
- create share;
- trash/restore;
- permanent purge request.

Persist only keys necessary to avoid accidental duplicate effects, with TTL cleanup.

---

# 3. System topology

## 3.0 Deployment contract

Production is **Docker Compose first**. The application must not be deployed as a manually installed host binary. Runtime components, migrations, backup and restore helpers are all executed as containers declared by Compose.

The VPS already has Cloudflare Tunnel. Reuse that tunnel instead of opening a new public ingress. The preferred setup is a remotely-managed `cloudflared` container attached to a dedicated external Docker network such as `filemgr_edge`, shared only by the existing `cloudflared` connector and this application. The file-manager Compose project joins that same network only for the application service.

Hard requirements:

- no public `0.0.0.0:80`, `:443` or `:8080` mappings for the file manager;
- no Docker socket mounted into the application, backup or migration containers;
- no Nginx/Caddy/Traefik added solely because the app is in Docker;
- reuse the existing Cloudflare Tunnel and Cloudflare zone;
- Cloudflare remains the only Internet-facing edge;
- Docker service discovery is used between `cloudflared` and the app over a dedicated per-application edge network;
- production configuration lives in `compose.yaml`, `.env.example`, secrets files and runbooks, not undocumented shell commands.

If the current `cloudflared` is installed as a host systemd service rather than a container, keep that as a supported migration state only. Bind the app to `127.0.0.1:<high-port>` if necessary, never to all interfaces, then migrate the connector into Docker when convenient.

## 3.1 Preferred network path

```text
Browser
   │ HTTPS
   ▼
Cloudflare Edge
   │
   ├── files.example.com
   │      Cloudflare Access
   │
   └── share.example.com
          public, no Access
   │
   ▼
Existing Cloudflare Tunnel
   │
   ▼
cloudflared container
   │ external Docker network: filemgr_edge
   ▼
filemgr-app:8080
   │
   ├── /data/db/app.db
   ├── /data/objects
   ├── /data/staging
   └── /data/backup-snapshots
```

Both public hostnames may map through the same tunnel to `http://filemgr-app:8080`. The application still validates `Host` and exposes different route allowlists for admin and public-share traffic.

**Caddy is not part of the default topology.** Keep an existing shared Caddy only when it already provides a concrete VPS-wide function that Cloudflare Tunnel does not replace for this deployment.

The Go application must have **no public `ports:` mapping** in the preferred topology.

## 3.2 Host isolation

Use two independent hostnames.

### `files.example.com`

Allowed:

- admin SPA;
- `/api/v1/*` admin API.

Requirements:

- Cloudflare Access enforced;
- application independently validates Access JWT;
- Host validation;
- Origin/CSRF protection on state-changing requests.

### `share.example.com`

Allowed:

- public share SPA/assets;
- explicit `/s/*` routes only.

Never expose:

- admin API;
- upload API;
- detailed health endpoint;
- metrics;
- debug/profiling endpoints.

Prefer separate admin/public frontend entrypoints so the public hostname does not even ship the admin JS bundle.

---

# 4. Repository structure

```text
/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── auth/
│   ├── files/
│   ├── uploads/
│   ├── downloads/
│   ├── shares/
│   ├── trash/
│   ├── backup/
│   ├── audit/
│   ├── jobs/
│   ├── storage/
│   ├── db/
│   ├── observability/
│   └── httpapi/
├── web/
│   ├── admin/
│   ├── share/
│   └── shared/
├── api/
│   └── openapi.yaml
├── migrations/
├── deploy/
│   ├── compose.yaml
│   ├── compose.tunnel.example.yaml   # optional example for existing cloudflared stack
│   ├── Caddyfile.optional.example    # only if VPS already uses Caddy
│   ├── cloudflare.md                 # Tunnel routes, Access, WAF/cache rules
│   ├── systemd/
│   │   ├── filemgr-backup.service
│   │   └── filemgr-backup.timer
│   └── restore.md
├── scripts/
├── tests/
│   ├── integration/
│   ├── e2e/
│   └── chaos/
├── docs/
│   ├── adr/
│   ├── runbook.md
│   ├── security.md
│   └── disaster-recovery.md
├── Dockerfile
├── compose.yaml
└── README.md
```

---

# 5. Data model

Use SQLite `STRICT` tables when practical and explicit `CHECK` constraints for state enums and non-negative sizes.

## 5.1 `nodes`

```text
id                  TEXT/UUID/ULID PK
parent_id           nullable FK nodes.id
kind                file | folder
name                display name
name_key            normalized NFC + case-fold key
created_at
updated_at
trashed_at           nullable
restore_parent_id    nullable
is_system            boolean
```

Constraints:

- active sibling uniqueness on `(parent_id, name_key)`;
- system nodes cannot be renamed/moved/deleted;
- folder cannot be moved into itself or a descendant;
- client never supplies physical storage paths.

Use system roots:

```text
ROOT
TRASH_ROOT
```

## 5.2 `file_objects`

```text
node_id              PK/FK
storage_key          unique opaque ID
size_bytes           >= 0
mime_sniffed
sha256               nullable until finalized
backup_selected_at   nullable
created_at
updated_at
```

Objects are immutable after upload completion.

Replacing file contents in a future version should create a new object, not mutate an existing object in place.

## 5.3 `uploads`

```text
id
owner_subject
parent_id
name
name_key
expected_size
received_bytes
reservation_bytes
staging_key
final_storage_key
state
sha256
last_error
created_at
updated_at
expires_at
```

`final_storage_key` is allocated before the filesystem finalization phase so reconciliation always knows the intended destination.

## 5.4 `shares`

```text
id
target_node_id
token_hash
password_hash         nullable
expires_at            nullable
revoked_at            nullable
auth_version          integer
global_share_epoch    captured value
created_at
updated_at
```

Never persist plaintext share tokens after creation.

## 5.5 `audit_events`

```text
id
created_at
request_id
actor_subject_hash
action
object_type
object_id
outcome
normalized_ip
metadata_json         allowlisted fields only
```

Do not log:

- Access JWT;
- share plaintext token;
- password;
- sensitive URL query string;
- full physical path.

## 5.6 `jobs` / `backup_runs`

Store job status, attempt count, timestamps and latest error. A single-process lease is sufficient.

---

# 6. Filesystem design

## 6.1 Layout

```text
/data/
├── db/
│   └── app.db
├── objects/
│   ├── 00/
│   ├── 01/
│   └── ...
├── staging/
│   └── uploads/
├── backup-snapshot/
├── trash-work/
└── runtime/
```

Physical object path:

```text
/data/objects/<first-2-chars>/<opaque-storage-key>
```

Never include a user filename in a physical path.

## 6.2 Security invariants

- do not follow symlinks;
- only regular files are accepted where an object is expected;
- keep data outside web root;
- data mount should be `noexec` where feasible;
- application container runs non-root;
- only `/data` is writable;
- root filesystem is read-only;
- `cap_drop: ALL`;
- `no-new-privileges:true`;
- small tmpfs `/tmp`.

## 6.3 Immutability

Completed objects are immutable. Rename/move operates purely on metadata.

Benefits:

- simpler crash recovery;
- stable download checksum/ETag;
- hardlink-based backup snapshot;
- easier integrity verification;
- fewer races between backup/download/purge.

---

# 7. SQLite production contract

## 7.1 Filesystem requirement

`/data/db` must be on a local filesystem attached to the VPS.

Do not place the WAL database on:

- NFS;
- SMB/CIFS;
- network filesystem;
- remote FUSE mount whose fsync/locking semantics are not explicitly proven safe.

SQLite WAL uses shared-memory coordination and is not intended for clients on different machines over a network filesystem.

## 7.2 Connection configuration

Every database connection must receive required connection-level settings, not merely the first startup connection.

At minimum validate:

```sql
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = <configured>;
```

At database initialization:

```sql
PRAGMA journal_mode = WAL;
```

Benchmark and explicitly choose:

```text
synchronous = FULL
or
synchronous = NORMAL
```

Start with `FULL` for conservative durability. Change only after crash/power-loss testing on the real VPS filesystem.

## 7.3 Pool sizing

SQLite is not helped by a large write pool.

Start with:

- small total connection pool;
- one logical application writer path;
- short write transactions;
- streaming file I/O outside transactions.

Benchmark metadata concurrency before increasing connections.

## 7.4 WAL operations

Monitor:

- WAL file size;
- checkpoint latency;
- busy errors;
- long-running readers.

Do not run arbitrary aggressive checkpoint commands on every request.

## 7.5 Migration policy

- versioned migrations;
- database snapshot before production migration;
- migration command runs separately before new application container is switched live;
- irreversible migrations require a documented restore rollback path;
- no automatic destructive migration on normal server startup.

---

# 8. Directory-tree consistency

## 8.1 Naming

Store both:

- `name`: original display representation;
- `name_key`: Unicode NFC normalized + application-defined case fold.

Do not delegate user-visible uniqueness entirely to OS filesystem semantics.

## 8.2 Moving folders

Before a move:

1. node exists and is active;
2. target parent exists and is active folder;
3. target parent is not node itself;
4. target parent is not a descendant of node;
5. target sibling name does not conflict;
6. node is not a system node.

Use a recursive CTE or maintained ancestry strategy. For the expected scale, recursive CTE is preferred before adding closure tables/materialized paths.

## 8.3 Trash semantics

Trash a subtree by moving its root to `TRASH_ROOT` and preserving children.

Important rule: **active search/listing must be rooted from active ROOT**, not merely `WHERE trashed_at IS NULL`, otherwise descendants of a trashed folder can leak back into active search results.

Restore behavior:

1. restore to `restore_parent_id` only if it still exists and is active;
2. otherwise restore to ROOT;
3. if name conflict exists, apply deterministic suffix or return an explicit user-visible conflict flow;
4. never restore into trash or a deleted descendant.

## 8.4 Purge

Permanent deletion is asynchronous/idempotent from a business perspective:

```text
requested → deleting_objects → deleting_metadata → complete
```

A failed purge can resume safely.

Do not hold one huge SQLite transaction while recursively deleting physical files.

---

# 9. Upload architecture

## 9.1 Protocol

Use tus 1.0 compatible behavior.

Required/expected methods:

- `OPTIONS` capability discovery;
- `POST` creation;
- `HEAD` offset recovery;
- `PATCH` bytes transfer;
- `DELETE` if termination extension is enabled.

The client must use the server-reported offset after reconnect/reload and never assume the previous PATCH was committed.

## 9.2 Chunk size

Make chunk size configurable.

Suggested initial values:

```text
TUS_CHUNK_SIZE=16MiB
MAX_FILE_SIZE=10GiB
MAX_PARALLEL_UPLOADS=2
```

Benchmark 8/16/32 MiB on the actual VPS.

Cloudflare Free currently accepts request bodies up to 100 MB, therefore any configured tus PATCH must remain comfortably below that ceiling.

## 9.3 Reservation

Before upload creation:

1. validate target parent;
2. validate filename;
3. validate max file size;
4. detect name conflict;
5. check logical quota;
6. check physical free-space reserve;
7. atomically reserve `expected_size`.

Reservations protect concurrent uploads from each independently observing enough free space.

## 9.4 Physical-space guard

Logical metadata alone is insufficient.

Calculate/observe:

- completed objects;
- staging uploads;
- trash/purge workspace;
- SQLite DB + WAL;
- backup snapshot temporary space;
- filesystem free bytes.

Suggested policy:

```text
warn at 80%
block new uploads at 90%
or earlier if MIN_FREE_BYTES would be crossed
```

Downloads and cleanup operations remain available while new uploads are blocked.

## 9.5 Upload state flow

```text
POST reservation
  ↓
created
  ↓ first PATCH
receiving
  ↓ received == expected
verifying
  ↓ checksum/policy OK
finalizing
  ↓ durable object + metadata
complete
```

Client treats only server-reported `complete` as a successfully created file.

## 9.6 Integrity policy

V1 should require `Upload-Length`; do not implement deferred upload length initially.

When all bytes have arrived:

1. flush/sync staging file;
2. stream file to calculate SHA-256;
3. sniff MIME/magic bytes;
4. validate configured file policy;
5. transition to finalizing.

The tus checksum extension may be supported for per-PATCH integrity later, but whole-file SHA-256 is still useful for object integrity, ETag, backup validation and restore drills.

## 9.7 Crash-safe finalization

Recommended sequence:

### Phase A — durable intent

SQLite transaction:

- ensure upload state = `verifying`;
- allocate/persist `final_storage_key`;
- persist checksum and verified size;
- set `state=finalizing`;
- commit.

### Phase B — filesystem commit

- `fsync` staging file;
- create destination shard directory if necessary;
- atomic rename staging file → final object path on same filesystem;
- sync destination directory as required by platform durability strategy.

### Phase C — metadata commit

SQLite transaction:

- idempotently create node;
- create file object record;
- release reservation;
- mark upload `complete`;
- append audit metadata / job signal as appropriate;
- commit.

### Reconciler cases

If `state=finalizing`:

- final object exists + metadata missing → finish metadata transaction;
- staging exists + final object missing → retry rename;
- both exist → verify expected key/size/checksum, resolve deterministically;
- neither exists → mark failed and release reservation.

Never create duplicate logical files on retry.

---

# 10. Download architecture

## 10.1 Streaming

Go streams directly from filesystem.

Never:

- `os.ReadFile()` a large file;
- buffer complete object into RAM;
- proxy complete file through another in-memory layer.

## 10.2 HTTP behavior

Support:

- `Range`;
- `206 Partial Content`;
- `ETag`;
- `If-Range`;
- `Content-Length` where known;
- safe `Content-Disposition: attachment`;
- `X-Content-Type-Options: nosniff`.

Strong ETag may derive from stored SHA-256 if semantics remain stable.

## 10.3 Concurrency limits

Use process-local semaphores:

```text
MAX_ACTIVE_UPLOADS
MAX_ACTIVE_DOWNLOADS
MAX_PUBLIC_DOWNLOADS
MAX_PUBLIC_DOWNLOADS_PER_SHARE
```

Return controlled overload responses rather than allowing disk/network saturation to destroy metadata API latency.

## 10.4 Timeout strategy

Do not apply one small global `WriteTimeout` to all routes.

Use:

- short `ReadHeaderTimeout`;
- bounded header size;
- normal API request deadlines;
- dedicated streaming behavior for upload/download;
- context cancellation on client disconnect;
- graceful shutdown with a bounded drain period.

---

# 11. Public share design

## 11.1 Token generation

Generate at least 256 bits from a CSPRNG.

URL example:

```text
https://share.example.com/s/<opaque-token>
```

Store only:

```text
HMAC-SHA256(server_pepper, token)
```

or a cryptographically appropriate token hash design.

Return plaintext token exactly once during creation.

## 11.2 Password protection

Optional password:

- Argon2id;
- parameters benchmarked against the VPS RAM/CPU;
- rate limit by share + normalized source IP;
- escalating delay/jitter;
- temporary lockout threshold.

Successful password verification creates a signed server session cookie containing:

```text
share_id
auth_version
global_share_epoch
expiry
```

Cookie:

```text
Secure
HttpOnly
SameSite=Lax
```

## 11.3 Revoke semantics

Every public metadata/download request must re-check share state in SQLite.

A revoke must affect the next request.

Incrementing `auth_version` invalidates prior password sessions.

## 11.4 Disaster-restore share invalidation

A restored database snapshot can accidentally resurrect a share that was revoked after that snapshot.

Therefore disaster recovery must include one of:

### Preferred

Increment/replace a deployment-level `GLOBAL_SHARE_EPOCH` after DR restore so all old public share sessions and optionally all pre-restore shares become invalid until explicitly re-enabled.

### Alternative

Rotate the share-token pepper/signing key and mark all shares revoked after restore.

Document this as a mandatory recovery step, not an optional operational note.

## 11.5 Public response privacy

For `/s/*` pages and metadata:

```text
Cache-Control: private, no-store
Referrer-Policy: no-referrer
X-Robots-Tag: noindex, nofollow
```

Configure Cloudflare rules so share pages are never unintentionally cached.

Do not install analytics that capture share token URLs.

---

# 12. Cloudflare Access security

Cloudflare Access is the first security boundary, not the only one.

For `files.example.com`, application middleware validates `Cf-Access-Jwt-Assertion`:

- signature against Cloudflare Access JWKS;
- issuer;
- audience;
- expiration;
- not-before where applicable;
- allowed identity subject/email policy.

Cache JWKS but support signing-key rotation and refresh when an unknown `kid` appears.

Do not trust a plain email header injected by a client.

The app also validates expected Host and the proxy trust boundary before trusting `CF-Connecting-IP`.

For state-changing admin requests:

- validate Origin;
- use CSRF strategy compatible with chosen auth flow;
- require appropriate content type;
- reject cross-host API use.

---

# 13. Security headers and uploaded content

Default security policy:

- strict CSP per frontend;
- HSTS at appropriate edge/origin layer;
- `X-Content-Type-Options: nosniff`;
- `frame-ancestors 'none'` unless explicitly needed;
- restrictive Permissions-Policy;
- `Referrer-Policy: no-referrer` on public share surface.

Uploaded user files are downloaded as attachments.

Do not render uploaded HTML/SVG inline in V1.

Do not:

- execute uploads;
- extract archives automatically;
- call a shell with a user filename;
- trust client-provided MIME type;
- concatenate a user path into an OS path.

---

# 14. API contract

Use OpenAPI as the API contract and generate client types where useful.

Errors use RFC-style Problem Details (`application/problem+json`).

## 14.1 Admin API

```text
GET    /api/v1/me

GET    /api/v1/nodes
POST   /api/v1/folders
PATCH  /api/v1/nodes/{id}
POST   /api/v1/nodes/{id}/move
POST   /api/v1/nodes/{id}/trash

GET    /api/v1/trash
POST   /api/v1/trash/{id}/restore
DELETE /api/v1/trash/{id}

GET    /api/v1/files/{id}/download
PUT    /api/v1/files/{id}/backup-selection

POST   /api/v1/uploads
GET    /api/v1/uploads/{id}
DELETE /api/v1/uploads/{id}

OPTIONS /api/v1/uploads/tus
POST    /api/v1/uploads/tus
HEAD    /api/v1/uploads/tus/{id}
PATCH   /api/v1/uploads/tus/{id}
DELETE  /api/v1/uploads/tus/{id}

GET    /api/v1/shares
POST   /api/v1/shares
PATCH  /api/v1/shares/{id}
DELETE /api/v1/shares/{id}

GET    /api/v1/system/storage
GET    /api/v1/system/backup
```

## 14.2 Public API

Allowlist only:

```text
GET  /s/{token}
POST /s/{token}/unlock
GET  /s/{token}/nodes
GET  /s/{token}/files/{node_id}/download
```

All other share-host API paths return 404.

## 14.3 Pagination

Use keyset/cursor pagination, not large OFFSET pagination.

Cursor should include a deterministic pair such as:

```text
(sort_key, id)
```

Always add stable ID tie-breaker.

---

# 15. Search and scale target

Capacity should be expressed in both bytes and object count.

Initial qualification targets:

```text
Storage:       <= 500 GB
Single file:   <= 10 GiB default
Nodes tested:  500,000
Stretch test:  1,000,000 nodes
Admin users:   1
App replicas:  1
```

500 GB of small files can create far more DB/UI pressure than 500 GB of large video files, so node count is a required performance metric.

Start with SQLite indexes on actual query patterns, likely including:

- `(parent_id, name_key)`;
- active/root traversal helpers;
- search key;
- upload state + expiry;
- shares by token hash;
- jobs by state/next-run.

Avoid premature FTS until filename search genuinely needs it.

---

# 16. Backup architecture

## 16.1 Separation of credentials

The HTTP application should **not possess R2 credentials or Restic repository password** if avoidable.

Preferred topology:

```text
Go web app
   │ writes local backup intent/status
   ▼
SQLite

systemd timer / backup service
   │ owns Restic + R2 credentials
   ▼
local snapshot → Restic → R2
```

This materially reduces the blast radius of a web-app compromise.

## 16.2 Stable backup snapshot

Do not have Restic walk live selected object paths directly from a DB manifest while purge can run concurrently.

Because completed objects are immutable and live on one filesystem, create a backup snapshot directory using hardlinks:

```text
/data/backup-snapshot/<run-id>/
  db/app.db
  manifest.json
  objects/...
  config/...
```

Workflow:

1. create consistent SQLite online backup into snapshot directory;
2. read list of selected immutable file objects;
3. create hardlinks from each live object into snapshot tree;
4. persist manifest including storage key, logical ID, size and SHA-256;
5. Restic backs up the snapshot tree;
6. verify Restic snapshot exists;
7. delete local hardlink snapshot after success or TTL.

If the original object is purged while Restic is running, the hardlink retains the underlying inode until backup completes.

Fallback if hardlinks are not supported: coordinated copy/snapshot strategy with explicit space budgeting.

## 16.3 R2 repository

- private bucket;
- public access disabled;
- least-privilege API token;
- Restic client-side encryption;
- password stored separately from VPS backup data;
- repository never exposed through public application routes.

Cloudflare R2's current Standard free tier includes 10 GB-month/month of storage, so treat the proposed backup budget as a **soft budget**, not a storage cap.

Suggested policy:

```text
selected user objects: ~7 GiB target
DB/config/history:      ~1 GiB budget
headroom:               ~2 GiB
```

Actual GB-month usage must be monitored.

## 16.4 Retention

Starting point:

```text
7 daily
4 weekly
```

Run `forget --prune` according to Restic guidance and actual repository growth.

Do not assume media files compress meaningfully.

## 16.5 Verification

Daily:

- verify recent Restic snapshot exists;
- alert if latest successful backup is stale (> ~26h for nightly cadence).

Weekly:

- repository check/read-data subset.

Monthly:

- restore DB + at least one selected object to temporary location;
- compare SHA-256;
- delete test restore after verification.

A backup is not production-ready until restore has been tested.

## 16.6 R2 Bucket Lock

Cloudflare R2 supports bucket-lock retention rules preventing deletion/overwrite for matching objects/prefixes.

Do **not** blindly apply a long immutable lock to the entire live Restic repository because repository maintenance/pruning may require changes.

Safer optional design:

- separate recovery bucket or prefix for periodic immutable DR artifacts;
- lock those recovery artifacts for 7–30 days;
- keep regular Restic repository operational and pruneable.

Test the exact retention behavior before enabling it in production.

---

# 17. Disaster recovery

`docs/disaster-recovery.md` must contain an executable procedure.

## 17.1 Recovery sequence

1. provision clean VPS;
2. install Docker/cloudflared and baseline firewall;
3. restore application configuration from approved backup;
4. restore SQLite snapshot;
5. restore object files;
6. verify ownership/mode;
7. start application isolated from public routing;
8. run DB integrity/migration checks;
9. run reconciler in recovery mode;
10. verify random file SHA-256;
11. increment `GLOBAL_SHARE_EPOCH` / invalidate previous share sessions;
12. smoke-test admin upload/download/trash/share flows;
13. restore Cloudflare routing only after checks pass.

## 17.2 Recovery objectives

Document realistic targets rather than fake guarantees.

Initial example:

```text
RPO: <= 24h for data selected for nightly R2 backup
RTO: best effort single-admin VPS rebuild target
```

Files not selected for backup have no off-VPS durability guarantee and the UI should communicate this clearly.

## 17.3 3-2-1 roadmap

V1 provides two copies for selected data:

- VPS;
- R2.

For irreplaceable data, add a third independent copy later on a different provider or physical device.

---

# 18. Background jobs

Keep local jobs simple.

Suggested schedules:

| Job | Cadence | Purpose |
|---|---:|---|
| Upload reconcile | startup + every few minutes | recover interrupted finalization |
| Stale reservation cleanup | every 10–30 min | release abandoned quota |
| Staging cleanup | hourly | remove expired files |
| Trash purge | daily | retention policy |
| Backup | nightly | R2 backup |
| Backup verify | daily/weekly | stale/check monitoring |
| Restore drill | monthly | prove recoverability |
| WAL/storage observation | periodic | health metrics |

Jobs must be idempotent.

Use a DB lease only to prevent duplicate local worker execution; do not invent a distributed lease system.

---

# 19. Observability

## 19.1 Logging

Go `slog` JSON fields:

```text
timestamp
level
request_id
route_template
method
status
latency_ms
bytes_in
bytes_out
actor_subject_hash
client_ip_normalized
error_code
```

Redact:

- share token;
- password;
- JWT;
- sensitive query values;
- raw uploaded filename when logging it has no operational value.

## 19.2 Metrics

Private metrics:

```text
HTTP request rate/error/latency by route template
active uploads
active downloads
upload bytes
upload failures
SQLite busy count
database latency
WAL size
disk total/free
staging bytes
trash-work bytes
share password failures
backup last success age
backup failures
reconcile repair counts
process RSS
```

Avoid high-cardinality labels such as filename, token, node ID or full URL.

## 19.3 Alerts

Minimum alerts:

- disk >80%;
- uploads blocked / disk >90%;
- backup stale/failure;
- repeated application restart;
- Cloudflare Tunnel unavailable;
- abnormal HTTP 5xx rate;
- persistent SQLite busy errors;
- reconcile repeatedly failing same object.

A full Prometheus/Grafana stack is optional. For a small VPS, a lightweight health/metric endpoint plus external webhook/email alerting is acceptable.

---

# 20. Docker Compose production design

Docker Compose is not merely an example deployment method; it is part of the production architecture.

## 20.1 Compose services

Recommended logical services:

| Service | Lifetime | Purpose | Internet-facing |
|---|---|---|---|
| `filemgr-app` | long-running | Go API + embedded React UI | No public port; reachable only from tunnel network |
| `filemgr-migrate` | one-shot/profile `ops` | database migrations | No |
| `filemgr-backup` | one-shot/profile `ops` | snapshot preparation + Restic upload to R2 | outbound only |
| `filemgr-restore` | one-shot/profile `ops` | controlled restore tooling | No |
| `cloudflared` | already existing/shared | Cloudflare Tunnel connector | outbound connection to Cloudflare only |

Do **not** add Redis, PostgreSQL, a queue, Portainer agent, Watchtower, Docker-socket proxy, cron container or a monitoring stack merely to support this application.

## 20.2 Preferred Compose topology

The file-manager project should declare the existing tunnel network as external:

```yaml
name: filemgr

services:
  app:
    image: ${FILEMGR_IMAGE}
    restart: unless-stopped
    init: true
    user: "10001:10001"
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp:size=64m,mode=1777
    volumes:
      - type: bind
        source: ${FILEMGR_DATA_DIR:-/srv/filemgr/data}
        target: /data
    expose:
      - "8080"
    networks:
      filemgr_edge:
        aliases:
          - filemgr-app
    healthcheck:
      test: ["CMD", "/app/filemgr", "healthcheck", "--url", "http://127.0.0.1:8080/health/live"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s
    stop_grace_period: 60s
    pids_limit: 256
    environment:
      APP_LISTEN_ADDR: ":8080"
      APP_DATA_DIR: "/data"
      APP_ADMIN_HOST: "${FILEMGR_ADMIN_HOST}"
      APP_SHARE_HOST: "${FILEMGR_SHARE_HOST}"
      CF_ACCESS_TEAM_DOMAIN: "${CF_ACCESS_TEAM_DOMAIN}"
      CF_ACCESS_AUD: "${CF_ACCESS_AUD}"

  migrate:
    image: ${FILEMGR_IMAGE}
    profiles: ["ops"]
    user: "10001:10001"
    read_only: true
    cap_drop: ["ALL"]
    security_opt:
      - no-new-privileges:true
    network_mode: "none"
    volumes:
      - type: bind
        source: ${FILEMGR_DATA_DIR:-/srv/filemgr/data}
        target: /data
    command: ["/app/filemgr", "migrate", "up"]

  backup:
    image: ${FILEMGR_BACKUP_IMAGE}
    profiles: ["ops"]
    user: "10001:10001"
    read_only: true
    cap_drop: ["ALL"]
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp:size=128m,mode=1777
    volumes:
      - type: bind
        source: ${FILEMGR_DATA_DIR:-/srv/filemgr/data}
        target: /data
    secrets:
      - r2_access_key_id
      - r2_secret_access_key
      - restic_password
    command: ["/app/filemgr-backup", "run"]

networks:
  filemgr_edge:
    external: true
    name: ${CF_TUNNEL_DOCKER_NETWORK:-filemgr_edge}

secrets:
  r2_access_key_id:
    file: ${SECRETS_DIR:-./secrets}/r2_access_key_id
  r2_secret_access_key:
    file: ${SECRETS_DIR:-./secrets}/r2_secret_access_key
  restic_password:
    file: ${SECRETS_DIR:-./secrets}/restic_password
```

This is a design skeleton, not a copy-paste final file. The final Compose must be tested against the actual Docker Compose version installed on the VPS.

## 20.3 Existing `cloudflared` container

Prefer a **remotely-managed tunnel**, because Cloudflare currently recommends that model for Docker deployments. Reuse the existing tunnel token/connector rather than creating a tunnel per application.

The shared infrastructure Compose can look conceptually like. If this `cloudflared` already serves other applications, attach it to one dedicated external network per application rather than placing every application on one broad shared network:

```yaml
services:
  cloudflared:
    image: cloudflare/cloudflared:<pinned-version-or-digest>
    restart: unless-stopped
    command: tunnel --no-autoupdate run
    environment:
      TUNNEL_TOKEN_FILE: /run/secrets/cloudflare_tunnel_token
    secrets:
      - cloudflare_tunnel_token
    networks:
      - filemgr_edge

networks:
  filemgr_edge:
    external: true
    name: filemgr_edge
```

`TUNNEL_TOKEN_FILE` is supported by `cloudflared` 2025.4.0 and later. Pin a supported release/digest and mount the token as a Compose secret. Never commit the tunnel token to `compose.yaml` or `.env`.

The tunnel should publish:

```text
files.example.com  -> http://filemgr-app:8080
share.example.com  -> http://filemgr-app:8080
```

A single Cloudflare Tunnel can publish multiple applications/hostnames.

## 20.4 Why no Caddy by default

Cloudflare Tunnel already provides the external routing path to the service. The application itself can terminate plain HTTP on the private Docker network because that hop never leaves the VPS Docker bridge.

Do not introduce Caddy only to perform:

- TLS termination already handled at Cloudflare Edge;
- hostname routing already handled by Tunnel + app host router;
- static file serving already handled by embedded Go assets;
- request buffering that harms streaming uploads/downloads.

Keep Caddy only if it is an existing shared ingress with a real independent purpose.

## 20.5 Backup image design

Do not put Restic or R2 credentials in the long-running web image.

Build two runtime targets from the repository:

```text
filemgr-runtime
  Go binary only

filemgr-backup-runtime
  backup CLI/helper + Restic
```

`filemgr-backup` receives R2 credentials and the Restic password. `filemgr-app` does not.

Schedule backups from a host systemd timer using:

```text
docker compose --profile ops run --rm backup
```

The job itself remains containerized while the scheduler holds no Docker socket inside another container. This is safer and simpler than running Ofelia/Watchtower-like privileged schedulers.

## 20.6 Migration and maintenance jobs

Run migrations as an explicit one-shot Compose job before starting the new application image:

```text
docker compose --profile ops run --rm migrate
```

Maintenance/reconcile commands should follow the same pattern.

No migration should execute implicitly in `app` startup in production, because rollback becomes less predictable.

## 20.7 Container hardening rules

For every applicable runtime container:

- explicit non-root UID/GID;
- `read_only: true`;
- `cap_drop: ALL`;
- `no-new-privileges`;
- bounded tmpfs;
- bounded process count;
- no Docker socket;
- no privileged mode;
- no host PID/IPC namespace;
- only required bind mounts;
- no application source checkout mounted into production;
- immutable/pinned images in production;
- SBOM + CVE scan in CI;
- secrets mounted at runtime, never baked into an image;
- `/srv/filemgr/data` should be on a local filesystem and mounted `noexec` at host level when practical.

## 20.8 Host firewall contract

Because Cloudflare Tunnel is outbound-only, the ideal host firewall exposes only what VPS administration actually requires, typically SSH restricted to trusted sources/VPN/WARP if used.

The file-manager application itself requires no inbound public TCP port.

## 20.9 Cloudflare ecosystem usage

Use the Cloudflare services already available instead of recreating them inside the VPS:

- **Cloudflare Tunnel** — sole application ingress;
- **Cloudflare Access** — admin authentication gate for `files.example.com`;
- **WAF/security rules** — coarse edge filtering and abuse controls where available;
- **Turnstile** — optional escalation for suspicious public share password-unlock attempts, not mandatory on every download;
- **Cache Rules** — explicitly bypass cache for `/s/*`, admin API and authenticated responses;
- **R2** — encrypted off-site backup target;
- **Cloudflare dashboard notifications/tunnel health** — supplement origin alerts where available.

Do not make core authorization or quota correctness depend solely on Cloudflare features. The Go application remains authoritative for share scope, expiry, revoke, upload quota and file access.

---

# 21. Deployment strategy

Single-VPS deployment does not justify pretending to have zero-downtime distributed deployment.

Optimize for reliable rollback.

## 21.1 Release flow

1. validate free disk;
2. take consistent DB backup;
3. confirm most recent off-site backup state;
4. pull immutable image;
5. run migration command;
6. start new app container isolated/not switched if topology allows;
7. readiness test;
8. switch proxy/tunnel destination;
9. smoke test admin + share + upload + Range download;
10. keep previous image reference and DB snapshot for rollback.

## 21.2 Graceful shutdown

On SIGTERM:

1. readiness becomes false;
2. reject new upload creation;
3. stop accepting new normal requests;
4. allow bounded grace for active streams/finalization;
5. persist recoverable state;
6. exit;
7. reconciler repairs anything interrupted after restart.

Do not attempt to wait indefinitely for a 10 GiB download to finish during a deploy.

---

# 22. Testing strategy

## 22.1 Unit tests

Test pure domain logic:

- name normalization;
- conflicts;
- state transitions;
- token hashing;
- share expiry/revoke;
- quota/reservation arithmetic;
- Range parser;
- cursor encode/decode;
- cycle prevention.

## 22.2 Integration tests

Use real SQLite + temporary filesystem.

Required:

- migrations;
- concurrent rename/move conflict;
- concurrent reservations;
- stale reservation;
- disk-full simulation;
- upload offset conflict;
- crash at each finalization boundary;
- reconciler recovery;
- trash subtree search isolation;
- purge retry;
- share revoke on next request;
- Range download checksum;
- hardlink backup snapshot while source purge occurs.

## 22.3 Security tests

- invalid/missing Access JWT;
- wrong issuer/audience;
- expired token;
- unknown signing key refresh path;
- forged identity header;
- Host confusion;
- Origin/CSRF failure;
- traversal attempts;
- Unicode separator/path tricks;
- symlink target;
- MIME spoofing;
- oversized upload chunk;
- brute-force password route;
- share token absent from logs/referrer;
- admin API unreachable from share host;
- direct origin bypass attempt.

## 22.4 E2E Playwright

Desktop + mobile:

- login;
- create folder;
- upload multiple files;
- pause/resume;
- reload browser during upload;
- reconnect after simulated network interruption;
- search/sort;
- rename/move;
- trash/restore;
- create password share;
- open share;
- wrong password;
- revoke share;
- expired share;
- Range download.

Accessibility:

- keyboard navigation;
- focus management;
- screen-reader labels;
- upload progress announcements;
- touch targets;
- queue state survives navigation.

## 22.5 Chaos tests

Inject crashes:

```text
before fsync staging
after checksum
before state=finalizing commit
after finalizing DB commit
before rename
after rename
before directory sync
after rename before metadata commit
after metadata commit before client response
```

Restart the application and assert exactly one valid terminal result.

---

# 23. Performance qualification

These are benchmark targets, not promises.

Initial target on actual production-class VPS:

```text
idle application RSS:       < ~150 MiB target
parallel uploads:           2
parallel downloads:         4
metadata API p95:           < ~300 ms during stream load
single file:                10 GiB without RAM scaling with file size
qualified node count:       500k
stretch DB test:            1M nodes
```

Verify:

- memory stays roughly constant during large transfers;
- checksum processing is CPU/disk acceptable;
- metadata APIs remain responsive during transfer load;
- WAL does not grow without bound;
- backup snapshot does not unexpectedly double disk use because hardlinks are used.

---

# 24. PR / milestone plan

Split risky subsystems into smaller changes than the original plan.

## PR 1 — Bootstrap & ADRs

Deliver:

- Go server skeleton;
- React/Vite setup;
- embedded static placeholder;
- config validation;
- `slog` JSON logging;
- OpenAPI skeleton;
- Docker multi-stage image;
- CI lint/test/build;
- initial ADRs.

Done when:

- one non-root container runs;
- health endpoint works internally;
- CI green.

## PR 2 — SQLite foundation & node model

Deliver:

- migrations;
- connection initializer applying required PRAGMAs per connection;
- nodes/file_objects schema;
- storage-key abstraction;
- create/list/rename;
- deterministic errors.

Done when:

- no API accepts OS paths;
- integration tests use real SQLite/temp filesystem.

## PR 3 — Tree move/search/trash invariants

Deliver:

- move with cycle protection;
- rooted active search;
- keyset pagination;
- trash/restore;
- system node invariants.

Done when:

- descendants of trashed folders never leak into active search;
- concurrency conflicts deterministic.

## PR 4 — Cloudflare Access & host boundary

Deliver:

- Access JWT verifier;
- JWKS cache/rotation handling;
- host router;
- Origin/CSRF policy;
- proxy trust configuration;
- admin/share hostname tests.

Done when:

- forged headers fail;
- share host cannot invoke admin mutations;
- origin bypass test fails closed.

## PR 5A — Upload backend

Deliver:

- reservation;
- tus creation/HEAD/PATCH/termination;
- state machine;
- staging;
- configurable chunk/concurrency limits;
- free-space policy.

Done when:

- interrupted upload resumes at exact offset;
- quota remains correct under concurrency.

## PR 5B — Verify/finalize/reconcile

Deliver:

- whole-file SHA-256;
- MIME/magic policy;
- persisted final storage key;
- durable rename sequence;
- startup/periodic reconciler;
- crash injection tests.

Done when:

- every injected crash boundary converges to one deterministic outcome;
- no duplicate logical file appears.

## PR 5C — Upload UI

Deliver:

- drag/drop;
- queue;
- progress;
- pause/resume;
- retry;
- persistence across reload;
- error states.

Done when Playwright proves reload/network-loss recovery.

## PR 6 — File-manager UI

Deliver:

- breadcrumbs;
- folder table/grid;
- selection;
- search/sort/cursor pagination;
- rename/move;
- trash/restore;
- desktop/mobile.

## PR 7A — Download/Range

Deliver:

- streaming download;
- Range;
- ETag/If-Range;
- concurrency semaphore;
- cancellation;
- safe headers.

Done when 10 GiB-style stream test does not scale RAM with file size.

## PR 7B — Public shares

Deliver:

- 256-bit tokens;
- hashed token storage;
- password sessions;
- Argon2id;
- rate limits;
- expiry/revoke;
- global share epoch;
- public frontend;
- no-store/noindex policy.

Done when revoke applies to next request and old password session is invalidated.

## PR 8A — Backup snapshot layer

Deliver:

- SQLite online backup;
- selected-object manifest;
- hardlink snapshot;
- backup status model;
- cleanup TTL.

Done when purge during a snapshot cannot invalidate the snapshot contents.

## PR 8B — Restic/R2 & restore drill

Deliver:

- separate backup runtime credentials;
- systemd timer or isolated Compose backup profile;
- retention;
- checks;
- monthly restore drill;
- alerting;
- optional immutable DR prefix design.

Done when clean-directory restore passes SHA-256 verification.

## PR 9 — Observability & operational hardening

Deliver:

- private metrics;
- alerts;
- container hardening;
- timeouts;
- graceful shutdown;
- cleanup jobs;
- load tests.

## PR 10 — Production qualification

Deliver:

- security regression;
- chaos suite;
- browser regression;
- 500k-node benchmark;
- backup restore drill;
- deployment rollback drill;
- disaster-recovery rehearsal.

Production release requires this milestone.

---

# 25. ADRs to write before/while coding

Create short Architecture Decision Records rather than burying decisions in code comments.

Recommended ADRs:

```text
ADR-001 modular monolith over microservices
ADR-002 SQLite over PostgreSQL for single-node metadata
ADR-003 local immutable filesystem objects
ADR-004 tus for resumable upload
ADR-005 Cloudflare Access plus origin JWT verification
ADR-006 dual admin/share hostnames
ADR-007 R2 used only for encrypted backup
ADR-008 upload crash-consistency protocol
ADR-009 rooted trash/search semantics
ADR-010 hardlink backup snapshot
ADR-011 global share epoch after disaster restore
ADR-012 single app replica limitation
```

Each ADR contains:

- context;
- decision;
- alternatives considered;
- consequences;
- conditions that would trigger re-evaluation.

---

# 26. When to re-architect

Do not migrate technology because of vague future scale.

## Move from SQLite to PostgreSQL only if real evidence appears

Examples:

- multiple app replicas are required;
- sustained write contention becomes material;
- metadata size/query complexity demonstrably exceeds SQLite operational comfort;
- cross-node workers require stronger shared coordination.

## Move primary blobs to object storage only if

- storage no longer fits one VPS;
- compute/storage need independent scaling;
- multi-node serving becomes necessary;
- operational economics justify added complexity.

## Introduce queue/broker only if

- durable asynchronous work cannot be represented by local DB state + reconciler;
- multiple workers/nodes genuinely need distributed consumption.

Before these thresholds, added infrastructure is more likely to reduce reliability than increase it.

---

# 27. Configuration contract

Example `.env.example` categories:

```text
APP_ENV
APP_BASE_URL_ADMIN
APP_BASE_URL_SHARE

DATA_DIR
DB_PATH
DB_BUSY_TIMEOUT
SQLITE_SYNCHRONOUS

MAX_STORAGE_BYTES
MAX_FILE_BYTES
MIN_FREE_BYTES
TRASH_RETENTION_DAYS
MAX_NODE_COUNT_SOFT

TUS_CHUNK_SIZE
MAX_ACTIVE_UPLOADS
MAX_ACTIVE_DOWNLOADS
MAX_PUBLIC_DOWNLOADS
UPLOAD_EXPIRY

CF_ACCESS_TEAM_DOMAIN
CF_ACCESS_AUD
CF_ACCESS_ALLOWED_SUBJECT
TRUSTED_PROXY_MODE

SHARE_TOKEN_PEPPER
SHARE_SESSION_SECRET
GLOBAL_SHARE_EPOCH

BACKUP_ENABLED
BACKUP_MAX_SELECTED_BYTES
BACKUP_SNAPSHOT_TTL

ALERT_WEBHOOK_URL
TZ
```

Do not pass R2/Restic credentials to the web process when backup runs separately.

---

# 28. Production release checklist

Release only when all conditions pass.

## Architecture / data

- [ ] SQLite database is on verified local filesystem.
- [ ] WAL and foreign keys are enabled correctly.
- [ ] required PRAGMAs apply to every connection.
- [ ] migrations and DB restore rollback tested.
- [ ] active search is root-scoped and trash descendants cannot leak.
- [ ] move-cycle prevention tested.

## Upload

- [ ] tus HEAD/PATCH resume tested through Cloudflare.
- [ ] configured chunk remains below Cloudflare request limit.
- [ ] upload reservation prevents concurrent quota oversubscription.
- [ ] physical free-space threshold includes staging/DB/WAL/backup temp.
- [ ] whole-file integrity hash computed.
- [ ] crash at every finalize boundary tested.
- [ ] reconciler repairs/marks failed deterministically.

## Download/share

- [ ] Range/If-Range tested.
- [ ] 10 GiB-equivalent stream does not scale RAM with file size.
- [ ] share token never logged.
- [ ] public pages are no-store/noindex/no-referrer.
- [ ] password rate limiting works.
- [ ] revoke applies on next request.
- [ ] disaster restore invalidates old share sessions/shares per policy.

## Security

- [ ] Cloudflare Access enabled on admin hostname.
- [ ] Go app independently validates Access JWT.
- [ ] unknown JWKS key rotation path tested.
- [ ] forged identity headers rejected.
- [ ] app has no public Docker port.
- [ ] share hostname route allowlist tested.
- [ ] CSRF/Origin policy tested.
- [ ] traversal/symlink/MIME spoofing tested.
- [ ] container non-root/read-only/cap-drop/no-new-privileges.
- [ ] secrets absent from repository and image.

## Backup/DR

- [ ] web process does not hold R2 credentials unless explicitly accepted as a risk.
- [ ] hardlink snapshot creation tested.
- [ ] encrypted Restic snapshot reaches R2.
- [ ] retention/prune tested.
- [ ] clean restore recovers DB + selected object.
- [ ] restored SHA-256 matches.
- [ ] disaster recovery runbook rehearsed.
- [ ] R2 usage budget monitored as GB-month, not assumed as a hard 10 GB cap.

## Operations

- [ ] disk warning/block thresholds alert correctly.
- [ ] backup stale alert tested.
- [ ] application graceful shutdown tested.
- [ ] previous image + DB snapshot rollback tested.
- [ ] desktop/mobile E2E passes.
- [ ] 500k-node qualification benchmark completed or explicitly waived with reason.

---

# 29. Recommended implementation order

The shortest safe path to a usable private beta is:

```text
foundation
→ DB/tree
→ Cloudflare Access boundary
→ upload backend + crash recovery
→ upload/file UI
→ download
→ public share
→ backup/restore
→ hardening/chaos/load
→ production
```

Do not expose public shares before public-host isolation, token redaction and brute-force controls are complete.

Do not call the system production-ready before an actual backup restore drill succeeds.

---

# 30. Estimated effort

For one developer already comfortable with Go/React/Docker/Cloudflare:

| Stage | Rough effort |
|---|---:|
| Foundations + DB/tree | 3–5 days |
| Access/security boundary | 1–2 days |
| resumable upload + recovery | 4–7 days |
| admin UI | 3–5 days |
| download + public share | 3–5 days |
| backup + restore | 2–4 days |
| hardening/chaos/load/runbook | 3–6 days |

A realistic production-quality range is roughly **19–34 focused developer-days**, depending heavily on UI polish and depth of testing.

A private beta can happen earlier, but public sharing should wait until the corresponding security tests pass.

---

# 31. Source-verified assumptions (checked 2026-09-05)

The following implementation assumptions were re-checked against current official documentation while preparing this V2 plan:

1. SQLite WAL relies on a shared-memory WAL index and should not be treated as a network-filesystem multi-host database.
   - https://sqlite.org/fileformat.html
   - https://sqlite.org/atomiccommit.html

2. tus resumable upload uses `HEAD` to recover the server offset and `PATCH` to continue data transfer; protocol extensions include checksum and termination.
   - https://tus.io/protocols/resumable-upload

3. Cloudflare Free currently limits incoming request body/upload size to 100 MB, so tus chunks should remain comfortably below that value.
   - https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/
   - https://developers.cloudflare.com/workers/platform/limits/

4. Cloudflare Access sends `Cf-Access-Jwt-Assertion`; Cloudflare recommends validating this JWT at the origin, including issuer/audience and rotating signing keys.
   - https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/

5. Cloudflare R2 supports bucket lock retention rules preventing deletion/overwrite for matching objects during the configured retention period.
   - https://developers.cloudflare.com/r2/buckets/bucket-locks/

6. Cloudflare R2 Standard currently includes 10 GB-month/month in its free tier; this is usage-based free allowance rather than a hard bucket storage limit.
   - https://developers.cloudflare.com/r2/pricing/

7. Cloudflare Tunnel can publish multiple applications/hostnames through one tunnel, Cloudflare recommends remotely-managed tunnels for most Docker deployments, and `TUNNEL_TOKEN_FILE` is supported for remotely-managed tunnels on cloudflared 2025.4.0+.
   - https://developers.cloudflare.com/tunnel/routing/
   - https://developers.cloudflare.com/tunnel/downloads/update-cloudflared/
   - https://developers.cloudflare.com/tunnel/advanced/local-management/
   - https://developers.cloudflare.com/tunnel/advanced/run-parameters/

8. Turnstile pre-clearance can issue Cloudflare clearance cookies, so it can be used as an optional escalation layer for suspicious public-share unlock flows without moving authorization out of the application.
   - https://developers.cloudflare.com/turnstile/additional-configuration/hostname-management/pre-clearance/

---

# 32. Final architecture statement

For the stated workload, the chosen architecture should remain deliberately boring and Docker-native:

```text
Internet
   ↓
Cloudflare Edge
   ├── Access on admin hostname
   ├── WAF / DDoS protections
   └── cache bypass for private/share routes
   ↓
existing Cloudflare Tunnel
   ↓
shared cloudflared container
   ↓  filemgr_edge (dedicated external Docker network)
filemgr-app container
   ├── embedded React UI
   ├── SQLite metadata on bind-mounted local disk
   └── immutable file objects on bind-mounted local disk

Host systemd timer
   ↓ docker compose run --rm
filemgr-backup one-shot container
   ↓
Restic encrypted snapshots
   ↓
private Cloudflare R2
```

The sophistication belongs in **correctness boundaries**—state machines, idempotency, reconciliation, immutable storage, scoped security boundaries, crash testing and restore drills—not in multiplying infrastructure components.

That design provides the best balance of security, operability, performance and implementation cost for a single-admin self-hosted VPS system.
