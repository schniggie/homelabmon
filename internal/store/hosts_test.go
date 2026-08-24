package store

import (
	"context"
	"testing"
	"time"

	"github.com/dx111ge/homelabmon/internal/models"
)

func TestMarkStaleHostsOfflinePerTypeThresholds(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	agent := &models.Host{ID: "agent-1", Hostname: "agent-host", MonitorType: "agent", Status: "online"}
	passive := &models.Host{ID: "passive-1", Hostname: "passive-host", MonitorType: "passive", Status: "online"}
	if err := st.UpsertHost(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertHost(ctx, passive); err != nil {
		t.Fatal(err)
	}

	// Freshly upserted: nothing stale with any thresholds
	stale, err := st.MarkStaleHostsOffline(ctx, 150*time.Second, 690*time.Second)
	if err != nil || len(stale) != 0 {
		t.Fatalf("fresh hosts must not be stale: %v %v", stale, err)
	}

	// Age both hosts beyond the agent threshold but within the passive one
	old := time.Now().UTC().Add(-10 * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := st.DB().ExecContext(ctx, "UPDATE hosts SET last_seen = ?", old); err != nil {
		t.Fatal(err)
	}

	stale, err = st.MarkStaleHostsOffline(ctx, 150*time.Second, 690*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].ID != "agent-1" || stale[0].MonitorType != "agent" {
		t.Fatalf("only the agent host should be stale, got %+v", stale)
	}

	// The passive host must still be online
	h, _ := st.GetHost(ctx, "passive-1")
	if h.Status != "online" {
		t.Fatal("passive host must survive agent-threshold staleness")
	}

	// Age beyond the passive threshold: passive goes stale too
	ancient := time.Now().UTC().Add(-30 * time.Minute).Format("2006-01-02 15:04:05")
	if _, err := st.DB().ExecContext(ctx, "UPDATE hosts SET last_seen = ?, status = 'online'", ancient); err != nil {
		t.Fatal(err)
	}
	stale, err = st.MarkStaleHostsOffline(ctx, 150*time.Second, 690*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 2 {
		t.Fatalf("both hosts should be stale, got %+v", stale)
	}
}
