package store

import (
	"context"
	"fmt"
	"time"
)

const (
	// podPairWindow is both how long a pod waits before we process it (by then its ship kill
	// has almost certainly arrived -- 99.3% of pods land within 15 min of the ship loss) and
	// the maximum look-back to the ship kill it followed.
	podPairWindow = 15 * time.Minute
	// podBatch bounds one pass so a large backlog (e.g. right after a backfill) drains in
	// bounded transactions instead of one enormous one.
	podBatch = 1000
)

// The capsule ("pod") victim type ids: Capsule and the Genolution variant, the only two that
// ever appear as a player victim. Inlined as SQL literals (not a bind parameter) so the
// planner can match migration 0003's partial index, whose predicate is these same literals.
// Keep both sites in sync.
const podVictimFilter = `victim_ship_type_id IN (670, 33328)`

// flagPodPairsSQL flags with_pod on the attackers who were on BOTH a pod and the ship kill it
// followed -- i.e. the pilots who actually finished the victim's pod, not every fleet-mate on
// the ship kill. eligible is the same set (same predicate, same deterministic order + limit)
// the delete below removes, so flag-then-delete in one transaction pairs every pod exactly
// once. Ordering flag before delete lets the correlated subquery still read the pod's
// attacker rows, which the delete's cascade then removes.
const flagPodPairsSQL = `
WITH eligible AS (
    SELECT killmail_id, victim_char_id, solar_system_id, killmail_time
    FROM killmails.zkillboard_killmails
    WHERE ` + podVictimFilter + `
      AND killmail_time < now() - make_interval(mins => $1)
    ORDER BY killmail_time, killmail_id
    LIMIT $2
),
pairs AS (
    SELECT e.killmail_id AS pod_id, s.ship_id
    FROM eligible e
    JOIN LATERAL (
        SELECT sk.killmail_id AS ship_id
        FROM killmails.zkillboard_killmails sk
        WHERE sk.victim_char_id = e.victim_char_id
          AND sk.solar_system_id = e.solar_system_id
          AND NOT (sk.` + podVictimFilter + `)
          AND sk.killmail_time <= e.killmail_time
          AND sk.killmail_time > e.killmail_time - make_interval(mins => $1)
        ORDER BY e.killmail_time - sk.killmail_time
        LIMIT 1
    ) s ON true
)
UPDATE killmails.kills k
SET with_pod = true
FROM pairs p
WHERE k.killmail_id = p.ship_id
  AND k.character_id IN (SELECT character_id FROM killmails.kills WHERE killmail_id = p.pod_id)`

// deletePodsSQL removes the same eligible pods (paired or not -- a pod with no preceding ship
// kill is normal), cascading to their kills rows.
const deletePodsSQL = `
DELETE FROM killmails.zkillboard_killmails
WHERE killmail_id IN (
    SELECT killmail_id
    FROM killmails.zkillboard_killmails
    WHERE ` + podVictimFilter + `
      AND killmail_time < now() - make_interval(mins => $1)
    ORDER BY killmail_time, killmail_id
    LIMIT $2
)`

// PairPods runs the pod-pairing pass to completion: repeatedly flag the "finished the pod"
// attackers on each pod older than podPairWindow, then delete the pod. Returns pods removed.
//
// The caller MUST serialize this against dump ingest (backfill/reconcile): a bulk day insert
// writes ship kills and pods in arbitrary order, and pairing a pod before its ship kill is
// stored would drop the pairing permanently (the pod is then deleted). Live-fed pods are
// safe on their own -- the ship kill always arrives well before the pod is podPairWindow old.
func (s *Store) PairPods(ctx context.Context) (int, error) {
	mins := int(podPairWindow.Minutes())
	total := 0
	for {
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return total, err
		}
		if _, err := tx.Exec(ctx, flagPodPairsSQL, mins, podBatch); err != nil {
			_ = tx.Rollback(ctx)
			return total, fmt.Errorf("flag pod pairs: %w", err)
		}
		tag, err := tx.Exec(ctx, deletePodsSQL, mins, podBatch)
		if err != nil {
			_ = tx.Rollback(ctx)
			return total, fmt.Errorf("delete pods: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return total, fmt.Errorf("commit pod pairing: %w", err)
		}

		n := int(tag.RowsAffected())
		total += n
		if n < podBatch {
			return total, nil // short batch -> backlog drained
		}
	}
}
