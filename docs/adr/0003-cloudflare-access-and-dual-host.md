# ADR-0003: Dual Host Routing and Cloudflare Access Authentication

## Status
Accepted

## Context
The file manager serves two distinct roles:
1. Admin management console (`files.example.com` or `admin.example.com`).
2. Public anonymous share links (`share.example.com`).

Public visitors must never be able to access administrative API endpoints or trigger mutations, while the admin portal must be strictly secured behind Zero Trust without storing admin passwords in the database.

## Decision
1. Route traffic using Host header separation:
   - Admin host: Requires valid Cloudflare Access JWT in `Cf-Access-Jwt-Assertion` header. Verified locally using Cloudflare team JWKS public keys with audience and email/identity policy checks. Supports key rotation.
   - Share host: Strictly allowlists `/s/{token}` endpoints and returns 404 for all admin `/api/v1/*` routes.
2. In local/development mode, an optional `DEV_AUTH_BYPASS` flag or mock identity can be configured for local testing without Cloudflare Access.
3. For public shares: Tokens are cryptographically generated (256-bit CSPRNG), stored as HMAC-SHA256 digests, and protected with optional Argon2id passwords. Successful password authentication issues an HMAC-signed HttpOnly session cookie scoped by `auth_version` and `global_share_epoch`.

## Consequences
- Defense in depth: Even if Cloudflare Access is misconfigured, forged headers fail verification.
- Share host cannot be leveraged for SSRF or privilege escalation to admin operations.
