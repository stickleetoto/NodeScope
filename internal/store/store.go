package store

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stickleetoto/NodeScope/internal/atomicfile"
	"github.com/stickleetoto/NodeScope/internal/protocol"
)

var (
	ErrNameExists = errors.New("node name already exists")
	ErrNodeLimit  = errors.New("node limit reached")
)

const (
	maxEvents            = 5000
	maxIncidents         = 2000
	maxNodes             = 4096
	CurrentSchemaVersion = 1
)

type AlertPolicy struct {
	CPUThreshold  float64       `json:"cpu_threshold"`
	RAMThreshold  float64       `json:"ram_threshold"`
	DiskThreshold float64       `json:"disk_threshold"`
	MetricFor     time.Duration `json:"metric_for"`
	UnstableAfter time.Duration `json:"unstable_after"`
	OfflineAfter  time.Duration `json:"offline_after"`
}

func DefaultAlertPolicy() AlertPolicy {
	return AlertPolicy{
		CPUThreshold: 90, RAMThreshold: 90, DiskThreshold: 90,
		MetricFor: 5 * time.Minute, UnstableAfter: 15 * time.Second, OfflineAfter: 60 * time.Second,
	}
}

type diskState struct {
	SchemaVersion   int                       `json:"schema_version"`
	Policy          AlertPolicy               `json:"policy"`
	AdminToken      string                    `json:"admin_token"`
	ReadToken       string                    `json:"read_token"`
	BootstrapToken  string                    `json:"bootstrap_token"`
	Nodes           map[string]*protocol.Node `json:"nodes"`
	Alerts          map[string]protocol.Alert `json:"alerts,omitempty"`
	Events          []protocol.Event          `json:"events,omitempty"`
	Incidents       []protocol.Incident       `json:"incidents,omitempty"`
	MetricHighSince map[string]time.Time      `json:"metric_high_since,omitempty"`
}

type Store struct {
	mu                sync.RWMutex
	path              string
	data              diskState
	policy            AlertPolicy
	created           bool
	lastHeartbeatSave time.Time
}

