-- 0003_pod_pairing: record "did this pilot finish the victim's pod" per attacker, and index
-- the pod lookup the pairing pass depends on.
--
-- with_pod is set AFTER insert by the pairing pass (see internal/store PairPods): once both
-- the ship kill and the following pod are stored, the attackers present on both are flagged
-- and the pod is deleted. This deliberately ends strict append-only (a stored row's flag
-- flips) -- an accepted consequence of no longer retaining pod killmails.
ALTER TABLE killmails.kills ADD COLUMN with_pod boolean NOT NULL DEFAULT false;

-- Index kills by killmail_id. The PK is (character_id, killmail_id), so killmail_id alone
-- can't be looked up by it. Pod pairing needs exactly that -- to find a kill's attacker rows
-- by killmail_id (both the ship kill it flags and the pod it reads) -- and so does the FK
-- cascade that deletes a pod's kills rows on delete. Without it every pairing batch and every
-- cascade seq-scans the whole kills table. (Also speeds readers' co-attacker joins.)
CREATE INDEX kills_killmail ON killmails.kills (killmail_id);

-- The pairing pass scans for capsule victims older than its window every few minutes,
-- forever, over the full retention table. A partial index covering only the two capsule
-- victim type ids keeps that an index lookup instead of a full-table scan. Keep the id list
-- in sync with the pairing query in internal/store/pods.go.
CREATE INDEX zkb_killmails_pods ON killmails.zkillboard_killmails (killmail_time)
  WHERE victim_ship_type_id IN (670, 33328);
