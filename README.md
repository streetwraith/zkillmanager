# zkillmanager

Collects EVE Online killmails from [zKillboard](https://zkillboard.com) into a shared
Postgres store, so read-only consumers — primarily **lscan** (the threat-profiling UI at
`/home/eve/lscan`) — can query and aggregate them without hitting zKillboard/ESI on their
request path.

- **What it does:** backfill + daily raw dumps + an R2Z2 live tail → a windowed store of
  **player-vs-player** killmails, keyed by `killmail_id` (immutable except a post-ingest
  pod-pairing flag; pods themselves aren't retained).
- **Stack:** Go (single long-running service with an internal scheduler), Postgres,
  deployed on Coolify.

## Docs

- `PROJECT.md` — architecture, ingest flows, Postgres schema, deployment.
- `docs/zkill-notes.md` — zKillboard API specifics, measured field breakdown, sizing, prior art.
- `AGENTS.md` — working conventions + Go language profile (pre-set).

## Status

Core ingestion implemented (Go 1.26, module `zkillmanager`): env config; Postgres store +
embedded migrations; the SDE-backed system→space resolver; the PvP filter and derived
`awox`; batched idempotent upserts; a gap-driven dump sync (backfill = all gaps,
reconciliation = biggest gaps, from `totals.json` vs `dump_progress`); and the live R2Z2
ephemeral tail — all wired via `errgroup` with graceful shutdown behind an internal `/healthz`.
Stealth hulls are materialized into `killmails.stealth_ship` (from `stealth_ships.txt`,
expanded via the SDE) and joined at read. The internal scheduler runs hourly reconciliation
(picks up new/grown dumps), a pod-pairing pass every few minutes (sets `kills.with_pod`, then
drops the pod), and a daily retention prune. Design + schema in `PROJECT.md`.

## Build & test

```
go build ./...                # compile
go test ./...                 # unit tests: classification, resolver, PvP/awox, buildRows, live feed, error reporting
go run ./cmd/zkillmanager     # requires DATABASE_URL, USER_AGENT

# integration tests hit a real Postgres, read-only:
ZKM_TEST_DSN="postgres://readonly@horizon:55432/eve" go test -tags integration ./internal/store/
```

## Configuration

12-factor env vars. The app **auto-loads `.env` if present** (real env vars still take
precedence), so local dev is just:

```
cp .env.example .env         # edit if needed
go run ./cmd/zkillmanager
```

`BUGSINK_DSN` is optional and stays **empty locally**: with no DSN the app makes no error-
reporting calls. Prod gets it from Coolify. See "Error reporting" in `PROJECT.md`.

Prereq: run `DUMP_SCHEMAS=sde ~/sync-prod-db.sh` first — it creates the local `zkillmanager`
role + `killmails` schema and loads `sde`. In prod, Coolify injects the same variables (no
`.env` in the container, so auto-load is a no-op). `USER_AGENT` should identify the
deployment + a contact, e.g. `zkillmanager/1.0 (+https://lscan.entropiadev.com; lscan@entropiadev.com)`.
