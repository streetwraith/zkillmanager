# HANDOFF — zkillmanager

You're a fresh session starting **zkillmanager**. This captures the context and decisions
from the originating session (in the lscan project) so you can pick up without
re-deriving. Read `PROJECT.md` for the full design/schema and `docs/zkill-notes.md` for
zKillboard API specifics; conventions + the Go profile are in `AGENTS.md`.

## Where this came from

zkillmanager was split out of **lscan** (`/home/eve/lscan`) — an EVE PVP threat-profiling
Django app currently running on *mock* killmails. lscan is the read-only UI; zkillmanager
is the data collector. lscan's `PROJECT.md` has a "Going live — data via zkillmanager" note
pointing here. This repo is greenfield: only docs + bucket seed files so far.

## Decisions already made (don't re-litigate)

- **Split:** collector (this repo, owns writes) + lscan (read-only reader), sharing one
  Postgres DB.
- **Language: Go** — a lean long-running daemon. Ingest is light and I/O-bound
  (~20k killmails/day), so it's about footprint/ops, not throughput.
- **Storage: Postgres** — lscan's read is a per-character point lookup + small aggregation;
  columnar (ClickHouse/DuckDB) only if fleet/region-wide analytics appear later.
- **Immutability → cache forever:** `INSERT ... ON CONFLICT (killmail_id) DO NOTHING`.
- **Sources:** daily R2 raw dumps (backfill + catch-up) + RedisQ (live tail).
- **Schema:** `killmails` (raw) + `participation` (attackers only; losses come from
  `killmails.victim_char_id`) + `attacker_bucket`/`victim_bucket` (two *independent* custom
  classifications, joined at read) + reuse the existing `sde` schema. Store **raw hull
  type_ids** in `participation`; never bake the volatile bucket mapping into the rows.
- **Stats:** windowed metrics computed from the store; all-time reputation from a cached
  zKill `/api/stats` call + ESI.
- **Coolify:** single always-on service, internal scheduler, Postgres managed by Coolify.

## Seed data (moved in with this repo)

- `victim_ship_groups.txt` — drafted victim classification. Format: a bucket name, then
  indented `SDE group: hull, hull` lines. Parses into `victim_bucket` (hull `type_id →
  bucket`); needs hull-name → type_id resolution via the `sde` schema.
- `attacker_ship_groups.txt` — empty placeholder; attacker classification TBD (a different
  scheme from victims).

## Open questions / TODO before/while building

- **Which SDE import** populates the `sde` schema? (Fuzzwork Postgres conversion? other?)
  Get the real table/column names (e.g. `sde."invTypes"."groupID"`,
  `sde."mapSolarSystems"."regionID"`/`.security`). Confirm it's the **same database**.
- **FW detection** — how to flag faction-warfare kills (zkb labels?).
- **Stealth detection** — which SDE groups count as covert (for the `stealth` flag).
- **Bucket parsing** — turn the two `*_ship_groups.txt` seeds into `attacker_bucket` /
  `victim_bucket` rows.
- Confirm Coolify Postgres + networking to lscan.

## Suggested first steps

1. `go mod init` + the package skeleton in `PROJECT.md`; add `pgx`, a cron lib, `errgroup`.
2. Write `migrations/0001_init.sql` from the schema in `PROJECT.md`.
3. Stub the RedisQ long-poll loop and the daily-dump job (idempotent upserts).
4. `/healthz` + Dockerfile; a deploy shell for Coolify.
5. Wire the SDE lookups once the `sde` schema shape is confirmed.
