package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
)

func TestNodeSecretSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	n := &protocol.Node{ID: "id", Name: "pi", Secret: "node-secret", RegisteredAt: time.Now()}
	if err := s.AddNode(n); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path, "ignored-admin", "new-join")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.AuthenticateNode("id", "node-secret"); !ok {
		t.Fatal("node secret did not survive server restart")
	}
}

func TestReadTokenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s1, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	first := s1.ReadToken()
	if first == "" {
		t.Fatal("read token was not generated")
	}
	s2, err := Open(path, "ignored", "join2")
	if err != nil {
		t.Fatal(err)
	}
	if s2.ReadToken() != first {
		t.Fatalf("read token changed across reopen")
	}
}

func TestAlertLifecycleNodeServiceAndMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultAlertPolicy()
	p.MetricFor = 10 * time.Second
	p.UnstableAfter = 5 * time.Second
	p.OfflineAfter = 10 * time.Second
	if err := s.SetAlertPolicy(p); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	n := &protocol.Node{ID: "n1", Name: "pi", Secret: "s", RegisteredAt: t0}
	if err := s.AddNode(n); err != nil {
		t.Fatal(err)
	}

	if err := s.Evaluate(t0.Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	alerts := s.Alerts("")
	if len(alerts) != 1 || alerts[0].Kind != "node_unstable" {
		t.Fatalf("unexpected unstable alerts: %+v", alerts)
	}
	if err := s.Evaluate(t0.Add(11 * time.Second)); err != nil {
		t.Fatal(err)
	}
	alerts = s.Alerts("")
	if len(alerts) != 1 || alerts[0].Kind != "node_offline" {
		t.Fatalf("unexpected offline alerts: %+v", alerts)
	}

	badSvc := []protocol.ServiceStatus{{Name: "bio", Type: "tcp", Target: "127.0.0.1:8787", Healthy: false, Message: "connection refused"}}
	m := protocol.Metrics{CPUPercent: 95, RAMPercent: 20, DiskPercent: 30}
	if err := s.Heartbeat("n1", m, badSvc, t0.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	alerts = s.Alerts("")
	if len(alerts) != 1 {
		t.Fatalf("expected only service alert while CPU duration is pending, got %+v", alerts)
	}
	kinds := map[string]bool{}
	for _, a := range alerts {
		kinds[a.Kind] = true
	}
	if !kinds["service_down"] || kinds["node_offline"] {
		t.Fatalf("recovery did not clear availability alert: %+v", alerts)
	}

	// The CPU must remain high for the configured duration before opening.
	if err := s.Heartbeat("n1", m, badSvc, t0.Add(23*time.Second)); err != nil {
		t.Fatal(err)
	}
	alerts = s.Alerts("")
	kinds = map[string]bool{}
	for _, a := range alerts {
		kinds[a.Kind] = true
	}
	if !kinds["metric_high"] || !kinds["service_down"] {
		t.Fatalf("expected metric and service alerts: %+v", alerts)
	}

	good := []protocol.ServiceStatus{{Name: "bio", Type: "tcp", Target: "127.0.0.1:8787", Healthy: true}}
	m.CPUPercent = 10
	if err := s.Heartbeat("n1", m, good, t0.Add(24*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := s.Alerts(""); len(got) != 0 {
		t.Fatalf("alerts did not resolve: %+v", got)
	}

	events := s.Events(20, "pi")
	wanted := map[string]bool{"node_unstable": false, "node_offline": false, "node_recovered": false, "service_down": false, "service_recovered": false, "metric_high": false, "metric_recovered": false}
	for _, e := range events {
		if _, ok := wanted[e.Kind]; ok {
			wanted[e.Kind] = true
		}
	}
	for kind, seen := range wanted {
		if !seen {
			t.Fatalf("missing %s event in %+v", kind, events)
		}
	}
}

func TestAlertsAndEventsPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path, "admin", "join")
	t0 := time.Now().UTC()
	if err := s.AddNode(&protocol.Node{ID: "n", Name: "pi", Secret: "s", RegisteredAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat("n", protocol.Metrics{}, []protocol.ServiceStatus{{Name: "x", Healthy: false, Message: "down"}}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(s.Alerts("")) != 1 {
		t.Fatal("expected active alert before restart")
	}

	s2, err := Open(path, "ignored", "new-join")
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Alerts(""); len(got) != 1 || got[0].Kind != "service_down" {
		t.Fatalf("alert did not persist: %+v", got)
	}
	if got := s2.Events(10, "pi"); len(got) == 0 || got[0].Kind != "service_down" {
		t.Fatalf("event did not persist: %+v", got)
	}
}

func TestPendingMetricDurationSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	t0 := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	p := DefaultAlertPolicy()
	p.MetricFor = 10 * time.Second

	s1, _ := Open(path, "admin", "join")
	if err := s1.SetAlertPolicy(p); err != nil {
		t.Fatal(err)
	}
	if err := s1.AddNode(&protocol.Node{ID: "n", Name: "pi", Secret: "s", RegisteredAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Heartbeat("n", protocol.Metrics{CPUPercent: 95}, nil, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := s1.Alerts(""); len(got) != 0 {
		t.Fatalf("alert opened too early: %+v", got)
	}

	s2, err := Open(path, "ignored", "join2")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.SetAlertPolicy(p); err != nil {
		t.Fatal(err)
	}
	if err := s2.Heartbeat("n", protocol.Metrics{CPUPercent: 96}, nil, t0.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	got := s2.Alerts("")
	if len(got) != 1 || got[0].Kind != "metric_high" || got[0].Subject != "CPU" {
		t.Fatalf("pending duration was lost across restart: %+v", got)
	}
}

func TestIncidentCorrelatesOutageAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultAlertPolicy()
	p.UnstableAfter = 5 * time.Second
	p.OfflineAfter = 10 * time.Second
	if err := s.SetAlertPolicy(p); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	if err := s.AddNode(&protocol.Node{ID: "n1", Name: "pi-main", Secret: "s", RegisteredAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(t0.Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.Evaluate(t0.Add(11 * time.Second)); err != nil {
		t.Fatal(err)
	}

	bad := []protocol.ServiceStatus{{Name: "bio", Type: "tcp", Target: "127.0.0.1:8787", Healthy: false, Message: "connection refused"}}
	if err := s.Heartbeat("n1", protocol.Metrics{}, bad, t0.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	incs := s.Incidents(10, "pi-main", "open")
	if len(incs) != 1 {
		t.Fatalf("expected one open correlated incident, got %+v", incs)
	}
	if incs[0].Title != "Node outage" || incs[0].EventCount < 4 {
		t.Fatalf("unexpected incident after outage: %+v", incs[0])
	}

	good := []protocol.ServiceStatus{{Name: "bio", Type: "tcp", Target: "127.0.0.1:8787", Healthy: true}}
	if err := s.Heartbeat("n1", protocol.Metrics{}, good, t0.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := s.Incidents(10, "pi-main", "open"); len(got) != 0 {
		t.Fatalf("incident remained open after all alerts resolved: %+v", got)
	}
	resolved := s.Incidents(10, "pi-main", "resolved")
	if len(resolved) != 1 || resolved[0].ResolvedAt == nil {
		t.Fatalf("expected one resolved incident, got %+v", resolved)
	}
	detail, ok := s.Incident(resolved[0].ID)
	if !ok || len(detail.Events) != resolved[0].EventCount {
		t.Fatalf("incident detail lost timeline: %+v ok=%v", detail, ok)
	}
	kinds := map[string]bool{}
	for _, e := range detail.Events {
		kinds[e.Kind] = true
	}
	for _, want := range []string{"node_unstable", "node_offline", "node_recovered", "service_down", "service_recovered"} {
		if !kinds[want] {
			t.Fatalf("incident missing %s: %+v", want, detail.Events)
		}
	}

	s2, err := Open(path, "ignored", "join2")
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := s2.Incident(resolved[0].ID)
	if !ok || persisted.Incident.Status != "resolved" || len(persisted.Events) != len(detail.Events) {
		t.Fatalf("incident did not survive restart: %+v ok=%v", persisted, ok)
	}

	// Incident timelines are stored with the incident, so rolling raw-event retention
	// cannot erase the evidence for an incident that is still retained.
	s2.mu.Lock()
	s2.data.Events = nil
	if err := s2.saveLocked(); err != nil {
		s2.mu.Unlock()
		t.Fatal(err)
	}
	s2.mu.Unlock()
	archived, ok := s2.Incident(resolved[0].ID)
	if !ok || len(archived.Events) != len(detail.Events) {
		t.Fatalf("incident timeline depended on raw event retention: %+v ok=%v", archived, ok)
	}
}

func TestJoinTokenPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s1, err := Open(path, "admin", "join-one")
	if err != nil {
		t.Fatal(err)
	}
	if got := s1.BootstrapToken(); got != "join-one" {
		t.Fatalf("initial join token = %q", got)
	}
	s2, err := Open(path, "different-admin", "join-two")
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.BootstrapToken(); got != "join-one" {
		t.Fatalf("join token changed across restart: %q", got)
	}
}

func TestAlertPolicyPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s1, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultAlertPolicy()
	p.CPUThreshold = 77
	p.RAMThreshold = 81
	p.MetricFor = 42 * time.Second
	p.UnstableAfter = 9 * time.Second
	p.OfflineAfter = 27 * time.Second
	if err := s1.SetAlertPolicy(p); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path, "ignored", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	got := s2.AlertPolicy()
	if got != p {
		t.Fatalf("policy did not persist: got %+v want %+v", got, p)
	}
}

func TestLegacyStateMigratesWithBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"admin_token":"admin","read_token":"read","bootstrap_token":"join","nodes":{}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, "new-admin", "new-join")
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion() != CurrentSchemaVersion {
		t.Fatalf("schema = %d", s.SchemaVersion())
	}
	if s.BootstrapToken() != "join" || s.ReadToken() != "read" || s.AdminToken() != "admin" {
		t.Fatal("migration changed credentials")
	}
	backup, err := os.ReadFile(path + ".schema0.bak")
	if err != nil {
		t.Fatalf("missing migration backup: %v", err)
	}
	if string(backup) != legacy {
		t.Fatalf("backup differs from legacy input: %s", backup)
	}
}

func TestRejectFutureStateSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	future := `{"schema_version":999,"policy":{"cpu_threshold":90,"ram_threshold":90,"disk_threshold":90,"metric_for":300000000000,"unstable_after":15000000000,"offline_after":60000000000},"nodes":{}}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "admin", "join"); err == nil {
		t.Fatal("expected future schema to be rejected")
	}
}

func TestRotateTokensPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	oldRead := s.ReadToken()
	newRead, err := s.RotateToken("read")
	if err != nil {
		t.Fatal(err)
	}
	if newRead == "" || newRead == oldRead || s.ReadToken() != newRead {
		t.Fatalf("read token was not rotated")
	}
	s2, err := Open(path, "ignored", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if s2.ReadToken() != newRead {
		t.Fatal("rotated token did not persist")
	}
}

func TestBackupAndRestoreState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddNode(&protocol.Node{ID: "n1", Name: "pi", Secret: "secret", RegisteredAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.json")
	info, err := BackupState(path, backup)
	if err != nil {
		t.Fatal(err)
	}
	if info.Nodes != 1 {
		t.Fatalf("backup nodes = %d", info.Nodes)
	}

	s2, err := Open(path, "ignored", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.DeleteNode("pi"); err != nil {
		t.Fatal(err)
	}
	if got := s2.Views(time.Now().UTC()); len(got) != 0 {
		t.Fatalf("node deletion did not persist: %+v", got)
	}

	restored, pre, err := RestoreState(path, backup)
	if err != nil {
		t.Fatal(err)
	}
	if pre == "" || restored.Nodes != 1 {
		t.Fatalf("restore result: %+v pre=%q", restored, pre)
	}
	s3, err := Open(path, "ignored", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s3.ViewByNameOrID("pi", time.Now().UTC()); !ok {
		t.Fatal("restored node missing")
	}
}

func TestInspectStateRejectsMissingCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	bad := fmt.Sprintf(`{"schema_version":%d,"policy":{"cpu_threshold":90,"ram_threshold":90,"disk_threshold":90,"metric_for":300000000000,"unstable_after":15000000000,"offline_after":60000000000},"nodes":{}}`, CurrentSchemaVersion)
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectState(path); err == nil {
		t.Fatal("expected missing credentials to fail validation")
	}
}

func TestDeleteNodeClosesOpenIncident(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	if err := s.AddNode(&protocol.Node{ID: "n1", Name: "pi", Secret: "s", RegisteredAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Heartbeat("n1", protocol.Metrics{}, []protocol.ServiceStatus{{Name: "bio", Healthy: false, Message: "down"}}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	open := s.Incidents(10, "pi", "open")
	if len(open) != 1 {
		t.Fatalf("expected open incident: %+v", open)
	}
	id := open[0].ID
	if err := s.DeleteNode("pi"); err != nil {
		t.Fatal(err)
	}
	if got := s.Incidents(10, "pi", "open"); len(got) != 0 {
		t.Fatalf("ghost incident remains open: %+v", got)
	}
	detail, ok := s.Incident(id)
	if !ok || detail.Incident.Status != "resolved" {
		t.Fatalf("incident was not resolved: %+v ok=%v", detail, ok)
	}
	if len(detail.Events) == 0 || detail.Events[len(detail.Events)-1].Kind != "node_removed" {
		t.Fatalf("node removal was not preserved in timeline: %+v", detail.Events)
	}
}

func TestFleetAcceleratedSoakSurvivesRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path, "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	p := DefaultAlertPolicy()
	p.MetricFor = 6 * time.Second
	p.UnstableAfter = 4 * time.Second
	p.OfflineAfter = 9 * time.Second
	if err := s.SetAlertPolicy(p); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	const nodes = 24
	for i := 0; i < nodes; i++ {
		id := fmt.Sprintf("n%02d", i)
		name := fmt.Sprintf("node-%02d", i)
		if err := s.AddNode(&protocol.Node{ID: id, Name: name, Secret: "secret-" + id, RegisteredAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	for sec := 1; sec <= 120; sec++ {
		now := t0.Add(time.Duration(sec) * time.Second)
		for i := 0; i < nodes; i++ {
			// node-03 has a deliberate outage from t=30..43; node-07 has a
			// service failure from t=60..72; node-11 has sustained CPU pressure.
			if i == 3 && sec >= 30 && sec <= 43 {
				continue
			}
			m := protocol.Metrics{CPUPercent: 20, RAMPercent: 30, DiskPercent: 40}
			if i == 11 && sec >= 80 && sec <= 92 {
				m.CPUPercent = 95
			}
			svcs := []protocol.ServiceStatus{{Name: "app", Healthy: true, Message: "ok"}}
			if i == 7 && sec >= 60 && sec <= 72 {
				svcs[0].Healthy = false
				svcs[0].Message = "synthetic failure"
			}
			if err := s.Heartbeat(fmt.Sprintf("n%02d", i), m, svcs, now); err != nil {
				t.Fatalf("t=%d node=%d heartbeat: %v", sec, i, err)
			}
		}
		if err := s.Evaluate(now); err != nil {
			t.Fatalf("t=%d evaluate: %v", sec, err)
		}
		if sec == 50 || sec == 100 {
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(path, "ignored", "ignored")
			if err != nil {
				t.Fatalf("reopen at t=%d: %v", sec, err)
			}
			if got := s.AlertPolicy(); got != p {
				t.Fatalf("policy changed after restart at t=%d: %+v", sec, got)
			}
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	info, err := InspectState(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Nodes != nodes || info.Incidents < 3 {
		t.Fatalf("unexpected final state: %+v", info)
	}
	if got := s.Alerts(""); len(got) != 0 {
		t.Fatalf("expected all synthetic incidents to recover: %+v", got)
	}
	for _, name := range []string{"node-03", "node-07", "node-11"} {
		incs := s.Incidents(20, name, "resolved")
		if len(incs) == 0 {
			t.Fatalf("expected resolved incident for %s", name)
		}
	}
}

func TestCorruptStateIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := []byte(`{"broken":`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, "admin", "join"); err == nil {
		t.Fatal("expected corrupt state to fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("corrupt input was modified: %q", after)
	}
}

func TestOpenRepairsGhostIncidentFromLegacyDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	t0 := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	legacy := fmt.Sprintf(`{
  "admin_token":"admin",
  "read_token":"read",
  "bootstrap_token":"join",
  "nodes":{},
  "alerts":{"service:gone:x":{"key":"service:gone:x","node_id":"gone","node_name":"old","kind":"service_down","severity":"critical","subject":"x","message":"down","since":%q,"updated_at":%q}},
  "metric_high_since":{"metric:gone:cpu":%q},
  "incidents":[{"id":"inc1","node_id":"gone","node_name":"old","status":"open","severity":"critical","title":"Service disruption","summary":"ongoing","started_at":%q,"last_event_at":%q,"duration_seconds":0,"event_count":1}]
}`, t0.Format(time.RFC3339), t0.Format(time.RFC3339), t0.Format(time.RFC3339), t0.Format(time.RFC3339), t0.Add(time.Minute).Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, "ignored", "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Alerts("")) != 0 {
		t.Fatalf("orphan alerts were not removed: %+v", s.Alerts(""))
	}
	d, ok := s.Incident("inc1")
	if !ok || d.Incident.Status != "resolved" || d.Incident.ResolvedAt == nil {
		t.Fatalf("ghost incident not repaired: %+v ok=%v", d, ok)
	}
}
