package zkill

import (
	"encoding/json"
	"testing"
)

// A raw-dump killmail carries victim.items (the bulk of the payload) and other ESI fields the
// pipeline doesn't keep. Decoding must ignore those and still populate the fields we use --
// this guards the deliberate omission of an Items field (see Victim) against a regression
// where a real, item-carrying dump fails to decode or loses a kept field.
func TestDecodeIgnoresItemsAndKeepsUsedFields(t *testing.T) {
	raw := []byte(`{
		"killmail_id": 12345,
		"killmail_time": "2026-07-01T12:34:56Z",
		"solar_system_id": 30000142,
		"victim": {
			"character_id": 7, "corporation_id": 8, "alliance_id": 9,
			"ship_type_id": 670, "damage_taken": 5000,
			"position": {"x": 1.5, "y": -2.0, "z": 3.0},
			"items": [
				{"item_type_id": 34, "flag": 5, "quantity_dropped": 100},
				{"item_type_id": 35, "flag": 5, "quantity_destroyed": 7, "singleton": 1}
			]
		},
		"attackers": [
			{"character_id": 42, "corporation_id": 43, "alliance_id": 44,
			 "ship_type_id": 588, "weapon_type_id": 2456, "final_blow": true, "damage_done": 5000},
			{"character_id": 0, "ship_type_id": 0}
		]
	}`)

	var km Killmail
	if err := json.Unmarshal(raw, &km); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if km.KillmailID != 12345 || km.SolarSystemID != 30000142 || km.KillmailTime.IsZero() {
		t.Errorf("top-level wrong: id=%d system=%d time=%v", km.KillmailID, km.SolarSystemID, km.KillmailTime)
	}
	v := km.Victim
	if v.CharacterID != 7 || v.CorporationID != 8 || v.AllianceID != 9 || v.ShipTypeID != 670 {
		t.Errorf("victim ids wrong: %+v", v)
	}
	if v.Position == nil || v.Position.X != 1.5 || v.Position.Z != 3.0 {
		t.Errorf("position not decoded: %+v", v.Position)
	}
	if len(km.Attackers) != 2 || km.Attackers[0].CharacterID != 42 || !km.Attackers[0].FinalBlow {
		t.Errorf("attackers wrong: %+v", km.Attackers)
	}
}
