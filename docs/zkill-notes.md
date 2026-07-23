# zKillboard API notes + prior art

## Endpoints (moved to Cloudflare R2 in Jan 2026)

- **History dumps** — `https://r2z2.zkillboard.com/history/`
  - `totals.json` — `{ "YYYYMMDD": count }`, killmails per day (data starts ~2007-12-05).
  - `YYYYMMDD.json` — `{ killmail_id: hash }` (id + hash index).
  - `raw/YYYYMMDD.json` — full killmails (victim, attackers, items) for that day.
- **RedisQ (live)** — `https://zkillredisq.stream/listen.php?queueID=<stable-unique>`:
  long-poll, one full-killmail package at a time, remembers the queue ~3h. One consumer
  per queueID.
- **Per-character (fallback top-up)** — `https://zkillboard.com/api/kills/characterID/{id}/`.
- **Aggregate stats (all-time reputation)** — `https://zkillboard.com/api/stats/characterID/{id}/`:
  shipsDestroyed/Lost, dangerRatio, gangRatio, top ship types. Cache per pilot with a TTL.
- **Websocket** — `wss://zkillboard.com/websocket/` exists but is flagged possibly
  deprecated; RedisQ / R2 polling is the current direction.
- **Etiquette** — descriptive `User-Agent` on every request; reasonable spacing.

## Immutability & data size

Killmails never change (permanent ESI records) → cache forever keyed by `killmail_id`. A
past day's list can *grow* (late submissions) but existing entries never change, so re-pull
the last ~2–3 days on the daily job as a completeness backstop.

Volume (~2007–present): roughly **70–100M** killmails total (compute exactly from
`totals.json`); recent ~15–35k/day. Raw storage for a rolling window: **~single-digit GB
(90d)** to **low tens of GB (1y)** uncompressed (~3 KB/killmail avg); the `participation`
index adds more. Disk is not the constraint.

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
