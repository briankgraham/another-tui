package store

import (
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	t.Setenv("CTABS_HOME", t.TempDir())

	err := Update(func(s *State) error {
		s.Sessions = append(s.Sessions, Session{ID: "a", Name: "alpha", PaneID: "%3"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, s := st.FindByPane("%3"); s == nil || s.Name != "alpha" {
		t.Fatalf("got %+v", st)
	}
}

func TestStatusDefaultsToNew(t *testing.T) {
	t.Setenv("CTABS_HOME", t.TempDir())
	s, err := LoadStatus("missing")
	if err != nil || s.State != StateNew {
		t.Fatalf("got %+v, %v", s, err)
	}
	now := time.Now().Round(0)
	if err := SaveStatus("x", Status{State: StateReady, Updated: now}); err != nil {
		t.Fatal(err)
	}
	s, _ = LoadStatus("x")
	if s.State != StateReady || !s.Updated.Equal(now) {
		t.Fatalf("got %+v", s)
	}
}