func Open(path, adminToken, bootstrapToken string) (*Store, error) {
	s := &Store{
		path:   path,
		data:   diskState{SchemaVersion: CurrentSchemaVersion, Policy: DefaultAlertPolicy(), Nodes: map[string]*protocol.Node{}, Alerts: map[string]protocol.Alert{}, MetricHighSince: map[string]time.Time{}},
		policy: DefaultAlertPolicy(),
	}
	b, err := readStateFile(path)
	switch {
	case err == nil:
		var loaded diskState
		if err := json.Unmarshal(b, &loaded); err != nil {
			return nil, fmt.Errorf("decode state: %w", err)
		}
		s.data = loaded
		if s.data.SchemaVersion > CurrentSchemaVersion {
			return nil, fmt.Errorf("state schema %d is newer than supported schema %d", s.data.SchemaVersion, CurrentSchemaVersion)
		}
		if s.data.SchemaVersion == 0 {
			// v0.6 and earlier had no explicit schema or persisted policy.
			// Keep a one-time pre-migration backup containing credentials as 0600.
			backup := path + ".schema0.bak"
			if _, statErr := os.Stat(backup); errors.Is(statErr, os.ErrNotExist) {
				if err := atomicfile.WriteFile(backup, b, 0o600); err != nil {
					return nil, fmt.Errorf("backup legacy state: %w", err)
				}
			}
			s.data.SchemaVersion = CurrentSchemaVersion
			s.data.Policy = DefaultAlertPolicy()
		}
	case errors.Is(err, os.ErrNotExist):
		s.created = true
	default:
		return nil, err
	}

	if s.data.Nodes == nil {
		s.data.Nodes = map[string]*protocol.Node{}
	}
	if s.data.Alerts == nil {
		s.data.Alerts = map[string]protocol.Alert{}
	}
	if s.data.MetricHighSince == nil {
		s.data.MetricHighSince = map[string]time.Time{}
	}
	if s.data.SchemaVersion == 0 {
		s.data.SchemaVersion = CurrentSchemaVersion
	}
	s.repairStateLocked()
	if err := validateAlertPolicy(s.data.Policy); err != nil {
		return nil, fmt.Errorf("invalid persisted alert policy: %w", err)
	}
	s.policy = s.data.Policy

	if s.data.AdminToken == "" {
		s.data.AdminToken = adminToken
	}
	if s.data.ReadToken == "" {
		tok, err := randomToken(24)
		if err != nil {
			return nil, err
		}
		s.data.ReadToken = tok
	}
	// Preserve the existing join token across restarts. Rotation is explicit.
	if s.data.BootstrapToken == "" {
		s.data.BootstrapToken = bootstrapToken
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) repairStateLocked() {
	// Remove transient alert/timer references to nodes that no longer exist.
	for key, a := range s.data.Alerts {
		if _, ok := s.data.Nodes[a.NodeID]; !ok {
			delete(s.data.Alerts, key)
		}
	}
	for key := range s.data.MetricHighSince {
		parts := strings.Split(key, ":")
		if len(parts) >= 3 {
			if _, ok := s.data.Nodes[parts[1]]; !ok {
				delete(s.data.MetricHighSince, key)
			}
		}
	}
	// v0.6 could leave an incident open when its node was administratively
	// deleted. Close such historical incidents deterministically during load.
	for i := range s.data.Incidents {
		inc := &s.data.Incidents[i]
		if inc.Status == "open" {
			if _, ok := s.data.Nodes[inc.NodeID]; !ok {
				resolved := inc.LastEventAt
				if resolved.IsZero() {
					resolved = inc.StartedAt
				}
				inc.Status = "resolved"
				inc.ResolvedAt = &resolved
				inc.DurationSec = maxInt64(0, int64(resolved.Sub(inc.StartedAt).Seconds()))
				inc.Summary = incidentSummary(*inc)
			}
		}
	}
}

func validateAlertPolicy(p AlertPolicy) error {
	if p.CPUThreshold < 0 || p.CPUThreshold > 100 || p.RAMThreshold < 0 || p.RAMThreshold > 100 || p.DiskThreshold < 0 || p.DiskThreshold > 100 {
		return fmt.Errorf("alert thresholds must be between 0 and 100")
	}
	if p.MetricFor < 0 || p.UnstableAfter <= 0 || p.OfflineAfter <= p.UnstableAfter {
		return fmt.Errorf("invalid alert durations")
	}
	return nil
}

func (s *Store) SetAlertPolicy(p AlertPolicy) error {
	if err := validateAlertPolicy(p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	oldPolicy := s.policy
	s.policy = p
	s.data.Policy = p
	if err := s.saveLocked(); err != nil {
		s.policy = oldPolicy
		s.data.Policy = oldPolicy
		return err
	}
	return nil
}

func (s *Store) AlertPolicy() AlertPolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.policy
}

func (s *Store) SchemaVersion() int { return CurrentSchemaVersion }

func (s *Store) IsNew() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.created
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	s.data.SchemaVersion = CurrentSchemaVersion
	s.data.Policy = s.policy
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.path, b, 0o600)
}

func (s *Store) Flush() error { s.mu.Lock(); defer s.mu.Unlock(); return s.saveLocked() }
func (s *Store) BootstrapToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.BootstrapToken
}
func (s *Store) AdminToken() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.data.AdminToken }
func (s *Store) ReadToken() string  { s.mu.RLock(); defer s.mu.RUnlock(); return s.data.ReadToken }

func (s *Store) RotateToken(kind string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kind = strings.ToLower(strings.TrimSpace(kind))
	n := 32
	if kind == "join" {
		n = 18
	} else if kind == "read" {
		n = 24
	} else if kind != "admin" {
		return "", fmt.Errorf("token kind must be join, read, or admin")
	}
	tok, err := randomToken(n)
	if err != nil {
		return "", err
	}
	oldJoin, oldRead, oldAdmin := s.data.BootstrapToken, s.data.ReadToken, s.data.AdminToken
	switch kind {
	case "join":
		s.data.BootstrapToken = tok
	case "read":
		s.data.ReadToken = tok
	case "admin":
		s.data.AdminToken = tok
	}
	if err := s.saveLocked(); err != nil {
		s.data.BootstrapToken, s.data.ReadToken, s.data.AdminToken = oldJoin, oldRead, oldAdmin
		return "", err
	}
	return tok, nil
}

