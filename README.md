# zkillmanager

Collects EVE Online killmails from [zKillboard](https://zkillboard.com) into a shared
Postgres store, so read-only consumers — primarily **lscan** (the threat-profiling UI at
`/home/eve/lscan`) — can query and aggregate them without hitting zKillboard/ESI on their
request path.

- **What it does:** backfill + daily raw dumps + a RedisQ live tail → an immutable,
  append-only killmail store keyed by `killmail_id`.
- **Stack:** Go (single long-running service with an internal scheduler), Postgres,
  deployed on Coolify.

## Docs

- `PROJECT.md` — architecture, ingest flows, Postgres schema, deployment.
- `HANDOFF.md` — context + decisions + first steps. **Read this first if you're picking it up.**
- `docs/zkill-notes.md` — zKillboard API specifics, sizing, and prior art (localthreat).
- `AGENTS.md` — working conventions + Go language profile (pre-set).

## Status

Greenfield — design only, not yet scaffolded. See `HANDOFF.md` for first steps.
