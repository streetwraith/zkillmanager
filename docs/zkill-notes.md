# zKillboard API notes + prior art

## Endpoints (moved to Cloudflare R2 in Jan 2026)

- **History dumps** — `https://r2z2.zkillboard.com/history/`
  - `totals.json` — `{ "YYYYMMDD": count }`, killmails per day (data starts ~2007-12-05).
  - `YYYYMMDD.json` — `{ killmail_id: hash }` (id + hash index).
  - `raw/YYYYMMDD.json` — full killmails for that day, a JSON object keyed by `killmail_id`.
    **Plain ESI killmails — no `zkb` block** (see "Raw dump contents" below).
- **R2Z2 live feed** (RedisQ replacement; **RedisQ sunset 2026-05-31**, its
  `zkillredisq.stream` host now resolves to 127.0.0.1) —
  `https://r2z2.zkillboard.com/ephemeral/sequence.json` gives the latest sequence id;
  `.../ephemeral/{seq}.json` is one killmail (`{esi, hash, zkb, sequence_id, uploaded_at}` —
  the ESI killmail lives under `esi`). Poll: iterate `seq++` until a 404 (caught up), sleep
  >=6s, repeat. Files expire ~24h; rate limit 15 req/s. Consumers track their own sequence
  (no queueID). Wiki: `github.com/zKillboard/zKillboard/wiki/API-(R2Z2)`.
- **Per-character (fallback top-up)** — `https://zkillboard.com/api/kills/characterID/{id}/`.
- **Aggregate stats (all-time reputation)** — `https://zkillboard.com/api/stats/characterID/{id}/`:
  shipsDestroyed/Lost, dangerRatio, gangRatio, top ship types. Cache per pilot with a TTL.
- **Websocket** — `wss://zkillboard.com/websocket/` exists but is flagged possibly
  deprecated; RedisQ / R2 polling is the current direction.
- **Etiquette** — descriptive `User-Agent` on every request; reasonable spacing.

## Raw dump contents (measured 2026-07-23, day 20260722)

The R2 raw dump is the **plain ESI killmail**, not zKill's enriched object. A value looks like
`{attackers[], killmail_id, killmail_time, solar_system_id, victim{character_id,
corporation_id, alliance_id, damage_taken, items[], position{x,y,z}, ship_type_id}}`. There
is **no `zkb` block** — so no `totalValue`, `npc`, `solo`, `awox`, `points`, `labels`, or
`hash`. The R2Z2 ephemeral (live) files *do* include `zkb` + `hash`; the daily/backfill
dumps do not — we derive the flags ourselves either way.

Implications (zkillmanager derives these itself, so live and backfilled rows match):
- `npc` — no attacker has `character_id`.
- `solo` — exactly one player attacker; computed at read as `attacker_count = 1`, not stored.
- `awox` — a player attacker shares the victim's corp/alliance.
- `total_value` — **not derivable** (ESI carries no ISK). Deferred: omit or fetch from zKill.
- `hash` — only in `history/YYYYMMDD.json`; unneeded since the dump already has the full mail.

One measured day (20260722): 13,282 kills, 28.8 MB, avg 2,159 B/kill. Byte breakdown:
`victim.items` 54.3% (median 16 items, max 639), `attackers[]` 31.0% (median 2, p99 34, max
129), `victim.position` 3.2%. 12.6% had no player attacker; 40.7% had exactly one; 98.5% had
a player victim. zkillmanager stores only player-vs-player kills, drops `victim.items`, keeps
`victim.position`, keeps only the attacker *count* plus derived `kills` rows, and stores no
raw JSONB.

All-time volume from `totals.json`: 94,395,956 killmails since 2007-12-05; recent ~16k/day
(90d ≈ 1.55M, 1y ≈ 6.0M).

## Immutability & data size

Killmails never change (permanent ESI records) → cache forever keyed by `killmail_id`. A
past day's list can *grow* (late submissions) but existing entries never change. We record
zkill's per-day total (`totals.json`) at each pull and re-pull the days whose total has since
grown (the biggest gaps), so late growth on *any* day is caught — not just recent ones.

Volume: see the measured figures above (94.4M all-time, ~16k/day). Because we store only
player-vs-player kills as typed columns — no raw JSONB, no `victim.items` — on-disk size is a
small fraction of the ~3 KB/killmail raw dump; a rolling window stays well under a GB plus the
`kills` index. Disk is not the constraint.

`weapon_type_id` on attackers resolves (via `sde` / ESI `universe/types`) to the
weapon/module (or drone/fighter) used — often equals `ship_type_id` when there is no
distinct weapon.

## Prior art: localthreat

[localthreat](https://github.com/haggen/localthreat) is a mature same-premise tool built
the *opposite* way: a thin backend (Bun + SQLite) that stores only the pasted name-list as
a shareable "report", with **all** EVE data fetched *client-side, in the browser* — ESI
(ids / affiliation / names) + zKill's **stats** endpoint (aggregate per-character only). A
100ms client-side queue rate-limits; caching is in-memory plus a `localStorage` report
history; nothing is persisted server-side.

That works because localthreat only needs aggregate stats (CORS-open, browser-fetchable).
lscan + zkillmanager want the full **killmail history** to aggregate what/where/how a pilot
kills — the heavier zKill use case (wants a `User-Agent` + spacing, which browsers can't
set) — which is exactly why we run a server-side collector with a shared, immutable,
cache-forever store.
