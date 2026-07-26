package ingest

import (
	"testing"

	"zkillmanager/internal/zkill"
)

func km(victimChar int64, attackers ...zkill.Attacker) zkill.Killmail {
	return zkill.Killmail{
		Victim:    zkill.Victim{CharacterID: victimChar, CorporationID: 1000, AllianceID: 2000},
		Attackers: attackers,
	}
}

var (
	player   = zkill.Attacker{CharacterID: 42, CorporationID: 9000, AllianceID: 8000}
	player2  = zkill.Attacker{CharacterID: 43, CorporationID: 9001, AllianceID: 8001}
	npc      = zkill.Attacker{CorporationID: 500019} // no character_id
	corpMate = zkill.Attacker{CharacterID: 99, CorporationID: 1000}
	alliMate = zkill.Attacker{CharacterID: 98, CorporationID: 7777, AllianceID: 2000}
)

func TestIsPvP(t *testing.T) {
	tests := []struct {
		name string
		in   zkill.Killmail
		want bool
	}{
		{"player v player", km(7, player), true},
		{"player + npc attackers", km(7, player, npc), true},
		{"npc-only attackers (ratting loss)", km(7, npc), false},
		{"structure/npc victim", km(0, player), false},
		{"no attackers", km(7), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPvP(tt.in); got != tt.want {
				t.Errorf("IsPvP = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlayerAttackerCount(t *testing.T) {
	if got := PlayerAttackerCount(km(7, player, npc, npc)); got != 1 {
		t.Errorf("PlayerAttackerCount(1 player + 2 NPC) = %d, want 1", got)
	}
	if got := PlayerAttackerCount(km(7, player, player2, npc)); got != 2 {
		t.Errorf("PlayerAttackerCount(2 players + 1 NPC) = %d, want 2", got)
	}
}

func TestAwox(t *testing.T) {
	if !Awox(km(7, corpMate)) {
		t.Error("same-corp attacker should be awox")
	}
	if !Awox(km(7, alliMate)) {
		t.Error("same-alliance attacker should be awox")
	}
	if Awox(km(7, player)) {
		t.Error("unrelated attacker should not be awox")
	}
}
