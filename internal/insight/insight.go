package insight

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stickleetoto/NodeScope/internal/protocol"
)

func Summary(nodes []protocol.NodeView, alerts []protocol.Alert) protocol.Summary {
	out := protocol.Summary{Total: len(nodes), ActiveAlerts: len(alerts)}
	for _, n := range nodes {
		switch n.Status {
		case "ONLINE":
			out.Online++
		case "UNSTABLE":
			out.Unstable++
		default:
			out.Offline++
		}
		for _, svc := range n.Services {
			if svc.Healthy {
				out.HealthyServices++
			} else {
				out.UnhealthyServices++
			}
		}
	}
	return out
}

func Overview(nodes []protocol.NodeView, alerts []protocol.Alert, events []protocol.Event, incidents []protocol.Incident, now time.Time) protocol.Overview {
	now = now.UTC()
	sum := Summary(nodes, alerts)
	unhealthy := make([]protocol.NodeDigest, 0)
	alerted := map[string]bool{}
	critical := false
	for _, a := range alerts {
		alerted[a.NodeID] = true
		if strings.EqualFold(a.Severity, "critical") {
			critical = true
		}
	}
	for _, n := range nodes {
		bad := n.Status != "ONLINE" || alerted[n.ID]
		if !bad {
			for _, svc := range n.Services {
				if !svc.Healthy {
					bad = true
					break
				}
			}
		}
		if !bad {
			continue
		}
		h, total := serviceCounts(n.Services)
		unhealthy = append(unhealthy, protocol.NodeDigest{
			ID: n.ID, Name: n.Name, Status: n.Status,
			CPUPercent: n.Metrics.CPUPercent, RAMPercent: n.Metrics.RAMPercent, DiskPercent: n.Metrics.DiskPercent,
			HealthyServices: h, TotalServices: total, LastHeartbeat: n.LastHeartbeat, AgentVersion: n.AgentVersion,
		})
	}
	status := "healthy"
	if sum.Offline > 0 || critical {
		status = "critical"
	} else if sum.Unstable > 0 || sum.ActiveAlerts > 0 || sum.UnhealthyServices > 0 {
		status = "degraded"
	}
	headline := fmt.Sprintf("%d/%d nodes online; %d active alerts; %d unhealthy services", sum.Online, sum.Total, sum.ActiveAlerts, sum.UnhealthyServices)
	if sum.Total == 0 {
		headline = "no nodes registered"
	}
	return protocol.Overview{
		GeneratedAt: now, Status: status, AttentionRequired: status != "healthy", Headline: headline,
		Summary: sum, UnhealthyNodes: unhealthy, ActiveAlerts: append([]protocol.Alert(nil), alerts...), ActiveIncidents: append([]protocol.Incident(nil), incidents...), RecentEvents: append([]protocol.Event(nil), events...),
	}
}

func Diagnose(node protocol.NodeView, alerts []protocol.Alert, now time.Time) protocol.Diagnosis {
	now = now.UTC()
	findings := make([]protocol.Finding, 0)
	actions := make([]string, 0)
	addAction := func(v string) {
		for _, x := range actions {
			if x == v {
				return
			}
		}
		actions = append(actions, v)
	}
	if node.Status == "OFFLINE" {
		findings = append(findings, protocol.Finding{Code: "node_offline", Severity: "critical", Summary: "Node is offline", Evidence: heartbeatEvidence(node, now)})
		addAction("Check network reachability and verify that `jjp agent` is running on the node.")
	} else if node.Status == "UNSTABLE" {
		findings = append(findings, protocol.Finding{Code: "node_unstable", Severity: "warning", Summary: "Node heartbeat is delayed", Evidence: heartbeatEvidence(node, now)})
		addAction("Check agent connectivity, packet loss, and central-server reachability.")
	}

	for _, a := range alerts {
		f := protocol.Finding{Code: a.Kind, Severity: a.Severity, Summary: a.Message}
		if a.Subject != "" {
			f.Evidence = a.Subject
		}
		findings = append(findings, f)
		switch a.Kind {
		case "service_down":
			addAction(fmt.Sprintf("Inspect service %q on %s and verify its configured health-check target.", a.Subject, node.Name))
		case "metric_high":
			switch strings.ToUpper(a.Subject) {
			case "CPU":
				addAction("Inspect the highest-CPU processes and recent workload changes.")
			case "RAM":
				addAction("Inspect memory-heavy processes and check for sustained memory growth.")
			case "DISK":
				addAction("Inspect disk usage by directory and remove or rotate unnecessary data.")
			}
		}
	}

	for _, svc := range node.Services {
		if !svc.Healthy && !hasFinding(findings, "service_down", svc.Name) {
			findings = append(findings, protocol.Finding{Code: "service_unhealthy", Severity: "critical", Summary: fmt.Sprintf("Service %s is unhealthy", svc.Name), Evidence: svc.Message})
			addAction(fmt.Sprintf("Inspect service %q on %s and verify its configured health-check target.", svc.Name, node.Name))
		}
	}

	if node.AgentVersion != "" && node.AgentVersion != protocol.Version {
		findings = append(findings, protocol.Finding{Code: "agent_version_mismatch", Severity: "warning", Summary: "Agent version differs from central server", Evidence: fmt.Sprintf("agent=%s server=%s", node.AgentVersion, protocol.Version)})
		addAction(fmt.Sprintf("Update the node agent to jjp %s when convenient.", protocol.Version))
	}

	if len(findings) == 0 {
		findings = append(findings, protocol.Finding{Code: "healthy", Severity: "info", Summary: "No current health problems detected", Evidence: heartbeatEvidence(node, now)})
	}
	severityRank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(findings, func(i, j int) bool { return severityRank[findings[i].Severity] < severityRank[findings[j].Severity] })
	overall := "healthy"
	for _, f := range findings {
		if f.Severity == "critical" {
			overall = "critical"
			break
		}
		if f.Severity == "warning" {
			overall = "warning"
		}
	}
	return protocol.Diagnosis{GeneratedAt: now, Overall: overall, Node: node, Findings: findings, SuggestedActions: actions}
}

func heartbeatEvidence(node protocol.NodeView, now time.Time) string {
	if node.LastHeartbeat.IsZero() {
		return "no heartbeat received yet"
	}
	age := now.Sub(node.LastHeartbeat).Round(time.Second)
	if age < 0 {
		age = 0
	}
	return fmt.Sprintf("last heartbeat %s ago", age)
}

func hasFinding(v []protocol.Finding, code, subject string) bool {
	for _, f := range v {
		if f.Code == code && (subject == "" || strings.Contains(strings.ToLower(f.Evidence), strings.ToLower(subject)) || strings.Contains(strings.ToLower(f.Summary), strings.ToLower(subject))) {
			return true
		}
	}
	return false
}

func serviceCounts(v []protocol.ServiceStatus) (healthy, total int) {
	for _, s := range v {
		total++
		if s.Healthy {
			healthy++
		}
	}
	return
}
