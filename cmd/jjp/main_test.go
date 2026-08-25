package main

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
	jjpserver "github.com/jjp-monitor/jjp/internal/server"
	"github.com/jjp-monitor/jjp/internal/store"
)

func TestHealthExitCodes(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()

	if err := cmdHealth([]string{"--server", ts.URL, "--token", st.ReadToken()}); err != nil {
		t.Fatalf("healthy fleet should return nil, got %v", err)
	}

	now := time.Now().UTC()
	if err := st.AddNode(&protocol.Node{ID: "n1", Name: "pi", Secret: "s", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat("n1", protocol.Metrics{}, []protocol.ServiceStatus{{Name: "bio", Healthy: false, Message: "down"}}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	err = cmdHealth([]string{"--server", ts.URL, "--token", st.ReadToken(), "--json"})
	ee, ok := err.(exitCodeError)
	if !ok || ee.ExitCode() != 2 {
		t.Fatalf("degraded fleet should return exit code 2, got %#v", err)
	}
}

func TestParseSinceFlag(t *testing.T) {
	before := time.Now().UTC().Add(-2 * time.Hour)
	got, err := parseSinceFlag("2h")
	if err != nil {
		t.Fatal(err)
	}
	if got.Before(before.Add(-2*time.Second)) || got.After(before.Add(2*time.Second)) {
		t.Fatalf("unexpected duration cutoff: %v", got)
	}
	stamp := "2026-08-25T00:00:00Z"
	got, err = parseSinceFlag(stamp)
	if err != nil || got.Format(time.RFC3339) != stamp {
		t.Fatalf("unexpected RFC3339 cutoff: %v %v", got, err)
	}
	if _, err := parseSinceFlag("0s"); err == nil {
		t.Fatal("expected zero duration rejection")
	}
}
