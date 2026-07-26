-- 0001_init: the killmails tables.
-- The schema itself + the zkillmanager role + read grants are created once by the bootstrap
-- (see PROJECT.md "One-time prod bootstrap") -- the app role cannot (no CREATE on the
-- database). ALTER DEFAULT PRIVILEGES in the bootstrap means every table created here is
-- automatically SELECT-able by readers (lscan), so migrations carry no grants.

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
    total_value         BIGINT               -- not sourced yet (see PROJECT.md "Stats")
);
CREATE INDEX zkb_killmails_time_brin   ON killmails.zkillboard_killmails USING BRIN (killmail_time);
CREATE INDEX zkb_killmails_victim_time ON killmails.zkillboard_killmails (victim_char_id, killmail_time DESC);

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
    space_type           SMALLINT NOT NULL,
    attacker_count       INT NOT NULL,        -- player attackers, for avg gang size (solo = count 1)
    attacker_corp_id     BIGINT,              -- attacker affiliation at kill time (historical snapshot)
    attacker_alliance_id BIGINT,
    PRIMARY KEY (character_id, killmail_id)
);
CREATE INDEX kills_char_time ON killmails.kills (character_id, killmail_time DESC);

-- custom, independent ship classifications; edited freely, joined at read
CREATE TABLE killmails.attacker_bucket (type_id INT PRIMARY KEY, bucket TEXT NOT NULL);
CREATE TABLE killmails.victim_bucket   (type_id INT PRIMARY KEY, bucket TEXT NOT NULL);

-- ingest bookkeeping (misc key/value markers)
CREATE TABLE killmails.ingest_state (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ship-attribute lookup, materialized from the seed by the collector, joined at read.
-- A hull's stealth-ness is a volatile classification (edited seed / new hulls), so it
-- lives here rather than as a flag baked onto every kills row.
CREATE TABLE killmails.stealth_ship (type_id INT PRIMARY KEY);

-- per-day dump completeness: zkill's totals.json count when we last pulled the day.
-- gap = current totals - zkill_total drives both backfill and daily reconciliation.
CREATE TABLE killmails.dump_progress (
    day         DATE PRIMARY KEY,
    zkill_total INTEGER NOT NULL,
    pulled_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
