package store

import (
	"context"
	"testing"
	"time"
)

func TestMemoryCRUD(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()

	node := &Memory{HostID: "host-1", Hostname: "web1", Scope: "node:host-1", Kind: "action",
		Title: "Restarted nginx", Detail: "config reload after cert renewal", Source: "agent", CreatedAt: time.Now().UTC()}
	if err := st.InsertMemory(ctx, node); err != nil {
		t.Fatalf("insert node memory: %v", err)
	}
	global := &Memory{Scope: "global", Kind: "note",
		Title: "Homelab convention", Detail: "upgrades run one host at a time", Source: "llm", CreatedAt: time.Now().UTC()}
	if err := st.InsertMemory(ctx, global); err != nil {
		t.Fatalf("insert global memory: %v", err)
	}

	// per-host listing includes homelab-wide entries
	list, err := st.ListMemories(ctx, "host-1", "", 50)
	if err != nil || len(list) != 2 {
		t.Fatalf("expected 2 memories for host-1 (node + global), got %d err %v", len(list), err)
	}

	// get + update
	got, err := st.GetMemory(ctx, node.ID)
	if err != nil || got == nil || got.Title != "Restarted nginx" {
		t.Fatalf("get memory: %+v err %v", got, err)
	}
	if err := st.UpdateMemory(ctx, node.ID, "Restarted nginx (edited)", "user corrected: it was a full restart"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ = st.GetMemory(ctx, node.ID)
	if got.Title != "Restarted nginx (edited)" || got.Detail != "user corrected: it was a full restart" {
		t.Fatalf("update not applied: %+v", got)
	}
	// kind/scope/source survive an edit
	if got.Kind != "action" || got.Scope != "node:host-1" || got.Source != "agent" {
		t.Fatalf("edit changed system fields: %+v", got)
	}

	// update of a missing id fails
	if err := st.UpdateMemory(ctx, 99999, "x", "y"); err == nil {
		t.Fatal("expected error updating missing memory")
	}

	// delete
	if err := st.DeleteMemory(ctx, node.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err = st.GetMemory(ctx, node.ID)
	if err != nil || got != nil {
		t.Fatalf("deleted memory still readable: %+v err %v", got, err)
	}
}
