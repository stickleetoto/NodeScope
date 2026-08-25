package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jjp-monitor/jjp/internal/apiclient"
	"github.com/jjp-monitor/jjp/internal/protocol"
	jjpserver "github.com/jjp-monitor/jjp/internal/server"
	"github.com/jjp-monitor/jjp/internal/store"
)

func TestLegacyMCPListAndSummary(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	api, err := apiclient.New(ts.URL, st.ReadToken())
	if err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_summary","arguments":{}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := (&Server{API: api}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	var initResp, listResp, callResp map[string]any
	for _, dst := range []*map[string]any{&initResp, &listResp, &callResp} {
		if err := dec.Decode(dst); err != nil {
			t.Fatal(err)
		}
	}
	if initResp["result"] == nil || listResp["result"] == nil || callResp["result"] == nil {
		t.Fatalf("missing MCP result: %s", out.String())
	}
	result := callResp["result"].(map[string]any)
	if result["isError"] != false {
		t.Fatalf("summary tool failed: %+v", result)
	}
}

func TestModernDiscoverAndReadOnlyTools(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	api, _ := apiclient.New(ts.URL, st.ReadToken())
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	input := `{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{` + meta + `}}` + "\n" +
		`{"jsonrpc":"2.0","id":"l","method":"tools/list","params":{` + meta + `}}`
	var out bytes.Buffer
	if err := (&Server{API: api}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "rename_node") || strings.Contains(out.String(), "remove_node") {
		t.Fatalf("write tools leaked in read-only mode: %s", out.String())
	}
	if !strings.Contains(out.String(), `"resultType":"complete"`) || !strings.Contains(out.String(), `"server/discover"`) && !strings.Contains(out.String(), `"supportedVersions"`) {
		t.Fatalf("modern response missing fields: %s", out.String())
	}
}

func TestWriteToolsRequireAllowWriteAndAdminToken(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	// Seed one node directly; MCP writes always go back through the HTTP API.
	if err := st.AddNode(&protocol.Node{ID: "node-1", Name: "old"}); err != nil {
		t.Fatal(err)
	}
	api, _ := apiclient.New(ts.URL, "admin")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"rename_node","arguments":{"node":"old","new_name":"new"}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := (&Server{API: api, AllowWrite: true}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "rename_node") || !strings.Contains(out.String(), `"renamed":true`) {
		t.Fatalf("write MCP path failed: %s", out.String())
	}
	if _, ok := st.ViewByNameOrID("new", time.Now()); !ok {
		t.Fatal("MCP rename did not change central state")
	}
}

func TestAlertAndEventTools(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	now := time.Now().UTC()
	if err := st.AddNode(&protocol.Node{ID: "n1", Name: "pi", Secret: "s", RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat("n1", protocol.Metrics{}, []protocol.ServiceStatus{{Name: "bio", Healthy: false, Message: "down"}}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	api, _ := apiclient.New(ts.URL, st.ReadToken())
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_active_alerts","arguments":{"node":"pi"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_recent_events","arguments":{"node":"pi","limit":10}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := (&Server{API: api}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "get_active_alerts") || !strings.Contains(text, "get_recent_events") {
		t.Fatalf("new tools missing: %s", text)
	}
	if !strings.Contains(text, `"kind":"service_down"`) {
		t.Fatalf("alert/event tool result missing service event: %s", text)
	}
}

func TestAIOrientedToolsExposeSchemasAnnotationsAndStructuredContent(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	now := time.Now().UTC()
	if err := st.AddNode(&protocol.Node{ID: "n1", Name: "pi", Secret: "s", AgentVersion: protocol.Version, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.Heartbeat("n1", protocol.Metrics{}, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	api, _ := apiclient.New(ts.URL, st.ReadToken())
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
	input := `{"jsonrpc":"2.0","id":"l","method":"tools/list","params":{` + meta + `}}` + "\n" +
		`{"jsonrpc":"2.0","id":"c","method":"tools/call","params":{"name":"get_overview","arguments":{"events":5},` + meta + `}}`
	var out bytes.Buffer
	if err := (&Server{API: api}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{`"name":"get_overview"`, `"name":"diagnose_node"`, `"outputSchema"`, `"readOnlyHint":true`, `"structuredContent"`, `"attention_required"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in MCP output: %s", want, text)
		}
	}
}

func TestRemoveNodeRequiresExplicitConfirm(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	if err := st.AddNode(&protocol.Node{ID: "n1", Name: "pi"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	api, _ := apiclient.New(ts.URL, "admin")
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"remove_node","arguments":{"node":"pi"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remove_node","arguments":{"node":"pi","confirm":true}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := (&Server{API: api, AllowWrite: true}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "confirm=true") || !strings.Contains(text, `"removed":true`) {
		t.Fatalf("expected confirmation gate and successful confirmed removal: %s", text)
	}
	if _, ok := st.ViewByNameOrID("pi", time.Now()); ok {
		t.Fatal("node should be removed only after confirmed call")
	}
}

func TestIncidentToolsPreferCorrelatedHistory(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
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
	incs := st.Incidents(10, "pi", "resolved")
	if len(incs) != 1 {
		t.Fatalf("expected one incident: %+v", incs)
	}

	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	api, _ := apiclient.New(ts.URL, st.ReadToken())
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_incidents","arguments":{"node":"pi","status":"resolved","limit":10}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_incident","arguments":{"incident_id":"` + incs[0].ID + `"}}}`,
	}, "\n")
	var out bytes.Buffer
	if err := (&Server{API: api}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{`"name":"get_incidents"`, `"name":"get_incident"`, `"title":"Node outage"`, `"status":"resolved"`, `"kind":"node_offline"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in MCP incident output: %s", want, text)
		}
	}
}

func TestHistoryToolsExposeLookbackWindow(t *testing.T) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"), "admin", "join")
	ts := httptest.NewServer((&jjpserver.Server{Store: st}).Handler())
	defer ts.Close()
	api, _ := apiclient.New(ts.URL, st.ReadToken())
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	var out bytes.Buffer
	if err := (&Server{API: api}).Run(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, `"since_minutes"`) {
		t.Fatalf("history tools missing since_minutes: %s", text)
	}
}
