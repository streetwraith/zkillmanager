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

- **Daily raw dumps** — `r2z2.zkillboard.com/history/raw/YYYYMMDD.json`: full killmails for a
  day, as a JSON object keyed by `killmail_id`. **Each value is the plain ESI killmail — no
  `zkb` block** (no `totalValue`/`npc`/`solo`/`awox`/`hash`); see `docs/zkill-notes.md`. Used
  for backfill + daily catch-up. `history/totals.json` (per-day counts) drives the gap-based
  sync: compared against the totals we recorded at last pull, it reveals which days are
  incomplete. (The `history/YYYYMMDD.json` id+hash index is not used — we take full
  killmails from the raw dump and do not store the hash.)
- **R2Z2 live feed** — `r2z2.zkillboard.com/ephemeral/sequence.json` (latest sequence id) +
  `/ephemeral/{seq}.json` (one killmail: `{esi, hash, zkb, sequence_id}`). Poll `seq++` until
  a 404 (caught up), sleep >=6s, repeat; files expire ~24h, rate limit 15 req/s. This is the
  official replacement for **RedisQ, which was sunset 2026-05-31** (its old
  `zkillredisq.stream` host now points at 127.0.0.1). We track our own sequence in
  `ingest_state` (no queueID; restarts resume cleanly). The ephemeral file carries a `zkb`
  block, but we derive the flags ourselves so live and backfilled rows are identical.
- Etiquette: descriptive `User-Agent` on every request; reasonable spacing — the backfill
  waits ~10s between daily dump fetches.

Details, measured field breakdown, sizing, and prior art (localthreat) in
`docs/zkill-notes.md`.

## Runtime model (Go)

A single long-running service, three concurrent duties + a tiny HTTP server, coordinated
with `context` + `errgroup`:

- **R2Z2 live tail** — polls the ephemeral sequence feed for live killmails; resumes from
  the stored sequence on restart.
- **Internal scheduler** — hourly poll for the new daily dump + the gap-based reconciliation
  `Sync` (catches late submissions on *any* day), a pod-pairing pass every few minutes, and
  retention prune.
- **Pod pairing** — pods (capsules) are ingested normally, then a periodic pass pairs each pod
  older than 15 min with the ship kill it followed, sets `kills.with_pod` on the pilots who were
  on *both* mails, and deletes the pod (cascading its `kills` rows), so pods aren't retained. It
  runs through the `Syncer`'s lock so it never overlaps a bulk day insert (which writes ship
  kills and pods in arbitrary order) and mispairs. This is the one place a stored row mutates
  after insert (`with_pod`), a deliberate trade for not storing ~28% more killmails.
- **Backfill / sync** — one gap-driven `Sync`: compare `totals.json` against `dump_progress`
  and re-pull the biggest-gap days (~10s apart). Backfill is `Sync` with no limit on first
  boot / `--backfill`; the daily job is the same `Sync` with a small limit. Resumable via
  `dump_progress`.
- **`/healthz`** — for Coolify health checks.

All ingest writes are `INSERT ... ON CONFLICT (killmail_id) DO NOTHING`, so dumps / live feed /
backfill overlap harmlessly. (Pod pairing is the one non-insert write path; see below.)

**Ingest filter (PvP only):** a killmail is stored only when the victim is a player
(`victim.character_id` present) *and* at least one attacker is a player. This drops NPC-only
kills and non-player-victim kills (structures, NPCs, mission/ratting losses) — lscan profiles
player-vs-player combat, so those carry no signal.

