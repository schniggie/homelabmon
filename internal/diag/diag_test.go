package diag

import "testing"

func TestRingRecordsAndCaps(t *testing.T) {
	events = nil // reset ring

	Record("startup", "node started", "version", "test")
	Record("host_offline", "nas went offline", "host", "nas")

	recent := Recent(10)
	if len(recent) != 2 {
		t.Fatalf("expected 2 events, got %d", len(recent))
	}
	if recent[0].Kind != "startup" || recent[1].Fields[0].Key != "host" {
		t.Errorf("events wrong: %+v", recent)
	}

	// ring caps at ringSize
	for i := 0; i < ringSize+50; i++ {
		Record("noise", "x")
	}
	recent = Recent(10)
	if len(recent) != 10 || recent[0].Kind != "noise" {
		t.Errorf("ring cap failed: %+v", recent)
	}
	if all := Recent(0); len(all) != ringSize {
		t.Errorf("expected full ring of %d, got %d", ringSize, len(all))
	}
}
