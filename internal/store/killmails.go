package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"zkillmanager/internal/enrich"
)

// KillmailRow is one row of killmails.zkillboard_killmails. Pointer fields map to
// nullable columns (nil -> NULL); total_value is deferred so it's always NULL for now.
type KillmailRow struct {
	KillmailID       int64
	KillmailTime     time.Time
	SolarSystemID    int
	ConstellationID  int
	RegionID         int
	SpaceType        int16
	PosX, PosY, PosZ *float64
	VictimCharID     int64
	VictimCorpID     *int64
	VictimAllianceID *int64
	VictimShipTypeID *int
	AttackerCount    int
	Awox             bool
}

// AttackerRow is the per-player-attacker part of a killmails.kills row; the shared
// killmail-level fields are copied from the KillmailRow at insert time.
type AttackerRow struct {
	CharacterID int64
	ShipTypeID  *int
	FinalBlow   bool
	CorpID      *int64
	AllianceID  *int64
}

// LoadSystemRecords reads the SDE system->space lookup (map_solar_systems joined to
// map_regions for the region wormhole-class). LEFT JOIN + COALESCE keeps a system with a
// NULL-class region (Syndicate, Zarzakh, ...) in the set, classified later by security.
func (s *Store) LoadSystemRecords(ctx context.Context) ([]enrich.SystemRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s._key, s.constellation_id, s.region_id,
		       COALESCE(r.wormhole_class_id, 0), s.security_status
		FROM sde.map_solar_systems s
		LEFT JOIN sde.map_regions r ON r._key = s.region_id`)
	if err != nil {
		return nil, fmt.Errorf("load systems: %w", err)
	}
	defer rows.Close()

	var out []enrich.SystemRecord
	for rows.Next() {
		var rec enrich.SystemRecord
		if err := rows.Scan(&rec.SystemID, &rec.ConstellationID, &rec.RegionID,
			&rec.RegionWormholeClass, &rec.Security); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

const insertKillmailSQL = `
INSERT INTO killmails.zkillboard_killmails
 (killmail_id, killmail_time, solar_system_id, constellation_id, region_id, space_type,
  pos_x, pos_y, pos_z, victim_char_id, victim_corp_id, victim_alliance_id,
  victim_ship_type_id, attacker_count, awox)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
ON CONFLICT (killmail_id) DO NOTHING`

const insertKillSQL = `
INSERT INTO killmails.kills
 (character_id, killmail_id, killmail_time, ship_type_id, victim_ship_type_id, final_blow,
  solar_system_id, constellation_id, region_id, space_type, attacker_count,
  attacker_corp_id, attacker_alliance_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (character_id, killmail_id) DO NOTHING`

// UpsertKillmail writes a killmail and its player-attacker rows in one transaction.
// Idempotent (ON CONFLICT DO NOTHING), so dumps / live feed / backfill overlap harmlessly.
func (s *Store) UpsertKillmail(ctx context.Context, km KillmailRow, attackers []AttackerRow) error {
	batch := &pgx.Batch{}
	batch.Queue(insertKillmailSQL,
		km.KillmailID, km.KillmailTime, km.SolarSystemID, km.ConstellationID, km.RegionID,
		km.SpaceType, km.PosX, km.PosY, km.PosZ, km.VictimCharID, km.VictimCorpID,
		km.VictimAllianceID, km.VictimShipTypeID, km.AttackerCount, km.Awox)
	for _, a := range attackers {
		batch.Queue(insertKillSQL,
			a.CharacterID, km.KillmailID, km.KillmailTime, a.ShipTypeID, km.VictimShipTypeID,
			a.FinalBlow, km.SolarSystemID, km.ConstellationID, km.RegionID, km.SpaceType,
			km.AttackerCount, a.CorpID, a.AllianceID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	br := tx.SendBatch(ctx, batch)
	for range batch.Len() {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("upsert killmail %d: %w", km.KillmailID, err)
		}
	}
	if err := br.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LoadStealthTypeIDs expands the given SDE ship groups to their member type ids.
func (s *Store) LoadStealthTypeIDs(ctx context.Context, groupIDs []int) ([]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT _key FROM sde.types WHERE group_id = ANY($1)`, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("load stealth types: %w", err)
	}
	defer rows.Close()

	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SyncStealthShips replaces killmails.stealth_ship with the given (de-duplicated) type
// ids in one transaction. Returns the number of distinct hulls written.
func (s *Store) SyncStealthShips(ctx context.Context, typeIDs []int) (int, error) {
	seen := make(map[int]struct{}, len(typeIDs))
	uniq := make([]int, 0, len(typeIDs))
	for _, id := range typeIDs {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			uniq = append(uniq, id)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `TRUNCATE killmails.stealth_ship`); err != nil {
		return 0, err
	}
	if len(uniq) > 0 {
		_, err := tx.CopyFrom(ctx,
			pgx.Identifier{"killmails", "stealth_ship"}, []string{"type_id"},
			pgx.CopyFromSlice(len(uniq), func(i int) ([]any, error) { return []any{uniq[i]}, nil }))
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(uniq), nil
}

// LoadDumpProgress reads the per-day recorded zkill totals (keyed by UTC day).
func (s *Store) LoadDumpProgress(ctx context.Context) (map[time.Time]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT day, zkill_total FROM killmails.dump_progress`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[time.Time]int)
	for rows.Next() {
		var day time.Time
		var total int
		if err := rows.Scan(&day, &total); err != nil {
			return nil, err
		}
		out[day.UTC()] = total
	}
	return out, rows.Err()
}

// SetDumpProgress records the zkill total for a day after (re-)pulling its dump.
func (s *Store) SetDumpProgress(ctx context.Context, day time.Time, total int) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO killmails.dump_progress (day, zkill_total, pulled_at) VALUES ($1, $2, now())
		 ON CONFLICT (day) DO UPDATE SET zkill_total = EXCLUDED.zkill_total, pulled_at = now()`,
		day, total)
	return err
}

// PruneOlderThan deletes killmails (cascading to their kills rows) and dump_progress
// markers older than the retention window. The cutoff matches Sync's midnight horizon
// (today - retentionDays), so the boundary day keeps its marker -- a wall-clock cutoff
// would drop it and force a needless full re-pull every prune cycle. Returns killmails deleted.
func (s *Store) PruneOlderThan(ctx context.Context, retentionDays int) (int64, error) {
	horizon := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -retentionDays)
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM killmails.zkillboard_killmails WHERE killmail_time < $1`, horizon)
	if err != nil {
		return 0, fmt.Errorf("prune killmails: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM killmails.dump_progress WHERE day < $1`, horizon); err != nil {
		return tag.RowsAffected(), fmt.Errorf("prune dump_progress: %w", err)
	}
	return tag.RowsAffected(), nil
}

// GetState reads an ingest_state value.
func (s *Store) GetState(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM killmails.ingest_state WHERE key = $1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetState upserts an ingest_state value.
func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO killmails.ingest_state (key, value, updated_at) VALUES ($1, $2, now())
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, value)
	return err
}
