# ADR-0002: Tus 1.0 Protocol and 3-Phase Crash-Safe Upload Finalization

## Status
Accepted

## Context
Uploading large files (up to 10 GiB) over home/mobile internet connections through Cloudflare edge requires resumability, strict chunk size control (to stay well under Cloudflare's 100 MB client body limit), pre-allocation space checks, and deterministic crash recovery.

## Decision
1. Implement tus 1.0 protocol (`OPTIONS`, `POST`, `HEAD`, `PATCH`, `DELETE`).
2. Client chunk size is configured to 16 MiB (safely below Cloudflare limits).
3. Enforce atomic space reservation on `POST` creation against both logical quota and physical filesystem free-space guards.
4. Execute 3-phase finalization when all bytes are received:
   - **Phase A (Durable Intent):** DB transaction sets state to `finalizing`, persists whole-file SHA-256 and pre-allocated `final_storage_key`.
   - **Phase B (Filesystem Commit):** `fsync` staging file, atomic `os.Rename` staging file to sharded physical storage path (`/data/objects/xx/<storage_key>`), sync destination directory.
   - **Phase C (Metadata Commit):** DB transaction creates logical `node` and `file_object`, frees quota reservation, marks upload `complete`.
5. Background reconciler scans `finalizing` uploads at startup and periodically to repair or deterministically resolve any crashes between phases.

## Consequences
- No corrupt or orphan files left behind across unexpected container or host restarts.
- Strict quota enforcement even under concurrent uploads.
- Full compatibility with standard `tus-js-client`.