func (s *Store) AddNode(n *protocol.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nameExistsLocked(n.Name, "") {
		return ErrNameExists
	}
	if len(s.data.Nodes) >= maxNodes {
		return ErrNodeLimit
	}
	s.data.Nodes[n.ID] = n
	if err := s.saveLocked(); err != nil {
		delete(s.data.Nodes, n.ID)
		return err
	}
	return nil
}

func (s *Store) AuthenticateNode(id, secret string) (*protocol.Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.data.Nodes[id]
	if !ok || subtle.ConstantTimeCompare([]byte(n.Secret), []byte(secret)) != 1 {
		return nil, false
	}
	cpy := *n
	return &cpy, true
}

func (s *Store) Heartbeat(id string, m protocol.Metrics, services []protocol.ServiceStatus, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.data.Nodes[id]
	if !ok {
		return os.ErrNotExist
	}
	now = now.UTC()
	changed := false
	before := statusOf(n, now, s.policy)

	if before != "ONLINE" {
		if s.resolveAlertLocked("availability:"+id, n, "node_recovered", "info", "node", fmt.Sprintf("node recovered after being %s", strings.ToLower(before)), now) {
			changed = true
		}
	}
	if s.updateServicesLocked(n, services, now) {
		changed = true
	}
	n.Metrics = m
	n.Services = append([]protocol.ServiceStatus(nil), services...)
	n.LastHeartbeat = now
	if s.updateMetricsLocked(n, now) {
		changed = true
	}
	if s.reconcileIncidentLocked(id, now) {
		changed = true
	}

	if changed || s.lastHeartbeatSave.IsZero() || now.Sub(s.lastHeartbeatSave) >= 30*time.Second {
		if err := s.saveLocked(); err != nil {
			return err
		}
		s.lastHeartbeatSave = now
	}
	return nil
}

