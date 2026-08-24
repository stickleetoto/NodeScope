package insight

import (
	"testing"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
)

func TestOverviewAndDiagnosis(t *testing.T) {
	now := time.Now().UTC()
	nodes := []protocol.NodeView{{ID: "n1", Name: "pi", Status: "ONLINE", AgentVersion: protocol.Version, LastHeartbeat: now, Services: []protocol.ServiceStatus{{Name: "bio", Healthy: false, Message: "connection refused"}}}}
	alerts := []protocol.Alert{{NodeID: "n1", NodeName: "pi", Kind: "service_down", Severity: "critical", Subject: "bio", Message: "connection refused", Since: now}}
	o := Overview(nodes, alerts, nil, nil, now)
	if o.Status != "critical" || !o.AttentionRequired || len(o.UnhealthyNodes) != 1 {
		t.Fatalf("unexpected overview: %+v", o)
	}
	d := Diagnose(nodes[0], alerts, now)
	if d.Overall != "critical" || len(d.SuggestedActions) == 0 {
		t.Fatalf("unexpected diagnosis: %+v", d)
	}
}
