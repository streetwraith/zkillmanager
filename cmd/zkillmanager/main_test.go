package main

import (
	"testing"

	"zkillmanager/internal/sentrytest"
)

// capturePanic has two halves: report the panic, and let it carry on killing the process.
// Swallowing it would leave a dead goroutine behind a /healthz that still passes, because
// the check only pings Postgres.
func TestCapturePanicReportsAndRepanics(t *testing.T) {
	sink := sentrytest.Capture(t)

	var escaped any
	func() {
		defer func() { escaped = recover() }()
		defer capturePanic("live")
		panic("live tail exploded")
	}()

	if escaped == nil {
		t.Fatal("capturePanic swallowed the panic; the process must still die")
	}
	events := sink.Captured()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Tags["component"] != "live" {
		t.Errorf("event tags = %v, want component=live", events[0].Tags)
	}
}
