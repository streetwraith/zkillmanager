package enrich

import "testing"

func TestResolver(t *testing.T) {
	records := []SystemRecord{
		{SystemID: 30000142, ConstellationID: 20000020, RegionID: 10000002, RegionWormholeClass: 7, Security: 0.946}, // Jita
		{SystemID: 31000401, ConstellationID: 21000048, RegionID: 11000004, RegionWormholeClass: 2, Security: -0.99}, // J132557 (C2)
		{SystemID: 30000001, ConstellationID: 20000001, RegionID: 10000041, RegionWormholeClass: 0, Security: -0.06}, // Syndicate (NULL class)
		{SystemID: 32000001, ConstellationID: 22000001, RegionID: 12000001, RegionWormholeClass: 19, Security: -1.0}, // abyssal -> dropped
	}
	r := NewResolver(records)

	if r.Len() != 3 {
		t.Fatalf("Len = %d, want 3 (abyssal excluded)", r.Len())
	}

	cases := []struct {
		sysID int
		want  SpaceType
		ok    bool
	}{
		{30000142, Highsec, true},
		{31000401, Wormhole, true},
		{30000001, Nullsec, true}, // NULL-class region classified by security
		{32000001, 0, false},      // abyssal: not in the map
		{99999999, 0, false},      // unknown system
	}
	for _, c := range cases {
		got, ok := r.Resolve(c.sysID)
		if ok != c.ok || (ok && got.SpaceType != c.want) {
			t.Errorf("Resolve(%d) = (%+v, %v), want space=%d ok=%v", c.sysID, got, ok, c.want, c.ok)
		}
	}
}
