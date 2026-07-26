// Package ingest turns raw killmails into stored rows: PvP filter -> SDE enrich ->
// upsert. The dumps carry no zkb block, so the flags are derived from the ESI data.
package ingest

import (
	"context"
	"log/slog"
	"time"

	"zkillmanager/internal/enrich"
	"zkillmanager/internal/store"
	"zkillmanager/internal/zkill"
)

// Resolver classifies a solar system (backed by the SDE). A miss means the system is
// abyssal/void or unknown -- the kill is skipped.
type Resolver interface {
	Resolve(systemID int) (enrich.System, bool)
}

// Writer persists a killmail and its player-attacker rows.
type Writer interface {
	UpsertKillmail(ctx context.Context, km store.KillmailRow, attackers []store.AttackerRow) error
}

// DayFetcher pulls a full day's killmails (the R2 raw-dump client).
type DayFetcher interface {
	FetchDay(ctx context.Context, day time.Time) (map[int64]zkill.Killmail, error)
}

type Ingester struct {
	resolver Resolver
	writer   Writer
	log      *slog.Logger
}

func New(resolver Resolver, writer Writer, log *slog.Logger) *Ingester {
	return &Ingester{resolver: resolver, writer: writer, log: log}
}

// Process runs one killmail through the pipeline. Non-PvP kills and kills in an
// unclassified space are skipped (nil error); only real failures return an error, so it
// satisfies zkill.Handler for the live tail.
func (ing *Ingester) Process(ctx context.Context, km zkill.Killmail) error {
	if !IsPvP(km) {
		return nil
	}
	sys, ok := ing.resolver.Resolve(km.SolarSystemID)
	if !ok {
		ing.log.Debug("skip: unclassified system", "killmail_id", km.KillmailID, "system_id", km.SolarSystemID)
		return nil
	}
	kmRow, attackers := buildRows(km, sys)
	return ing.writer.UpsertKillmail(ctx, kmRow, attackers)
}

// Day fetches a full day and processes every killmail. Returns the number fetched.
func (ing *Ingester) Day(ctx context.Context, fetcher DayFetcher, day time.Time) (int, error) {
	kms, err := fetcher.FetchDay(ctx, day)
	if err != nil {
		return 0, err
	}
	for _, km := range kms {
		if err := ing.Process(ctx, km); err != nil {
			return 0, err
		}
	}
	return len(kms), nil
}

// buildRows is the pure transform from a raw killmail + resolved system to stored rows.
func buildRows(km zkill.Killmail, sys enrich.System) (store.KillmailRow, []store.AttackerRow) {
	row := store.KillmailRow{
		KillmailID:       km.KillmailID,
		KillmailTime:     km.KillmailTime,
		SolarSystemID:    km.SolarSystemID,
		ConstellationID:  sys.ConstellationID,
		RegionID:         sys.RegionID,
		SpaceType:        int16(sys.SpaceType),
		VictimCharID:     km.Victim.CharacterID,
		VictimCorpID:     nz(km.Victim.CorporationID),
		VictimAllianceID: nz(km.Victim.AllianceID),
		VictimShipTypeID: nz(km.Victim.ShipTypeID),
		AttackerCount:    PlayerAttackerCount(km),
		Awox:             Awox(km),
	}
	if p := km.Victim.Position; p != nil {
		row.PosX, row.PosY, row.PosZ = &p.X, &p.Y, &p.Z
	}

	attackers := make([]store.AttackerRow, 0, row.AttackerCount)
	for _, a := range km.Attackers {
		if !isPlayer(a) {
			continue // NPC attacker: no kills row
		}
		attackers = append(attackers, store.AttackerRow{
			CharacterID: a.CharacterID,
			ShipTypeID:  nz(a.ShipTypeID),
			FinalBlow:   a.FinalBlow,
			CorpID:      nz(a.CorporationID),
			AllianceID:  nz(a.AllianceID),
		})
	}
	return row, attackers
}

// nz maps a zero EVE id (which means "absent") to a nil pointer -> SQL NULL.
func nz[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

// --- pure derivations (no zkb block in the dumps, so we compute these ourselves) ---

func isPlayer(a zkill.Attacker) bool { return a.CharacterID != 0 }

// PlayerAttackerCount counts capsuleer attackers (NPCs excluded); the stored
// attacker_count, and what drives avg gang size.
func PlayerAttackerCount(km zkill.Killmail) int {
	n := 0
	for _, a := range km.Attackers {
		if isPlayer(a) {
			n++
		}
	}
	return n
}

// IsPvP reports whether the kill has a player victim AND at least one player attacker.
func IsPvP(km zkill.Killmail) bool {
	if km.Victim.CharacterID == 0 {
		return false
	}
	for _, a := range km.Attackers {
		if isPlayer(a) {
			return true
		}
	}
	return false
}

// Awox is true when a player attacker shares the victim's corporation or alliance.
func Awox(km zkill.Killmail) bool {
	v := km.Victim
	for _, a := range km.Attackers {
		if !isPlayer(a) {
			continue
		}
		if v.CorporationID != 0 && a.CorporationID == v.CorporationID {
			return true
		}
		if v.AllianceID != 0 && a.AllianceID == v.AllianceID {
			return true
		}
	}
	return false
}
