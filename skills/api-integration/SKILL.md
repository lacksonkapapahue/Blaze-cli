---
name: api-integration
description: Connect to a web API or webhook - read the docs, authenticate (API key, OAuth, bearer token), call endpoints with curl or code, handle pagination, rate limits, retries and errors, and store keys safely. Use when the user wants to automate a service (Shopify, Stripe, GitHub, Google, Notion, Telegram, any REST/GraphQL API) or build an integration.
tags: [api, rest, http, integration, automation, oauth]
version: 1
---

# API integration

## 1. Before writing code

- Find the official docs (web_search "<service> API reference"), and fetch the page for the exact endpoint: method, URL, auth, required params, response shape, rate limits, pagination, API version.
- Check if Blaze already has a connector or MCP server for it (Accounts screen / find_tools) - use that before building your own.
- Prefer an official SDK when it's maintained; plain HTTP is fine for a few endpoints.
- Decide where it runs (one-off script, scheduled job, part of an app) - that sets error handling and logging needs.

## 2. Secrets

- Never hard-code keys or paste them into chat logs, commits or command lines that land in shell history.
- Store them in environment variables, a `.env` file that is git-ignored (add it to `.gitignore` first), or the OS keychain. Read with `os.Getenv` / `os.environ` / `process.env`.
- Ask the user to create keys with the **least privilege** (read-only scopes when possible) and tell them where in the service's settings.
- Mask keys in any output (`sk-…abcd`).

## 3. First call

Start with the simplest authenticated GET to prove auth works:
```bash
curl -sS -H "Authorization: Bearer $API_TOKEN" -H "Accept: application/json" "https://api.example.com/v1/me" | jq .
```
PowerShell: `Invoke-RestMethod -Uri $url -Headers @{Authorization="Bearer $env:API_TOKEN"}`.
Read the status code and error body; 401 = auth, 403 = scope/permission, 404 = wrong path/ID, 422/400 = payload, 429 = rate limit.

## 4. Robust calls

- **Timeouts** on every request (10-30 s).
- **Retries** only for 429/5xx/network errors, exponential backoff with jitter (e.g. 1s, 2s, 4s, max 5 tries), honour `Retry-After`. Never blindly retry non-idempotent POSTs - use an idempotency key if the API supports it.
- **Pagination**: follow the API's scheme (cursor/`next` link/`page`+`per_page`/`Link` header) until exhausted; cap total pages for safety.
- **Rate limits**: read `X-RateLimit-Remaining`/`Reset` headers; throttle; batch where the API allows.
- **Validation**: check required fields of responses; handle nulls; log the request ID header on errors.
- **Webhooks**: verify signatures (HMAC with the signing secret), respond 2xx fast and process async, handle duplicate deliveries (idempotent by event ID).
- **OAuth**: authorization-code + PKCE for user apps; store refresh tokens securely; refresh before expiry; loopback redirect (`http://127.0.0.1:<port>/callback`) for desktop tools.

## 5. Code shape (any language)

Small client module: base URL + auth header + `request(method, path, params, body)` with timeout/retry/error mapping, then one function per endpoint returning typed data. Keep side effects (writes, sends, deletes) behind an explicit confirmation when acting on the user's real accounts.

## 6. Test and hand over

- Test against a sandbox/test mode when the service has one (Stripe test keys, Shopify dev store).
- A dry-run flag for scripts that change data.
- README section: what it does, required env vars, how to run, how to schedule, how to rotate keys.
- Checklist: [ ] auth works [ ] pagination complete [ ] 429/5xx retried [ ] no secrets in repo/logs [ ] errors are readable for the user.
