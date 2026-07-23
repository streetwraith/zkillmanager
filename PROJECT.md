# PROJECT — zkillmanager

## What this is

**zkillmanager** collects EVE Online killmails from zKillboard into a shared Postgres
store, so read-only consumers — primarily [lscan](/home/eve/lscan), the threat-profiling
UI — can query and aggregate them without ever touching zKillboard/ESI on their request
path.

Collecting/managing the data and serving it are deliberately **two apps sharing one
database**. This repo is the collector (owns all writes); lscan is a reader (read-only DB
role). Fetching is decoupled from user traffic, so cost is O(killmail volume), not
O(searches).

## Why a collector at all

Killmails are **immutable** and keyed by `killmail_id`: a killmail's content never
changes. So once fetched it is cached forever and shared across every pilot in it (a
200-pilot fleet fight is stored once). The collector builds and maintains that shared
cache; readers just query it.

## Data sources (zKillboard; on Cloudflare R2 since Jan 2026)

- **Daily raw dumps** — `r2z2.zkillboard.com/history/raw/YYYYMMDD.json` (full killmails for
  a day). `history/totals.json` gives per-day counts; `history/YYYYMMDD.json` is the
  id+hash index. Used for backfill + daily catch-up.
- **RedisQ live feed** — `zkillredisq.stream/listen.php?queueID=<stable-unique>`: long-poll,
  one full killmail per package, remembers the queue ~3h (a periodic cron won't miss kills
  across restarts). One consumer per queueID.
- Etiquette: descriptive `User-Agent` on every request; reasonable spacing.

Details, sizing, and prior art (localthreat) in `docs/zkill-notes.md`.

## Runtime model (Go)

A single long-running service, three concurrent duties + a tiny HTTP server, coordinated
with `context` + `errgroup`:

- **RedisQ tail** — continuous long-poll goroutine (live killmails).
- **Internal scheduler** — daily dump + reconciliation + prune (cron).
- **Backfill** — one-shot on first boot / `--backfill`, resumable via `ingest_state`.
- **`/healthz` + `/metrics`** — for Coolify health checks.

All writes are `INSERT ... ON CONFLICT (killmail_id) DO NOTHING`, so dumps / RedisQ /
backfill overlap harmlessly. At ingest, each killmail is enriched from the SDE for the
*stable* derivations only (`system → region`, `security → space_class`).

### Package layout
```
cmd/zkillmanager/main.go   # wire config; start scheduler + redisq + http
internal/config/           # env-driven (12-factor)
internal/store/            # pgxpool, migrations, upserts, read helpers
internal/zkill/history.go  # daily raw-dump client (R2)
internal/zkill/redisq.go   # RedisQ long-poll client
internal/enrich/           # SDE-backed derivations (space_class, region)
internal/ingest/           # parse -> enrich -> upsert (killmails + participation)
internal/scheduler/        # cron: daily dump, reconciliation, prune
migrations/                # *.sql
Dockerfile
```

### Config (env; Coolify injects)
`DATABASE_URL`, `REDISQ_URL`, `REDISQ_QUEUE_ID`, `HISTORY_BASE_URL`, `RETENTION_DAYS`,
`USER_AGENT`, `BACKFILL_ON_START`, `DAILY_CRON`, `PRUNE_CRON`, `HTTP_ADDR`.

### Libraries
`pgx/v5` (+ `COPY` for bulk dump inserts), a cron lib (e.g. `robfig/cron/v3`) or a stdlib
ticker, `errgroup`, stdlib `net/http` + `encoding/json` (swap to a faster JSON only if dump
parsing proves hot), `log/slog`. No frameworks.

## Postgres schema

Two schemas in **one** database: the killmail data (below) plus the **existing `sde`
schema**, reused for id→name display and the `system → region` / `security → space_class`
derivations (no bespoke reference tables). The `sde` schema must be in the same DB —
Postgres can't join across databases.

