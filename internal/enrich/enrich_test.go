package enrich

import "testing"

func TestClassifySpace(t *testing.T) {
	tests := []struct {
		name    string
		whClass int
		sec     float64
		want    SpaceType
		wantOK  bool
	}{
		{"highsec", 7, 0.946, Highsec, true},
		{"highsec boundary 0.45", 7, 0.45, Highsec, true},
		{"lowsec system in a class-7 region (Rancer)", 7, 0.36, Lowsec, true},
		{"lowsec class-8", 8, 0.365, Lowsec, true},
		{"nullsec class-9 negative", 9, -0.1, Nullsec, true},
		{"nullsec zero", 9, 0.0, Nullsec, true},
		{"C1", 1, -0.99, Wormhole, true},
		{"C6", 6, -0.99, Wormhole, true},
		{"Thera", 12, -0.99, Wormhole, true},
		{"C13 shattered", 13, -0.99, Wormhole, true},
		{"drifter", 15, -0.99, Wormhole, true},
		{"pochven", 25, -1.0, Pochven, true},
		{"abyssal skipped", 19, -1.0, 0, false},
		{"void skipped", 22, -1.0, 0, false},
		{"NULL-class NPC null (Syndicate/Zarzakh)", 0, -0.06, Nullsec, true},
		{"NULL-class highsec (Exordium)", 0, 1.0, Highsec, true},
		{"unexpected class falls back to security", 99, 0.5, Highsec, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ClassifySpace(tt.whClass, tt.sec)
			if ok != tt.wantOK || (ok && got != tt.want) {
				t.Errorf("ClassifySpace(%d, %v) = (%d, %v), want (%d, %v)",
					tt.whClass, tt.sec, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
