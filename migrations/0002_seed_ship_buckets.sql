-- 0002_seed_ship_buckets: initial attacker_bucket / victim_bucket seed.
-- Generated from attacker_ship_groups.txt and victim_ship_groups.txt. Groups expand to their
-- published SDE hulls at apply time (sde is in the same DB); explicit hulls are inserted
-- first so they win over their group, and the catch-all is inserted last. ON CONFLICT DO
-- NOTHING enforces precedence: explicit type > group > rest.
--
-- Re-seeding later (after editing the .txt seeds or an SDE update) is a fresh migration or a
-- manual truncate+reinsert; a hull added to the SDE after this runs stays unclassified until
-- then.

-- ============================ attacker_bucket ============================

-- explicit single-hull buckets (highest precedence)
INSERT INTO killmails.attacker_bucket (type_id, bucket) VALUES
  (77114, 'Metamorphosis'),
  (44993, 'Pacifier'),
  (33468, 'Astero'),
  (33470, 'Stratios'),
  (85086, 'Cenotaph'),
  (85087, 'Tholos'),
  (89607, 'Odysseus')
ON CONFLICT (type_id) DO NOTHING;

-- group-defined buckets (each group expands to its published hulls)
INSERT INTO killmails.attacker_bucket (type_id, bucket)
SELECT t._key, b.bucket
FROM (VALUES
  (834::int, 'Stealth Bombers'),
  (830, 'Covert Ops'),
  (833, 'Force Recon Ship'),
  (906, 'Combat Recon Ship'),
  (963, 'Strategic Cruiser'),
  (898, 'Black Ops'),
  (541, 'Interdictors'),
  (894, 'Heavy Interdiction Cruisers'),
  (25, 'Frigates'), (324, 'Frigates'), (831, 'Frigates'), (893, 'Frigates'), (1527, 'Frigates'),
  (420, 'Destroyers'), (1534, 'Destroyers'), (1305, 'Destroyers'),
  (26, 'Cruisers'), (358, 'Cruisers'), (832, 'Cruisers'), (1972, 'Cruisers'),
  (419, 'Battlecruisers'), (1201, 'Battlecruisers'), (540, 'Battlecruisers'),
  (27, 'Battleships'), (900, 'Battleships'),
  (30, 'Capitals'), (485, 'Capitals'), (4594, 'Capitals'), (547, 'Capitals'),
  (5120, 'Capitals'), (659, 'Capitals'), (1538, 'Capitals')
) AS b(group_id, bucket)
JOIN sde.types t ON t.group_id = b.group_id AND t.published
ON CONFLICT (type_id) DO NOTHING;

-- rest -> Others (every published ship hull not yet bucketed)
INSERT INTO killmails.attacker_bucket (type_id, bucket)
SELECT t._key, 'Others'
FROM sde.types t
JOIN sde.groups g ON g._key = t.group_id
WHERE g.category_id = 6 AND t.published
ON CONFLICT (type_id) DO NOTHING;

-- ============================ victim_bucket ============================

-- explicit hulls (highest precedence)
INSERT INTO killmails.victim_bucket (type_id, bucket) VALUES
  (32880, 'Small miners'), (89648, 'Small miners'),                       -- Ventures
  (89240, 'Small miners'), (91174, 'Small miners'), (89647, 'Small miners'), -- mining Destroyers
  (89649, 'Small miners'),                                                -- Outrider
  (29248, 'Explorers'), (605, 'Explorers'), (607, 'Explorers'), (586, 'Explorers'),          -- explorer Frigates
  (72907, 'Explorers'), (72904, 'Explorers'), (72913, 'Explorers'), (72903, 'Explorers'),    -- Navy versions
  (11188, 'Explorers'), (11192, 'Explorers'), (11182, 'Explorers'), (11172, 'Explorers'), (44993, 'Explorers'), -- Covert Ops
  (33468, 'Explorers'), (77114, 'Explorers'), (33472, 'Explorers'), (89607, 'Explorers')     -- Astero, Metamorphosis, Nestor, Odysseus
ON CONFLICT (type_id) DO NOTHING;

-- group-defined buckets
INSERT INTO killmails.victim_bucket (type_id, bucket)
SELECT t._key, b.bucket
FROM (VALUES
  (1283::int, 'Small miners'),                                            -- Expedition Frigates
  (463, 'Big miners'), (543, 'Big miners'), (941, 'Big miners'), (883, 'Big miners'),
  (28, 'Haulers'), (380, 'Haulers'), (1202, 'Haulers'), (513, 'Haulers'), (902, 'Haulers'),
  (237, 'Other'), (31, 'Other'), (5087, 'Other')
) AS b(group_id, bucket)
JOIN sde.types t ON t.group_id = b.group_id AND t.published
ON CONFLICT (type_id) DO NOTHING;

-- rest -> Combat ships (every published ship hull not yet bucketed)
INSERT INTO killmails.victim_bucket (type_id, bucket)
SELECT t._key, 'Combat ships'
FROM sde.types t
JOIN sde.groups g ON g._key = t.group_id
WHERE g.category_id = 6 AND t.published
ON CONFLICT (type_id) DO NOTHING;
