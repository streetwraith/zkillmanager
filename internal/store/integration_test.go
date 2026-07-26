//go:build integration

// Integration tests hit a real Postgres (read-only). Run with:
//
//	ZKM_TEST_DSN="postgres://readonly@horizon:55432/eve" go test -tags integration ./internal/store/
package store_test

import (
	"context"
	"os"
	"testing"

	"zkillmanager/internal/enrich"
	"zkillmanager/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("ZKM_TEST_DSN")
	if dsn == "" {
		t.Skip("ZKM_TEST_DSN not set")
	}
	st, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestLoadSystemRecords(t *testing.T) {
	st := testStore(t)
	recs, err := st.LoadSystemRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) < 8000 {
		t.Fatalf("loaded %d systems, want >8000", len(recs))
	}
	r := enrich.NewResolver(recs)
	if r.Len() < 7000 {
		t.Fatalf("classified %d systems, want >7000", r.Len())
	}
	if s, ok := r.Resolve(30000142); !ok || s.SpaceType != enrich.Highsec { // Jita
		t.Errorf("Jita resolved to %+v ok=%v, want highsec", s, ok)
	}
}

func TestLoadStealthTypeIDs(t *testing.T) {
	st := testStore(t)
	ids, err := st.LoadStealthTypeIDs(context.Background(), enrich.StealthGroupIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) < 30 {
		t.Fatalf("loaded %d stealth-group types, want >=30", len(ids))
	}
}
