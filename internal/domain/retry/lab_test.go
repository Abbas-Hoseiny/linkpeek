package retry

import (
	"testing"
	"time"
)

func TestNewLab(t *testing.T) {
	lab := NewLab(nil)
	if lab == nil {
		t.Fatal("NewLab returned nil")
	}

	scenarios := lab.ListScenarios()
	if len(scenarios) != 3 {
		t.Errorf("expected 3 scenarios, got %d", len(scenarios))
	}

	// Check that stats are initialized
	stats := lab.SnapshotStats()
	if len(stats) != 3 {
		t.Errorf("expected 3 stat entries, got %d", len(stats))
	}

	for _, stat := range stats {
		if stat.TotalHits != 0 {
			t.Errorf("expected TotalHits to be 0 for new lab, got %d", stat.TotalHits)
		}
		if stat.UniqueIPs != 0 {
			t.Errorf("expected UniqueIPs to be 0 for new lab, got %d", stat.UniqueIPs)
		}
	}
}

func TestRecordHit(t *testing.T) {
	publishCalled := false
	publisher := func() {
		publishCalled = true
	}

	lab := NewLab(publisher)

	// Record a hit
	lab.RecordHit("retry-hint", "192.168.1.1")

	stats := lab.SnapshotStats()
	found := false
	for _, stat := range stats {
		if stat.ID == "retry-hint" {
			found = true
			if stat.TotalHits != 1 {
				t.Errorf("expected TotalHits to be 1, got %d", stat.TotalHits)
			}
			if stat.UniqueIPs != 1 {
				t.Errorf("expected UniqueIPs to be 1, got %d", stat.UniqueIPs)
			}
			if stat.LastSeen == nil {
				t.Error("expected LastSeen to be set")
			}
		}
	}

	if !found {
		t.Error("retry-hint scenario not found in stats")
	}

	// Give publisher goroutine time to run
	time.Sleep(10 * time.Millisecond)
	if !publishCalled {
		t.Error("expected publisher to be called")
	}
}

func TestRecordMultipleHits(t *testing.T) {
	lab := NewLab(nil)

	// Record multiple hits from different IPs
	lab.RecordHit("drop-after-n", "192.168.1.1")
	lab.RecordHit("drop-after-n", "192.168.1.2")
	lab.RecordHit("drop-after-n", "192.168.1.1") // Duplicate IP

	stats := lab.SnapshotStats()
	for _, stat := range stats {
		if stat.ID == "drop-after-n" {
			if stat.TotalHits != 3 {
				t.Errorf("expected TotalHits to be 3, got %d", stat.TotalHits)
			}
			if stat.UniqueIPs != 2 {
				t.Errorf("expected UniqueIPs to be 2, got %d", stat.UniqueIPs)
			}
			return
		}
	}

	t.Error("drop-after-n scenario not found in stats")
}

func TestListScenarios(t *testing.T) {
	lab := NewLab(nil)
	scenarios := lab.ListScenarios()

	expectedIDs := map[string]bool{
		"retry-hint":   false,
		"drop-after-n": false,
		"wrong-length": false,
	}

	for _, sc := range scenarios {
		if _, ok := expectedIDs[sc.ID]; ok {
			expectedIDs[sc.ID] = true
			if sc.Title == "" {
				t.Errorf("scenario %s has empty title", sc.ID)
			}
			if sc.Description == "" {
				t.Errorf("scenario %s has empty description", sc.ID)
			}
			if sc.Path == "" {
				t.Errorf("scenario %s has empty path", sc.ID)
			}
		}
	}

	for id, found := range expectedIDs {
		if !found {
			t.Errorf("expected scenario %s not found", id)
		}
	}
}

func TestRecordHitWithEmptyIP(t *testing.T) {
	lab := NewLab(nil)

	// Record hits without IP
	lab.RecordHit("wrong-length", "")
	lab.RecordHit("wrong-length", "")

	stats := lab.SnapshotStats()
	for _, stat := range stats {
		if stat.ID == "wrong-length" {
			if stat.TotalHits != 2 {
				t.Errorf("expected TotalHits to be 2, got %d", stat.TotalHits)
			}
			if stat.UniqueIPs != 0 {
				t.Errorf("expected UniqueIPs to be 0 when no IPs recorded, got %d", stat.UniqueIPs)
			}
			return
		}
	}

	t.Error("wrong-length scenario not found in stats")
}

func TestSetPublisher(t *testing.T) {
	lab := NewLab(nil)
	called := make(chan struct{}, 1)
	lab.SetPublisher(func() {
		called <- struct{}{}
	})

	lab.RecordHit("retry-hint", "")

	select {
	case <-called:
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("expected publisher to trigger after RecordHit")
	}
}

func TestResetAll(t *testing.T) {
	lab := NewLab(nil)
	lab.RecordHit("retry-hint", "1.1.1.1")
	lab.RecordHit("drop-after-n", "2.2.2.2")

	lab.Reset()

	stats := lab.SnapshotStats()
	for _, stat := range stats {
		if stat.TotalHits != 0 {
			t.Errorf("expected TotalHits to reset to 0 for %s, got %d", stat.ID, stat.TotalHits)
		}
		if stat.UniqueIPs != 0 {
			t.Errorf("expected UniqueIPs to reset to 0 for %s, got %d", stat.ID, stat.UniqueIPs)
		}
		if stat.LastSeen != nil {
			t.Errorf("expected LastSeen to reset for %s", stat.ID)
		}
	}
}

func TestResetSelectedScenarios(t *testing.T) {
	triggered := make(chan struct{}, 1)
	lab := NewLab(func() {
		triggered <- struct{}{}
	})

	lab.RecordHit("retry-hint", "1.1.1.1")
	lab.RecordHit("drop-after-n", "2.2.2.2")

	lab.Reset("retry-hint")

	select {
	case <-triggered:
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("expected publisher to trigger after reset")
	}

	stats := lab.SnapshotStats()
	var hint, drop *StatDTO
	for i := range stats {
		switch stats[i].ID {
		case "retry-hint":
			hint = &stats[i]
		case "drop-after-n":
			drop = &stats[i]
		}
	}
	if hint == nil || drop == nil {
		t.Fatalf("expected stats for retry-hint and drop-after-n")
	}
	if hint.TotalHits != 0 || hint.UniqueIPs != 0 || hint.LastSeen != nil {
		t.Errorf("expected retry-hint stats to be cleared, got %+v", hint)
	}
	if drop.TotalHits == 0 || drop.UniqueIPs == 0 || drop.LastSeen == nil {
		t.Errorf("expected drop-after-n stats to remain populated, got %+v", drop)
	}
}
