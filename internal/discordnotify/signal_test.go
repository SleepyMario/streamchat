package discordnotify

import "testing"

func TestSignalReportsFreshLifecycleState(t *testing.T) {
	var signal Signal
	first := signal.State()
	if first.Online || first.CheckedAt.IsZero() {
		t.Fatalf("initial state=%+v", first)
	}
	signal.Set(true)
	second := signal.State()
	if !second.Online || !second.CheckedAt.After(first.CheckedAt) {
		t.Fatalf("online state=%+v first=%+v", second, first)
	}
	signal.Set(false)
	if state := signal.State(); state.Online {
		t.Fatalf("offline state=%+v", state)
	}
}