func (s *Store) Evaluate(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now = now.UTC()
	changed := false
	for _, n := range s.data.Nodes {
		state := statusOf(n, now, s.policy)
		key := "availability:" + n.ID
		switch state {
		case "ONLINE":
			if s.resolveAlertLocked(key, n, "node_recovered", "info", "node", "node heartbeat recovered", now) {
				changed = true
			}
		case "UNSTABLE":
			if s.setAlertLocked(key, n, "node_unstable", "warning", "node", fmt.Sprintf("no heartbeat for more than %s", s.policy.UnstableAfter), now, nil, nil) {
				changed = true
			}
		case "OFFLINE":
			if s.setAlertLocked(key, n, "node_offline", "critical", "node", fmt.Sprintf("no heartbeat for more than %s", s.policy.OfflineAfter), now, nil, nil) {
				changed = true
			}
		}
		if s.reconcileIncidentLocked(n.ID, now) {
			changed = true
		}
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

func (s *Store) updateServicesLocked(n *protocol.Node, next []protocol.ServiceStatus, now time.Time) bool {
	changed := false
	seen := map[string]bool{}
	for _, svc := range next {
		name := strings.ToLower(strings.TrimSpace(svc.Name))
		if name == "" {
			continue
		}
		seen[name] = true
		key := "service:" + n.ID + ":" + name
		if !svc.Healthy {
			msg := strings.TrimSpace(svc.Message)
			if msg == "" {
				msg = "service health check failed"
			}
			if s.setAlertLocked(key, n, "service_down", "critical", svc.Name, msg, now, nil, nil) {
				changed = true
			}
		} else if s.resolveAlertLocked(key, n, "service_recovered", "info", svc.Name, "service health check recovered", now) {
			changed = true
		}
	}
	for key, a := range s.data.Alerts {
		if a.NodeID != n.ID || a.Kind != "service_down" {
			continue
		}
		name := strings.TrimPrefix(key, "service:"+n.ID+":")
		if !seen[name] {
			if s.resolveAlertLocked(key, n, "service_cleared", "info", a.Subject, "service check removed from agent", now) {
				changed = true
			}
		}
	}
	return changed
}

func (s *Store) updateMetricsLocked(n *protocol.Node, now time.Time) bool {
	changed := false
	metrics := []struct {
		name             string
		value, threshold float64
	}{
		{"cpu", n.Metrics.CPUPercent, s.policy.CPUThreshold},
		{"ram", n.Metrics.RAMPercent, s.policy.RAMThreshold},
		{"disk", n.Metrics.DiskPercent, s.policy.DiskThreshold},
	}
	for _, x := range metrics {
		key := "metric:" + n.ID + ":" + x.name
		if x.value >= x.threshold {
			since, ok := s.data.MetricHighSince[key]
			if !ok {
				since = now
				s.data.MetricHighSince[key] = since
				changed = true
			}
			if now.Sub(since) >= s.policy.MetricFor {
				v, th := x.value, x.threshold
				if s.setAlertSinceLocked(key, n, "metric_high", "warning", strings.ToUpper(x.name), fmt.Sprintf("%s usage %.1f%% is at/above %.1f%%", strings.ToUpper(x.name), x.value, x.threshold), since, now, &v, &th) {
					changed = true
				}
			}
		} else {
			if _, ok := s.data.MetricHighSince[key]; ok {
				delete(s.data.MetricHighSince, key)
				changed = true
			}
			if s.resolveAlertLocked(key, n, "metric_recovered", "info", strings.ToUpper(x.name), fmt.Sprintf("%s usage recovered to %.1f%%", strings.ToUpper(x.name), x.value), now) {
				changed = true
			}
		}
	}
	return changed
}

func (s *Store) setAlertLocked(key string, n *protocol.Node, kind, severity, subject, message string, now time.Time, value, threshold *float64) bool {
	return s.setAlertSinceLocked(key, n, kind, severity, subject, message, now, now, value, threshold)
}

func (s *Store) setAlertSinceLocked(key string, n *protocol.Node, kind, severity, subject, message string, since, now time.Time, value, threshold *float64) bool {
	if old, ok := s.data.Alerts[key]; ok {
		if old.Kind == kind {
			old.NodeName, old.Subject, old.Message, old.UpdatedAt, old.Value, old.Threshold = n.Name, subject, message, now, value, threshold
			s.data.Alerts[key] = old
			return false
		}
		// A severity/state transition (for example UNSTABLE -> OFFLINE) is a new event.
	}
	a := protocol.Alert{Key: key, NodeID: n.ID, NodeName: n.Name, Kind: kind, Severity: severity, Subject: subject, Message: message, Since: since, UpdatedAt: now, Value: value, Threshold: threshold}
	s.data.Alerts[key] = a
	s.addEventLocked(n, kind, severity, subject, message, now)
	return true
}

func (s *Store) resolveAlertLocked(key string, n *protocol.Node, eventKind, severity, subject, message string, now time.Time) bool {
	if _, ok := s.data.Alerts[key]; !ok {
		return false
	}
	delete(s.data.Alerts, key)
	s.addEventLocked(n, eventKind, severity, subject, message, now)
	return true
}

func (s *Store) addEventLocked(n *protocol.Node, kind, severity, subject, message string, now time.Time) {
	id, err := randomToken(9)
	if err != nil {
		id = fmt.Sprintf("evt-%d", now.UnixNano())
	}
	e := protocol.Event{ID: id, OccurredAt: now.UTC(), NodeID: n.ID, NodeName: n.Name, Kind: kind, Severity: severity, Subject: subject, Message: message}
	s.data.Events = append(s.data.Events, e)
	if len(s.data.Events) > maxEvents {
		s.data.Events = append([]protocol.Event(nil), s.data.Events[len(s.data.Events)-maxEvents:]...)
	}
	s.correlateIncidentLocked(e)
}

func (s *Store) correlateIncidentLocked(e protocol.Event) {
	isProblem := incidentProblemKind(e.Kind)
	idx := s.openIncidentIndexLocked(e.NodeID)
	if idx < 0 && !isProblem {
		return
	}
	if idx < 0 {
		id, err := randomToken(9)
		if err != nil {
			id = fmt.Sprintf("inc-%d", e.OccurredAt.UnixNano())
		}
		s.data.Incidents = append(s.data.Incidents, protocol.Incident{
			ID: id, NodeID: e.NodeID, NodeName: e.NodeName, Status: "open", Severity: e.Severity,
			StartedAt: e.OccurredAt, LastEventAt: e.OccurredAt, EventIDs: []string{}, Timeline: []protocol.Event{}, Kinds: []string{}, Subjects: []string{},
		})
		idx = len(s.data.Incidents) - 1
	}
	inc := &s.data.Incidents[idx]
	inc.NodeName = e.NodeName
	inc.LastEventAt = e.OccurredAt
	inc.EventIDs = append(inc.EventIDs, e.ID)
	inc.Timeline = append(inc.Timeline, e)
	inc.EventCount = len(inc.EventIDs)
	inc.Kinds = appendUnique(inc.Kinds, e.Kind)
	if strings.TrimSpace(e.Subject) != "" {
		inc.Subjects = appendUnique(inc.Subjects, e.Subject)
	}
	if severityRank(e.Severity) < severityRank(inc.Severity) {
		inc.Severity = e.Severity
	}
	inc.DurationSec = maxInt64(0, int64(e.OccurredAt.Sub(inc.StartedAt).Seconds()))
	inc.Title = incidentTitle(*inc)
	inc.Summary = incidentSummary(*inc)

	s.pruneIncidentsLocked()
}

func (s *Store) reconcileIncidentLocked(nodeID string, now time.Time) bool {
	if s.activeAlertCountLocked(nodeID) != 0 {
		return false
	}
	idx := s.openIncidentIndexLocked(nodeID)
	if idx < 0 {
		return false
	}
	inc := &s.data.Incidents[idx]
	resolved := now.UTC()
	if resolved.Before(inc.LastEventAt) {
		resolved = inc.LastEventAt
	}
	inc.Status = "resolved"
	inc.ResolvedAt = &resolved
	inc.DurationSec = maxInt64(0, int64(resolved.Sub(inc.StartedAt).Seconds()))
	inc.Summary = incidentSummary(*inc)
	return true
}

func incidentProblemKind(kind string) bool {
	switch kind {
	case "node_unstable", "node_offline", "service_down", "metric_high":
		return true
	default:
		return false
	}
}

func (s *Store) openIncidentIndexLocked(nodeID string) int {
	for i := len(s.data.Incidents) - 1; i >= 0; i-- {
		if s.data.Incidents[i].NodeID == nodeID && s.data.Incidents[i].Status == "open" {
			return i
		}
	}
	return -1
}

func (s *Store) activeAlertCountLocked(nodeID string) int {
	n := 0
	for _, a := range s.data.Alerts {
		if a.NodeID == nodeID {
			n++
		}
	}
	return n
}

func (s *Store) pruneIncidentsLocked() {
	for len(s.data.Incidents) > maxIncidents {
		idx := -1
		for i := range s.data.Incidents {
			if s.data.Incidents[i].Status == "resolved" {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		s.data.Incidents = append(s.data.Incidents[:idx], s.data.Incidents[idx+1:]...)
	}
}

func appendUnique(v []string, x string) []string {
	for _, cur := range v {
		if strings.EqualFold(cur, x) {
			return v
		}
	}
	return append(v, x)
}

func severityRank(v string) int {
	switch strings.ToLower(v) {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}

func incidentTitle(inc protocol.Incident) string {
	if containsFold(inc.Kinds, "node_offline") {
		return "Node outage"
	}
	if containsFold(inc.Kinds, "service_down") {
		return "Service disruption"
	}
	if containsFold(inc.Kinds, "metric_high") {
		return "Resource pressure"
	}
	if containsFold(inc.Kinds, "node_unstable") {
		return "Heartbeat disruption"
	}
	return "Node incident"
}

func incidentSummary(inc protocol.Incident) string {
	state := "ongoing"
	if inc.Status == "resolved" {
		state = "resolved"
	}
	return fmt.Sprintf("%s on %s: %d correlated events, %s", inc.Title, inc.NodeName, inc.EventCount, state)
}

func containsFold(v []string, x string) bool {
	for _, cur := range v {
		if strings.EqualFold(cur, x) {
			return true
		}
	}
	return false
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func cloneDiskState(in diskState) (diskState, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return diskState{}, err
	}
	var out diskState
	if err := json.Unmarshal(b, &out); err != nil {
		return diskState{}, err
	}
	return out, nil
}

func (s *Store) RenameNode(ref, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, n := s.resolveLocked(ref)
	if n == nil {
		return os.ErrNotExist
	}
	if s.nameExistsLocked(newName, id) {
		return ErrNameExists
	}
	before, err := cloneDiskState(s.data)
	if err != nil {
		return err
	}
	n.Name = newName
	for key, a := range s.data.Alerts {
		if a.NodeID == id {
			a.NodeName = newName
			s.data.Alerts[key] = a
		}
	}
	for i := range s.data.Incidents {
		if s.data.Incidents[i].NodeID == id {
			s.data.Incidents[i].NodeName = newName
			s.data.Incidents[i].Summary = incidentSummary(s.data.Incidents[i])
		}
	}
	if err := s.saveLocked(); err != nil {
		s.data = before
		return err
	}
	return nil
}
func (s *Store) DeleteNode(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, n := s.resolveLocked(ref)
	if n == nil {
		return os.ErrNotExist
	}
	before, err := cloneDiskState(s.data)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if idx := s.openIncidentIndexLocked(id); idx >= 0 && !now.After(s.data.Incidents[idx].LastEventAt) {
		now = s.data.Incidents[idx].LastEventAt.Add(time.Nanosecond)
	}
	// If the node is removed while an incident is open, preserve the final
	// administrative transition and close the incident instead of leaving a
	// permanently-open ghost incident.
	s.addEventLocked(n, "node_removed", "info", "node", "node removed from registry", now)
	for key, a := range s.data.Alerts {
		if a.NodeID == id {
			delete(s.data.Alerts, key)
		}
	}
	for key := range s.data.MetricHighSince {
		if strings.Contains(key, ":"+id+":") {
			delete(s.data.MetricHighSince, key)
		}
	}
	_ = s.reconcileIncidentLocked(id, now)
	delete(s.data.Nodes, id)
	if err := s.saveLocked(); err != nil {
		s.data = before
		return err
	}
	return nil
}
func (s *Store) Views(now time.Time) []protocol.NodeView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]protocol.NodeView, 0, len(s.data.Nodes))
	for _, n := range s.data.Nodes {
		out = append(out, viewOf(n, now, s.policy))
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Store) ViewByNameOrID(v string, now time.Time) (protocol.NodeView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, n := s.resolveLocked(v)
	if n == nil {
		return protocol.NodeView{}, false
	}
	return viewOf(n, now, s.policy), true
}

func (s *Store) Alerts(ref string) []protocol.Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := ""
	if strings.TrimSpace(ref) != "" {
		rid, n := s.resolveLocked(ref)
		if n == nil {
			return []protocol.Alert{}
		}
		id = rid
	}
	out := make([]protocol.Alert, 0, len(s.data.Alerts))
	for _, a := range s.data.Alerts {
		if id == "" || a.NodeID == id {
			out = append(out, a)
		}
	}
	severityRank := func(v string) int {
		if v == "critical" {
			return 0
		}
		if v == "warning" {
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := severityRank(out[i].Severity), severityRank(out[j].Severity)
		if ri != rj {
			return ri < rj
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}

func (s *Store) Events(limit int, ref string) []protocol.Event {
	return s.EventsSince(limit, ref, time.Time{})
}

func (s *Store) EventsSince(limit int, ref string, since time.Time) []protocol.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	since = since.UTC()
	id := ""
	if strings.TrimSpace(ref) != "" {
		rid, n := s.resolveLocked(ref)
		if n == nil {
			return []protocol.Event{}
		}
		id = rid
	}
	out := make([]protocol.Event, 0, limit)
	for i := len(s.data.Events) - 1; i >= 0 && len(out) < limit; i-- {
		e := s.data.Events[i]
		if !since.IsZero() && e.OccurredAt.Before(since) {
			break
		}
		if id == "" || e.NodeID == id {
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) Incidents(limit int, ref, status string) []protocol.Incident {
	return s.IncidentsSince(limit, ref, status, time.Time{})
}

func (s *Store) IncidentsSince(limit int, ref, status string, since time.Time) []protocol.Incident {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	status = strings.ToLower(strings.TrimSpace(status))
	ref = strings.TrimSpace(ref)
	since = since.UTC()
	out := make([]protocol.Incident, 0, limit)
	for i := len(s.data.Incidents) - 1; i >= 0 && len(out) < limit; i-- {
		inc := s.data.Incidents[i]
		if !since.IsZero() && inc.LastEventAt.Before(since) {
			continue
		}
		if ref != "" && !strings.EqualFold(inc.NodeID, ref) && !strings.EqualFold(inc.NodeName, ref) {
			continue
		}
		if status != "" && inc.Status != status {
			continue
		}
		inc.EventIDs = append([]string(nil), inc.EventIDs...)
		inc.Timeline = nil
		inc.Kinds = append([]string(nil), inc.Kinds...)
		inc.Subjects = append([]string(nil), inc.Subjects...)
		out = append(out, inc)
	}
	return out
}

func (s *Store) Incident(id string) (protocol.IncidentDetail, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id = strings.TrimSpace(id)
	var inc *protocol.Incident
	for i := range s.data.Incidents {
		if strings.EqualFold(s.data.Incidents[i].ID, id) {
			cpy := s.data.Incidents[i]
			inc = &cpy
			break
		}
	}
	if inc == nil {
		return protocol.IncidentDetail{}, false
	}
	events := append([]protocol.Event(nil), inc.Timeline...)
	if len(events) == 0 && len(inc.EventIDs) > 0 {
		wanted := make(map[string]bool, len(inc.EventIDs))
		for _, eventID := range inc.EventIDs {
			wanted[eventID] = true
		}
		for _, e := range s.data.Events {
			if wanted[e.ID] {
				events = append(events, e)
			}
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].OccurredAt.Before(events[j].OccurredAt) })
	inc.Timeline = nil
	return protocol.IncidentDetail{Incident: *inc, Events: events}, true
}

func (s *Store) resolveLocked(ref string) (string, *protocol.Node) {
	if n, ok := s.data.Nodes[ref]; ok {
		return ref, n
	}
	for id, n := range s.data.Nodes {
		if strings.EqualFold(n.Name, ref) {
			return id, n
		}
	}
	return "", nil
}
func (s *Store) nameExistsLocked(name, exceptID string) bool {
	for id, n := range s.data.Nodes {
		if id != exceptID && strings.EqualFold(n.Name, name) {
			return true
		}
	}
	return false
}

func statusOf(n *protocol.Node, now time.Time, p AlertPolicy) string {
	base := n.LastHeartbeat
	if base.IsZero() {
		base = n.RegisteredAt
	}
	if base.IsZero() {
		return "OFFLINE"
	}
	age := now.Sub(base)
	if age <= p.UnstableAfter {
		return "ONLINE"
	}
	if age <= p.OfflineAfter {
		return "UNSTABLE"
	}
	return "OFFLINE"
}
func viewOf(n *protocol.Node, now time.Time, p AlertPolicy) protocol.NodeView {
	return protocol.NodeView{ID: n.ID, Name: n.Name, OS: n.OS, Arch: n.Arch, AgentVersion: n.AgentVersion, RegisteredAt: n.RegisteredAt, LastHeartbeat: n.LastHeartbeat, Status: statusOf(n, now, p), Metrics: n.Metrics, Services: append([]protocol.ServiceStatus(nil), n.Services...)}
}
func HealthyServiceCount(v protocol.NodeView) (healthy, total int) {
	for _, svc := range v.Services {
		total++
		if svc.Healthy {
			healthy++
		}
	}
	return
}
func ValidateNodeName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return fmt.Errorf("node name must be 1-64 characters")
	}
	return nil
}
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
