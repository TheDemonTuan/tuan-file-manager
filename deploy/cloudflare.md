# Cloudflare Configuration Guide

This guide details how to configure Cloudflare Tunnel and Zero Trust Access for the self-hosted file manager.

---

## 1. Cloudflare Tunnel Configuration

The system uses a single backend container listening on port `8080`, handling two separate hostnames:

1. **Admin Host:** `files.yourdomain.com`
2. **Share Host:** `share.yourdomain.com`

### Recommended: Cloudflare Zero Trust Dashboard (Remotely Managed)

1. Go to **Cloudflare Zero Trust** > **Networks** > **Tunnels**.
2. Select your active tunnel (or create one).
3. Under the **Public Hostnames** tab, add two entries:

| Path / Subdomain | Service Type | URL | Additional Settings |
|---|---|---|---|
| `files.yourdomain.com` | HTTP | `filemgr-app:8080` (or `127.0.0.1:8080`) | Disable Chunked Encoding: Off |
| `share.yourdomain.com` | HTTP | `filemgr-app:8080` (or `127.0.0.1:8080`) | Disable Chunked Encoding: Off |

---

## 2. Cloudflare Access Application (Admin Protection)

Protect `files.yourdomain.com` with Zero Trust Access.

1. In **Cloudflare Zero Trust**, navigate to **Access** > **Applications**.
2. Click **Add an Application** > **Self-Hosted**.
3. Fill in the details:
   - **Application Name:** `File Manager Admin`
   - **Session Duration:** 24 hours (or your preference)
   - **Application domain:** `files.yourdomain.com`
4. Under **Policies**, create a rule:
   - **Policy Name:** `Allow Admin`
   - **Action:** `Allow`
   - **Include:** `Emails` > `admin@yourdomain.com` (matching your `CF_ACCESS_ALLOWED_SUBJECT`)
5. Save the application.
6. Under the application's **Overview** tab, copy the **Audience (AUD) Tag**.
7. Set the environment variables in your `.env` file:
   ```env
   CF_ACCESS_TEAM_DOMAIN=yourteam.cloudflareaccess.com
   CF_ACCESS_AUD=<your-audience-tag>
   CF_ACCESS_ALLOWED_SUBJECT=admin@yourdomain.com
   ```

---

## 3. Cloudflare Rules & Caching Policy

To prevent Cloudflare from interfering with resumable uploads and private public share links:

### A. Caching Rules (Zero Trust / Page Rules)
Create a Cache Rule for `share.yourdomain.com`:
- **Match:** `Hostname equals share.yourdomain.com`
- **Cache Eligibility:** `Bypass cache` (Respects `Cache-Control: no-store`)

### B. Upload Chunk Size
Cloudflare Free tier limits standard request bodies to 100 MB.
The file manager is preconfigured with `TUS_CHUNK_SIZE=16777216` (16 MiB), safely remaining below this limit while maximizing throughput.