```sql
-- immutable raw store: source of truth, reprocessable
CREATE TABLE killmails (
    killmail_id         BIGINT PRIMARY KEY,
    killmail_time       TIMESTAMPTZ NOT NULL,
    solar_system_id     INT NOT NULL,
    region_id           INT,
    victim_char_id      BIGINT,
    victim_corp_id      BIGINT,
    victim_alliance_id  BIGINT,
    victim_ship_type_id INT,
    total_value         BIGINT NOT NULL DEFAULT 0,
    attacker_count      INT NOT NULL,
    npc  BOOLEAN NOT NULL DEFAULT false,
    solo BOOLEAN NOT NULL DEFAULT false,
    awox BOOLEAN NOT NULL DEFAULT false,
    hash TEXT   NOT NULL,
    raw  JSONB  NOT NULL
);
CREATE INDEX killmails_time_brin   ON killmails USING BRIN (killmail_time);
CREATE INDEX killmails_victim_time ON killmails (victim_char_id, killmail_time DESC); -- losses

-- attackers only; the inverted index that turns per-pilot lookups into a range scan
CREATE TABLE participation (
    character_id         BIGINT NOT NULL,
    killmail_id          BIGINT NOT NULL REFERENCES killmails(killmail_id) ON DELETE CASCADE,
    killmail_time        TIMESTAMPTZ NOT NULL,
    ship_type_id         INT,            -- attacker hull (raw; classify via attacker_bucket)
    victim_ship_type_id  INT,            -- target hull  (raw; classify via victim_bucket)
    final_blow           BOOLEAN NOT NULL DEFAULT false,
    solar_system_id      INT NOT NULL,
    region_id            INT,
    space_class          SMALLINT NOT NULL,   -- 0 hs, 1 ls, 2 ns, 3 wh, 4 other
    total_value          BIGINT NOT NULL DEFAULT 0,
    attacker_count       INT NOT NULL,        -- for avg gang size
    solo                 BOOLEAN NOT NULL DEFAULT false,
    stealth              BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (character_id, killmail_id)
);
CREATE INDEX participation_char_time ON participation (character_id, killmail_time DESC);

-- custom, independent ship classifications; edited freely, joined at read
CREATE TABLE attacker_bucket (type_id INT PRIMARY KEY, bucket TEXT NOT NULL);
CREATE TABLE victim_bucket   (type_id INT PRIMARY KEY, bucket TEXT NOT NULL);

-- ingest bookkeeping (resumable backfill, last daily date, ...)
CREATE TABLE ingest_state (key TEXT PRIMARY KEY, value TEXT NOT NULL,
                           updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
```

**Design principle:** denormalize the *stable* facts (`system → region`, `security →
space_class`) into the rows; keep the *volatile* classifications (`attacker_bucket`,
`victim_bucket`) as small join tables so reclassifying is an `UPDATE`, never a reprocess.
Full fidelity always lives in `killmails.raw`.

- **Kills** for a character → `participation WHERE character_id = ?` (attacker rows).
- **Losses** → `killmails WHERE victim_char_id = ?` (victim is one row per mail).
- **Targets / ships-flown** → join `victim_bucket` / `attacker_bucket` at read; group by bucket.

Bucket seed data: `attacker_ship_groups.txt` (empty; TODO) and `victim_ship_groups.txt`
(drafted). Retention (e.g. 90d / 1y) is a pruning choice, not correctness; consider monthly
RANGE partitions on `killmail_time` so pruning is a partition `DROP`.

## Stats (computed by the reader, window-scoped)

The dumps give raw killmails, not zKill's precomputed stat block — kills, losses, avg gang
size, ISK efficiency, solo %, etc. are derived from the stored killmails over the active
window. **All-time reputation** (danger/gang ratio, lifetime totals) can't come from a
windowed store: lscan fetches it from a cached zKill `/api/stats/characterID/{id}/` call,
plus sec-status/affiliations from ESI.

## Deployment (Coolify)

A lean always-on Docker service — a strong fit:
- Multi-stage Dockerfile (`golang` build → `distroless/static`), ~15–25 MB image.
- One Coolify service, health check `/healthz`, restart-on-failure, small memory limit.
- Postgres as a Coolify-managed DB in the same project/network (zkillmanager r/w, lscan
  read-only, `sde` schema in the same DB).
- **Scheduling is internal** (the app's own cron) — Coolify just keeps the one container
  alive. Always-on (RedisQ), not scale-to-zero; RedisQ's ~3h memory + the daily dump re-pull
  cover restarts/gaps.
