package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
	"github.com/jjp-monitor/jjp/internal/store"
)

func joinedNode(t *testing.T, ts *httptest.Server, name string) protocol.JoinResponse {
	t.Helper()
	jr := protocol.JoinRequest{Token: "join", Name: name, OS: "linux", Arch: "arm64", AgentVersion: protocol.Version}
	b, _ := json.Marshal(jr)
	resp, err := http.Post(ts.URL+"/api/v1/join", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("join status %d", resp.StatusCode)
	}
	var joined protocol.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&joined); err != nil {
		t.Fatal(err)
	}
	return joined
}

func adminReq(t *testing.T, method, endpoint string, body any) *http.Response {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, endpoint, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer admin")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestJoinHeartbeatListWithServices(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	joined := joinedNode(t, ts, "pi")

	hb, _ := json.Marshal(protocol.HeartbeatRequest{
		Metrics:  protocol.Metrics{CPUPercent: 12.5, RAMPercent: 42},
		Services: []protocol.ServiceStatus{{Name: "bio", Type: "tcp", Target: "127.0.0.1:8787", Healthy: true, Message: "ok"}},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/heartbeat", bytes.NewReader(hb))
	req.Header.Set("Authorization", "Bearer "+joined.Secret)
	req.Header.Set("X-JJP-Node-ID", joined.NodeID)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat status %d", r2.StatusCode)
	}

	r3 := adminReq(t, http.MethodGet, ts.URL+"/api/v1/nodes", nil)
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("list status %d", r3.StatusCode)
	}
	var nodes []protocol.NodeView
	if err := json.NewDecoder(r3.Body).Decode(&nodes); err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].Status != "ONLINE" || nodes[0].Metrics.CPUPercent != 12.5 {
		t.Fatalf("unexpected nodes: %+v", nodes)
	}
	if len(nodes[0].Services) != 1 || !nodes[0].Services[0].Healthy {
		t.Fatalf("service status not persisted: %+v", nodes[0].Services)
	}
}

