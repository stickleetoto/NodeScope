package agent

import (
	"path/filepath"
	"testing"
)

func TestConfigRoundTripWithServices(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.json")
	want := Config{
		Server: "http://127.0.0.1:7443", NodeID: "id", Secret: "secret", Name: "pi",
		Services: []ServiceSpec{{Name: "bio", Type: "tcp", Target: "127.0.0.1:8787"}},
	}
	if err := SaveConfig(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Name != "bio" {
		t.Fatalf("unexpected config: %+v", got)
	}
}

func TestRejectDuplicateServices(t *testing.T) {
	c := Config{Server: "x", NodeID: "id", Secret: "s", Services: []ServiceSpec{
		{Name: "BIO", Type: "tcp", Target: "a:1"},
		{Name: "bio", Type: "tcp", Target: "a:2"},
	}}
	if err := ValidateConfig(c); err == nil {
		t.Fatal("expected duplicate service error")
	}
}
