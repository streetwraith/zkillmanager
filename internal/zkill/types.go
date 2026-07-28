// Package zkill contains clients and types for zKillboard's data sources.
package zkill

import "time"

// Killmail is one entry from an R2 raw dump or an R2Z2 ephemeral file: the plain ESI
// killmail (no zkb block). A zero CharacterID means "not a capsuleer" (NPC
// attacker, or a structure/NPC victim) -- the basis of the PvP filter.
type Killmail struct {
	KillmailID    int64      `json:"killmail_id"`
	KillmailTime  time.Time  `json:"killmail_time"`
	SolarSystemID int        `json:"solar_system_id"`
	Victim        Victim     `json:"victim"`
	Attackers     []Attacker `json:"attackers"`
}

// Victim is the loss side of a killmail. victim.items is deliberately NOT a field: it is
// ~54% of a raw dump's bytes (median 16 items/kill) and decoding it into structs dominated
// the backfill's per-day memory peak. Omitting it lets the JSON decoder skip it entirely --
// do not add an Items field without an actual consumer.
type Victim struct {
	CharacterID   int64     `json:"character_id"`
	CorporationID int64     `json:"corporation_id"`
	AllianceID    int64     `json:"alliance_id"`
	ShipTypeID    int       `json:"ship_type_id"`
	DamageTaken   int64     `json:"damage_taken"`
	Position      *Position `json:"position"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Attacker struct {
	CharacterID   int64 `json:"character_id"`
	CorporationID int64 `json:"corporation_id"`
	AllianceID    int64 `json:"alliance_id"`
	ShipTypeID    int   `json:"ship_type_id"`
	WeaponTypeID  int   `json:"weapon_type_id"`
	FinalBlow     bool  `json:"final_blow"`
	DamageDone    int64 `json:"damage_done"`
}
