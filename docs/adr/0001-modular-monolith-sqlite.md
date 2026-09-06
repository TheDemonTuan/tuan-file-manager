# ADR-0001: Modular Monolith with SQLite and Local Filesystem

## Status
Accepted

## Context
The goal is a lightweight, secure, and easily maintainable self-hosted file manager running on a single VPS (2 vCPU, 2-4 GB RAM) for one administrator, with storage up to 500 GB and files up to 10 GiB behind Cloudflare Tunnel.

Previous distributed architectures (Postgres, Redis, Celery/Kafka, S3/MinIO) introduce high RAM baselines, multi-container failure modes, distributed concurrency issues, and excessive operational complexity for a single-node deployment.

## Decision
1. Implement a Go modular monolith application binary with embedded frontend assets.
2. Use SQLite in WAL mode on the local SSD/filesystem for metadata, strictly configured with foreign keys and connection timeouts.
3. Store files directly on the local filesystem using opaque immutable storage keys (`/data/objects/xx/<key>`), decoupling logical paths from filesystem layout.
4. Off-site backup is performed out-of-process via Restic to Cloudflare R2 using dedicated snapshots without exposing R2 credentials to the web server process.

## Consequences
- Extremely low memory footprint (< 100 MB baseline RAM).
- Single binary deployment via Docker Compose.
- Zero network hops for metadata queries and file streaming.
- Scaling horizontally across multiple nodes is deliberately not supported in V1; if scale outgrows single VPS, migration paths are well-defined.