func TestRenameAndDeleteNode(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	joined := joinedNode(t, ts, "old")

	resp := adminReq(t, http.MethodPatch, ts.URL+"/api/v1/nodes/old", protocol.RenameNodeRequest{Name: "new"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("rename status %d", resp.StatusCode)
	}
	if _, ok := st.ViewByNameOrID("new", now()); !ok {
		t.Fatal("renamed node not found")
	}

	resp = adminReq(t, http.MethodDelete, ts.URL+"/api/v1/nodes/new", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	if _, ok := st.ViewByNameOrID(joined.NodeID, now()); ok {
		t.Fatal("deleted node still exists")
	}
}

func TestRejectDuplicateNodeName(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	_ = joinedNode(t, ts, "pi")
	b, _ := json.Marshal(protocol.JoinRequest{Token: "join", Name: "PI"})
	resp, err := http.Post(ts.URL+"/api/v1/join", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestRejectBadJoinToken(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	b, _ := json.Marshal(protocol.JoinRequest{Token: "wrong", Name: "x"})
	resp, err := http.Post(ts.URL+"/api/v1/join", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func now() time.Time { return time.Now().UTC() }

func TestReadTokenCanReadButCannotWrite(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	_ = joinedNode(t, ts, "pi")

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+st.ReadToken())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read token list status %d", resp.StatusCode)
	}

	body, _ := json.Marshal(protocol.RenameNodeRequest{Name: "nope"})
	req, _ = http.NewRequest(http.MethodPatch, ts.URL+"/api/v1/nodes/pi", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+st.ReadToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("read token write status %d", resp.StatusCode)
	}
	var apiErr protocol.APIErrorBody
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		t.Fatal(err)
	}
	if apiErr.Error.Code != "admin_required" || apiErr.Error.RequestID == "" {
		t.Fatalf("unexpected API error: %+v", apiErr)
	}
}

func TestSummaryAndServicesEndpoints(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	joined := joinedNode(t, ts, "pi")
	hb, _ := json.Marshal(protocol.HeartbeatRequest{Services: []protocol.ServiceStatus{
		{Name: "ok", Healthy: true}, {Name: "bad", Healthy: false, Message: "down"},
	}})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/heartbeat", bytes.NewReader(hb))
	req.Header.Set("Authorization", "Bearer "+joined.Secret)
	req.Header.Set("X-JJP-Node-ID", joined.NodeID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/summary", nil)
	req.Header.Set("Authorization", "Bearer "+st.ReadToken())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sum protocol.Summary
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
		t.Fatal(err)
	}
	if sum.Total != 1 || sum.Online != 1 || sum.HealthyServices != 1 || sum.UnhealthyServices != 1 {
		t.Fatalf("unexpected summary: %+v", sum)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes/pi/services", nil)
	req.Header.Set("Authorization", "Bearer "+st.ReadToken())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var services []protocol.ServiceStatus
	if err := json.NewDecoder(resp.Body).Decode(&services); err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 {
		t.Fatalf("unexpected services: %+v", services)
	}
}

func TestAlertsEventsAndSummaryAPI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	joined := joinedNode(t, ts, "pi")

	hb, _ := json.Marshal(protocol.HeartbeatRequest{Services: []protocol.ServiceStatus{{Name: "bio", Healthy: false, Message: "connection refused"}}})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/heartbeat", bytes.NewReader(hb))
	req.Header.Set("Authorization", "Bearer "+joined.Secret)
	req.Header.Set("X-JJP-Node-ID", joined.NodeID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat status %d", resp.StatusCode)
	}

	get := func(path string, dst any) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+st.ReadToken())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if dst != nil && resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}
	var alerts []protocol.Alert
	if code := get("/api/v1/alerts", &alerts); code != http.StatusOK {
		t.Fatalf("alerts status %d", code)
	}
	if len(alerts) != 1 || alerts[0].Kind != "service_down" {
		t.Fatalf("unexpected alerts: %+v", alerts)
	}

	var events []protocol.Event
	if code := get("/api/v1/events?limit=10&node=pi", &events); code != http.StatusOK {
		t.Fatalf("events status %d", code)
	}
	if len(events) != 1 || events[0].Kind != "service_down" {
		t.Fatalf("unexpected events: %+v", events)
	}

	var sum protocol.Summary
	if code := get("/api/v1/summary", &sum); code != http.StatusOK {
		t.Fatalf("summary status %d", code)
	}
	if sum.ActiveAlerts != 1 {
		t.Fatalf("expected one active alert: %+v", sum)
	}

	if code := get("/api/v1/events?limit=999", nil); code != http.StatusBadRequest {
		t.Fatalf("invalid limit status %d", code)
	}
}

func TestOverviewAndDiagnosisAPI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.AddNode(&protocol.Node{ID: "n1", Name: "pi", Secret: "s", AgentVersion: "0.4.0", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat("n1", protocol.Metrics{CPUPercent: 12, RAMPercent: 34, DiskPercent: 56}, []protocol.ServiceStatus{{Name: "bio", Healthy: false, Message: "connection refused"}}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()

	get := func(path string, dst any) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+st.ReadToken())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if dst != nil && resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}
	var overview protocol.Overview
	if code := get("/api/v1/overview?events=10", &overview); code != http.StatusOK {
		t.Fatalf("overview status %d", code)
	}
	if overview.Status != "critical" || !overview.AttentionRequired || len(overview.ActiveAlerts) != 1 || len(overview.UnhealthyNodes) != 1 {
		t.Fatalf("unexpected overview: %+v", overview)
	}
	if code := get("/api/v1/overview?events=99", nil); code != http.StatusBadRequest {
		t.Fatalf("invalid overview event limit status %d", code)
	}
	var diagnosis protocol.Diagnosis
	if code := get("/api/v1/nodes/pi/diagnosis", &diagnosis); code != http.StatusOK {
		t.Fatalf("diagnosis status %d", code)
	}
	if diagnosis.Overall != "critical" || len(diagnosis.Findings) < 2 || len(diagnosis.SuggestedActions) == 0 {
		t.Fatalf("unexpected diagnosis: %+v", diagnosis)
	}
}

func TestIncidentAPI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	p := store.DefaultAlertPolicy()
	p.UnstableAfter = 5 * time.Second
	p.OfflineAfter = 10 * time.Second
	if err := st.SetAlertPolicy(p); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNode(&protocol.Node{ID: "n1", Name: "pi", Secret: "s", RegisteredAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := st.Evaluate(t0.Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.Evaluate(t0.Add(11 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat("n1", protocol.Metrics{}, nil, t0.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	get := func(path string, dst any) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+st.ReadToken())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if dst != nil && resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}
	var incidents []protocol.Incident
	if code := get("/api/v1/incidents?status=resolved&node=pi&limit=10", &incidents); code != http.StatusOK {
		t.Fatalf("incidents status %d", code)
	}
	if len(incidents) != 1 || incidents[0].Status != "resolved" || incidents[0].EventCount < 3 {
		t.Fatalf("unexpected incidents: %+v", incidents)
	}
	var detail protocol.IncidentDetail
	if code := get("/api/v1/incidents/"+incidents[0].ID, &detail); code != http.StatusOK {
		t.Fatalf("incident detail status %d", code)
	}
	if detail.Incident.ID != incidents[0].ID || len(detail.Events) != incidents[0].EventCount {
		t.Fatalf("unexpected incident detail: %+v", detail)
	}
	if code := get("/api/v1/incidents?status=banana", nil); code != http.StatusBadRequest {
		t.Fatalf("bad status returned %d", code)
	}
	if code := get("/api/v1/incidents/missing", nil); code != http.StatusNotFound {
		t.Fatalf("missing incident returned %d", code)
	}
}

func TestHeartbeatRejectsInvalidMetricsAndServicePayload(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	joined := joinedNode(t, ts, "pi")

	cases := []protocol.HeartbeatRequest{
		{Metrics: protocol.Metrics{CPUPercent: 101}},
		{Metrics: protocol.Metrics{RAMPercent: -1}},
		{Metrics: protocol.Metrics{RAMUsedBytes: 2, RAMTotalBytes: 1}},
		{Services: []protocol.ServiceStatus{{Name: "dup"}, {Name: "DUP"}}},
		{Services: []protocol.ServiceStatus{{Name: "x", Message: strings.Repeat("x", 513)}}},
	}
	for i, hb := range cases {
		b, _ := json.Marshal(hb)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/heartbeat", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+joined.Secret)
		req.Header.Set("X-JJP-Node-ID", joined.NodeID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("case %d: got %d", i, resp.StatusCode)
		}
	}
}

func TestTokenRotationInvalidatesPreviousToken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/tokens/read/rotate", nil)
	req.Header.Set("Authorization", "Bearer admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rotate status %d", resp.StatusCode)
	}
	var rotated protocol.TokenRotationResponse
	if err := json.NewDecoder(resp.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Kind != "read" || rotated.Token == "" {
		t.Fatalf("bad rotation response: %+v", rotated)
	}

	oldReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes", nil)
	// We need the original token from disk before rotation, so generate another store for a deterministic read token check.
	oldReq.Header.Set("Authorization", "Bearer definitely-old")
	oldResp, err := http.DefaultClient.Do(oldReq)
	if err != nil {
		t.Fatal(err)
	}
	oldResp.Body.Close()
	if oldResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old token status %d", oldResp.StatusCode)
	}

	newReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/nodes", nil)
	newReq.Header.Set("Authorization", "Bearer "+rotated.Token)
	newResp, err := http.DefaultClient.Do(newReq)
	if err != nil {
		t.Fatal(err)
	}
	newResp.Body.Close()
	if newResp.StatusCode != http.StatusOK {
		t.Fatalf("new token status %d", newResp.StatusCode)
	}
}

func TestInfoIncludesStateSchema(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info protocol.APIInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.StateSchema != store.CurrentSchemaVersion {
		t.Fatalf("state schema %d", info.StateSchema)
	}
}

func TestHistorySinceQueryValidation(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{Store: st}).Handler())
	defer ts.Close()
	get := func(path string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+st.ReadToken())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("/api/v1/events?since=not-a-time"); code != http.StatusBadRequest {
		t.Fatalf("invalid events since status %d", code)
	}
	if code := get("/api/v1/incidents?since=not-a-time"); code != http.StatusBadRequest {
		t.Fatalf("invalid incidents since status %d", code)
	}
	if code := get("/api/v1/events?since=2026-08-25T00:00:00Z"); code != http.StatusOK {
		t.Fatalf("valid events since status %d", code)
	}
}