At ingest each surviving killmail is enriched from the SDE for the *stable* derivations
(`system → constellation`/`region`, region wormhole-class + `security_status` → `space_type`), and
`awox` is derived from the ESI data (a player attacker shares the victim's corp/alliance).
`solo` is **not stored** — it's `attacker_count = 1` at read. The full attacker array is read
to emit `kills` rows (keeping each attacker's `character_id`, hull, `final_blow`, and
corp/alliance), then discarded.

### Package layout
```
cmd/zkillmanager/main.go   # wire config; start scheduler + live tail + http
internal/config/           # env-driven (12-factor)
internal/store/            # pgxpool, migrations, upserts, read helpers
internal/zkill/history.go  # daily raw-dump + totals client (R2Z2)
internal/zkill/live.go     # R2Z2 ephemeral live-feed client
internal/enrich/           # SDE-backed derivations (space_type, region)
internal/ingest/           # parse -> PvP filter -> enrich -> upsert (killmails + kills)
internal/scheduler/        # tickers: reconciliation (gap Sync) + retention prune
migrations/                # *.sql
Dockerfile
```

### Config (env; Coolify injects)
`DATABASE_URL`, `HISTORY_BASE_URL`, `LIVE_BASE_URL`, `RETENTION_DAYS`, `USER_AGENT`,
`BACKFILL_ON_START`, `RECONCILE_INTERVAL` (default `1h`), `PRUNE_INTERVAL` (default `24h`),
`HTTP_ADDR`.

`RETENTION_DAYS` bounds **both** pruning and backfill depth — the backfill never fetches R2
dumps older than the horizon (no point pulling what prune would drop). Prod runs e.g. 365;
local dev sets it to 3 (see "Local development").

`USER_AGENT` must identify the deployment + a contact (e.g.
`zkillmanager/1.0 (+https://lscan.entropiadev.com; lscan@entropiadev.com)`; dev uses the
`test.` subdomain) — sent on every R2Z2 request. Local dev loads all of the above from
a gitignored `.env` (template: `.env.example`) — the app auto-loads `.env` if present (real
env vars still win); prod uses Coolify-injected env (no `.env` in the container).

### Libraries
`pgx/v5` (batched inserts per killmail; `COPY` for the stealth-ship refresh), stdlib
`time.Ticker` for scheduling (no cron lib), `errgroup`, `joho/godotenv` (dev-only `.env`
auto-load), stdlib `net/http` + `encoding/json` (swap to a faster JSON only if dump parsing
proves hot), `log/slog`. No frameworks.

## Postgres schema

A dedicated **`killmails` schema** in the shared `eve` database on `horizon`, owned and
written **only** by zkillmanager; readers (lscan) get `USAGE` + `SELECT` (mirroring how
sdemanager owns `sde`). It sits beside the **existing `sde` schema** in the same DB — reused
for id→name display and the `system → constellation`/`region`, region wormhole-class, and
`security_status` → `space_type` derivations (no bespoke reference tables). This uses
`mapSolarSystems`, `mapRegions`, and `mapConstellations` in `sde` (plus
`types`/`groups`/`categories` for names + buckets) — all imported. Same DB matters: Postgres
can't join across databases.

```sql
-- the `killmails` schema is created by the one-time bootstrap (owned by zkillmanager, which
-- has no CREATE on the database); migrations only create tables within it.

-- one row per stored kill (player victim + >=1 player attacker); no raw JSONB blob
CREATE TABLE killmails.zkillboard_killmails (
    killmail_id         BIGINT PRIMARY KEY,
    killmail_time       TIMESTAMPTZ NOT NULL,
    solar_system_id     INT NOT NULL,
    constellation_id    INT,                 -- SDE-derived
    region_id           INT,                 -- SDE-derived
    space_type          SMALLINT NOT NULL,   -- 0 hs, 1 ls, 2 ns, 3 wormhole, 4 pochven
    pos_x               DOUBLE PRECISION,    -- victim.position (kept for future use)
    pos_y               DOUBLE PRECISION,
    pos_z               DOUBLE PRECISION,
    victim_char_id      BIGINT NOT NULL,     -- NOT NULL encodes the player-victim filter
    victim_corp_id      BIGINT,
    victim_alliance_id  BIGINT,
    victim_ship_type_id INT,
    attacker_count      INT NOT NULL,        -- player attackers only (NPCs excluded); solo = (attacker_count = 1)
    awox                BOOLEAN NOT NULL DEFAULT false,  -- attacker shares victim corp/alliance
    total_value         BIGINT               -- NOT sourced yet; see "Stats"
);
CREATE INDEX zkb_killmails_time_brin   ON killmails.zkillboard_killmails USING BRIN (killmail_time);
CREATE INDEX zkb_killmails_victim_time ON killmails.zkillboard_killmails (victim_char_id, killmail_time DESC); -- losses
-- partial index for the pod-pairing pass (capsule victims only)
CREATE INDEX zkb_killmails_pods ON killmails.zkillboard_killmails (killmail_time)
    WHERE victim_ship_type_id IN (670, 33328);

-- attackers only; the inverted index that turns per-pilot lookups into a range scan.
-- one row per player attacker per kill.
CREATE TABLE killmails.kills (
    character_id         BIGINT NOT NULL,
    killmail_id          BIGINT NOT NULL REFERENCES killmails.zkillboard_killmails(killmail_id) ON DELETE CASCADE,
    killmail_time        TIMESTAMPTZ NOT NULL,
    ship_type_id         INT,            -- attacker hull (raw; classify via attacker_bucket)
    victim_ship_type_id  INT,            -- target hull  (raw; classify via victim_bucket)
    final_blow           BOOLEAN NOT NULL DEFAULT false,
    solar_system_id      INT NOT NULL,
    constellation_id     INT,
    region_id            INT,
    space_type           SMALLINT NOT NULL,   -- see zkillboard_killmails
    attacker_count       INT NOT NULL,        -- player attackers, for avg gang size (solo = count 1)
    attacker_corp_id     BIGINT,              -- attacker affiliation at kill time (historical snapshot)
    attacker_alliance_id BIGINT,
    with_pod             BOOLEAN NOT NULL DEFAULT false, -- this pilot also podded the victim (set by pod pairing)
    PRIMARY KEY (character_id, killmail_id)
);
CREATE INDEX kills_char_time ON killmails.kills (character_id, killmail_time DESC);
-- lookup by killmail_id (pod pairing's flag + the FK cascade; also co-attacker reads)
CREATE INDEX kills_killmail  ON killmails.kills (killmail_id);

-- custom, independent ship classifications; edited freely, joined at read
CREATE TABLE killmails.attacker_bucket (type_id INT PRIMARY KEY, bucket TEXT NOT NULL);
CREATE TABLE killmails.victim_bucket   (type_id INT PRIMARY KEY, bucket TEXT NOT NULL);

-- ingest bookkeeping (misc key/value markers)
CREATE TABLE killmails.ingest_state (key TEXT PRIMARY KEY, value TEXT NOT NULL,
                                     updated_at TIMESTAMPTZ NOT NULL DEFAULT now());

-- ship-attribute lookup: stealth hulls, materialized from the seed by the collector and
-- joined at read (a volatile classification like the buckets -- not a per-row flag).
CREATE TABLE killmails.stealth_ship (type_id INT PRIMARY KEY);

-- per-day dump completeness: zkill's totals.json count when we last pulled the day.
-- gap = current totals - zkill_total drives backfill + reconciliation.
CREATE TABLE killmails.dump_progress (day DATE PRIMARY KEY, zkill_total INTEGER NOT NULL,
                                      pulled_at TIMESTAMPTZ NOT NULL DEFAULT now());
```

**Design principle:** store only the fields we query, in typed columns — there is **no raw
JSONB blob**. R2 dumps are immutable and re-fetchable, so reprocessing (e.g. a new derived
field) re-pulls the day rather than reading a local copy. Denormalize the *stable* SDE facts
(`constellation`/`region`, `space_type`) into the rows; keep the *volatile* classifications
(`attacker_bucket`, `victim_bucket`) as small join tables so reclassifying is an `UPDATE`,
never a reprocess. The full attacker array is read at ingest and consumed to emit `kills`
rows, then dropped; `victim.items` (54% of dump bytes, unused by lscan) is deliberately
omitted from the decode struct so the JSON decoder skips it entirely — which roughly halves
the backfill's per-day memory. `victim.position` is kept (`pos_x/y/z`).

- **Kills** for a character → `kills WHERE character_id = ?` (player-attacker rows).
- **Losses** → `zkillboard_killmails WHERE victim_char_id = ?` (victim is one row per mail).
- **Targets / ships-flown** → join `victim_bucket` / `attacker_bucket` at read; group by bucket.
- **Stealth** → join `ship_type_id` → `stealth_ship`; works for filtering kills and for
  flagging hulls/groups in the ships-flown view.

`space_type` (0 hs, 1 ls, 2 ns, 3 wormhole, 4 pochven) is derived at ingest from the system's
region wormhole-class (`sde.map_regions.wormhole_class_id`, uniform per wormhole/Pochven
region) + per-system `security_status`: class 25 → pochven; 1-6/12/13/14-18 → wormhole (C1-C6,
Thera, shattered, drifter); 19-23 (abyssal/void) → logged and skipped (instanced, no normal
PvP; no catch-all bucket); **everything else** — k-space 7/8/9 plus a few untagged NPC/edge
regions (Syndicate, the Jove regions, Zarzakh, Exordium, NULL class) — splits hs/ls/ns by
per-system `security_status`. Region class 7/8/9 is region-wide and coarse, so security is the
authority (Rancer is lowsec inside a class-7 region). lscan shows wormhole location as
region = "J-Space", constellation = the class (`C2`, ...), system = the J-code, by joining
`region_id → map_regions`. Bucket seed data: `attacker_ship_groups.txt` +
`victim_ship_groups.txt` (both complete; `group/groups/type/types/rest` grammar, "specific
type overrides its group") are seeded into `attacker_bucket` / `victim_bucket` by
`migrations/0002_seed_ship_buckets.sql` (groups expand from `sde` at apply time);
`migrations/0004_seed_deployables.sql` adds every published Deployable (SDE category 22) to
`victim_bucket` as `Deployables`. Retention
(e.g. 90d / 1y) is a pruning choice, not correctness; consider monthly RANGE partitions on
`killmail_time` so pruning is a partition `DROP`.

## Stats (computed by the reader, window-scoped)

The dumps give raw killmails, not zKill's precomputed stat block — kills, losses, avg gang
size, solo %, etc. are derived from the stored killmails over the active window. **All-time
reputation** (danger/gang ratio, lifetime totals) can't come from a windowed store: lscan
fetches it from a cached zKill `/api/stats/characterID/{id}/` call, plus
sec-status/affiliations from ESI.

**ISK value is unresolved.** ESI killmails (hence the R2 dumps) carry no ISK, so
`total_value` is not populated. We will *not* compute it locally; it will either be left out
or fetched from zKill — deferred. Any ISK-efficiency metric depends on that decision.

## Deployment (Coolify)

A lean always-on Docker service — a strong fit:
- Multi-stage Dockerfile (`golang` build → `distroless/static`), ~15–25 MB image.
- One Coolify service, health check `/healthz`, restart-on-failure, small memory limit.
- Postgres is the shared **`eve`** database on `horizon` (also home to `sde`): zkillmanager
  connects r/w as the `zkillmanager` role, lscan read-only. One cluster, many schemas — not a
  per-service DB.
- **Scheduling is internal** (the app's own cron) — Coolify just keeps the one container
  alive. Always-on (live feed), not scale-to-zero; the live feed resumes from its stored
  sequence and the daily dump re-pull covers any gap.
- **Not exposed:** the service is outbound-only (R2Z2, Postgres). No public domain / no
  published ports; `/healthz` is reached over Coolify's internal network only.

### One-time prod bootstrap (run as a superuser on the `eve` db)

Creates the collector's login role, the `killmails` schema it owns, and read-for-everyone
grants that also cover tables created later (mirrors how `sde` is set up):

```sql
CREATE ROLE zkillmanager LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
  PASSWORD 'REPLACE_WITH_STRONG_PASSWORD';
ALTER ROLE zkillmanager SET search_path = killmails, public;
CREATE SCHEMA IF NOT EXISTS killmails AUTHORIZATION zkillmanager;
GRANT USAGE  ON SCHEMA killmails TO PUBLIC;
GRANT SELECT ON ALL TABLES IN SCHEMA killmails TO PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE zkillmanager IN SCHEMA killmails
  GRANT SELECT ON TABLES TO PUBLIC;
```

The migrations (run by `zkillmanager`) then create the tables; `ALTER DEFAULT PRIVILEGES`
means readers (lscan) see them automatically.

## Local development

Dev shares one Postgres cluster with helion (the `eve-dev` compose: `127.0.0.1:55432`, db
`eve`, superuser `eve`) — `killmails` is just another schema in the same DB.

- **Reference data:** `DUMP_SCHEMAS=sde ~/sync-prod-db.sh` pulls the static `sde` schema from
  prod. The script also ensures the `zkillmanager` role and an empty owned `killmails` schema
  exist locally (mirroring the prod bootstrap).
- **Killmail data:** don't dump it from prod — regenerate it locally by running the collector
  with `RETENTION_DAYS=3` (+ `BACKFILL_ON_START=true`), which pulls only the last 3 R2 dumps.
  Tiny, and it exercises the real ingest path.
- **lscan** then reads the local `killmails` schema read-only, exactly as in prod.
