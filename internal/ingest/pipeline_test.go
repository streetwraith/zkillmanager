package ingest

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"zkillmanager/internal/enrich"
	"zkillmanager/internal/store"
	"zkillmanager/internal/zkill"
)

type fakeResolver struct {
	sys enrich.System
	ok  bool
}

func (f fakeResolver) Resolve(int) (enrich.System, bool) { return f.sys, f.ok }

type fakeWriter struct {
	kms  []store.KillmailRow
	atts [][]store.AttackerRow
}

func (w *fakeWriter) UpsertKillmail(_ context.Context, km store.KillmailRow, a []store.AttackerRow) error {
	w.kms = append(w.kms, km)
	w.atts = append(w.atts, a)
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fullKill() zkill.Killmail {
	return zkill.Killmail{
		KillmailID:    777,
		SolarSystemID: 30000142,
		Victim: zkill.Victim{
			CharacterID: 7, CorporationID: 1000, AllianceID: 2000, ShipTypeID: 670,
			Position: &zkill.Position{X: 1, Y: 2, Z: 3},
		},
		Attackers: []zkill.Attacker{
			{CharacterID: 42, CorporationID: 9000, ShipTypeID: 11567, FinalBlow: true},
			{CharacterID: 43, CorporationID: 9001, ShipTypeID: 587},
			{CorporationID: 500019, ShipTypeID: 202}, // NPC: no character_id
		},
	}
}

func TestBuildRows(t *testing.T) {
	sys := enrich.System{ConstellationID: 20000020, RegionID: 10000002, SpaceType: enrich.Highsec}
	km, attackers := buildRows(fullKill(), sys)

	if km.AttackerCount != 2 {
		t.Errorf("AttackerCount = %d, want 2 (NPC excluded)", km.AttackerCount)
	}
	if km.ConstellationID != 20000020 || km.RegionID != 10000002 || km.SpaceType != int16(enrich.Highsec) {
		t.Errorf("space fields not copied from resolver: %+v", km)
	}
	if km.VictimShipTypeID == nil || *km.VictimShipTypeID != 670 {
		t.Error("victim ship type not set")
	}
	if km.VictimAllianceID == nil || *km.VictimAllianceID != 2000 {
		t.Error("victim alliance not set")
	}
	if km.PosX == nil || *km.PosX != 1 || km.PosZ == nil || *km.PosZ != 3 {
		t.Error("victim position not copied")
	}
	if len(attackers) != 2 {
		t.Fatalf("attacker rows = %d, want 2", len(attackers))
	}
	if !attackers[0].FinalBlow || attackers[0].CharacterID != 42 {
		t.Errorf("first attacker row wrong: %+v", attackers[0])
	}
	if attackers[0].ShipTypeID == nil || *attackers[0].ShipTypeID != 11567 {
		t.Error("attacker ship type not set")
	}
	if attackers[0].CorpID == nil || *attackers[0].CorpID != 9000 {
		t.Error("attacker corp id not set")
	}
}

func TestProcess(t *testing.T) {
	classified := fakeResolver{sys: enrich.System{RegionID: 10000002, SpaceType: enrich.Highsec}, ok: true}

	t.Run("pvp + classified -> written", func(t *testing.T) {
		w := &fakeWriter{}
		ing := New(classified, w, discardLogger())
		if err := ing.Process(context.Background(), fullKill()); err != nil {
			t.Fatal(err)
		}
		if len(w.kms) != 1 || w.kms[0].KillmailID != 777 {
			t.Fatalf("expected one upsert of kill 777, got %+v", w.kms)
		}
	})

	t.Run("non-pvp -> skipped", func(t *testing.T) {
		w := &fakeWriter{}
		ing := New(classified, w, discardLogger())
		npcOnly := zkill.Killmail{Victim: zkill.Victim{CharacterID: 7}, Attackers: []zkill.Attacker{{CorporationID: 500019}}}
		if err := ing.Process(context.Background(), npcOnly); err != nil {
			t.Fatal(err)
		}
		if len(w.kms) != 0 {
			t.Error("non-PvP kill should not be written")
		}
	})

	t.Run("unclassified space -> skipped", func(t *testing.T) {
		w := &fakeWriter{}
		ing := New(fakeResolver{ok: false}, w, discardLogger())
		if err := ing.Process(context.Background(), fullKill()); err != nil {
			t.Fatal(err)
		}
		if len(w.kms) != 0 {
			t.Error("kill in unclassified space should not be written")
		}
	})
}
