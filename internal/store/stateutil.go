package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/atomicfile"
)

const maxStateBytes int64 = 64 << 20

func readStateFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxStateBytes {
		return nil, fmt.Errorf("state file is too large: %d bytes (maximum %d)", st.Size(), maxStateBytes)
	}
	return os.ReadFile(path)
}

type StateInfo struct {
	SchemaVersion int         `json:"schema_version"`
	Legacy        bool        `json:"legacy"`
	Nodes         int         `json:"nodes"`
	Alerts        int         `json:"alerts"`
	Events        int         `json:"events"`
	Incidents     int         `json:"incidents"`
	Policy        AlertPolicy `json:"policy"`
}

func inspectStateBytes(b []byte) (StateInfo, diskState, error) {
	var d diskState
	if err := json.Unmarshal(b, &d); err != nil {
		return StateInfo{}, diskState{}, fmt.Errorf("decode state: %w", err)
	}
	legacy := d.SchemaVersion == 0
	if d.SchemaVersion > CurrentSchemaVersion {
		return StateInfo{}, diskState{}, fmt.Errorf("state schema %d is newer than supported schema %d", d.SchemaVersion, CurrentSchemaVersion)
	}
	if legacy {
		d.SchemaVersion = CurrentSchemaVersion
		d.Policy = DefaultAlertPolicy()
	}
	if err := validateAlertPolicy(d.Policy); err != nil {
		return StateInfo{}, diskState{}, fmt.Errorf("invalid alert policy: %w", err)
	}
	if d.AdminToken == "" || d.ReadToken == "" || d.BootstrapToken == "" {
		return StateInfo{}, diskState{}, fmt.Errorf("state is missing required credentials")
	}
	seenNames := map[string]string{}
	for id, n := range d.Nodes {
		if n == nil {
			return StateInfo{}, diskState{}, fmt.Errorf("node %q is null", id)
		}
		if n.ID == "" || n.Secret == "" {
			return StateInfo{}, diskState{}, fmt.Errorf("node %q is missing id or secret", id)
		}
		if n.ID != id {
			return StateInfo{}, diskState{}, fmt.Errorf("node map key %q does not match node id %q", id, n.ID)
		}
		if err := ValidateNodeName(n.Name); err != nil {
			return StateInfo{}, diskState{}, fmt.Errorf("node %q: %w", id, err)
		}
		folded := strings.ToLower(n.Name)
		if prev, ok := seenNames[folded]; ok {
			return StateInfo{}, diskState{}, fmt.Errorf("duplicate node name %q on %s and %s", n.Name, prev, id)
		}
		seenNames[folded] = id
	}
	if !legacy {
		for key, a := range d.Alerts {
			if _, ok := d.Nodes[a.NodeID]; !ok {
				return StateInfo{}, diskState{}, fmt.Errorf("alert %q references missing node %q", key, a.NodeID)
			}
		}
		for _, inc := range d.Incidents {
			if inc.Status == "open" {
				if _, ok := d.Nodes[inc.NodeID]; !ok {
					return StateInfo{}, diskState{}, fmt.Errorf("open incident %q references missing node %q", inc.ID, inc.NodeID)
				}
			}
		}
	}
	return StateInfo{
		SchemaVersion: d.SchemaVersion,
		Legacy:        legacy,
		Nodes:         len(d.Nodes),
		Alerts:        len(d.Alerts),
		Events:        len(d.Events),
		Incidents:     len(d.Incidents),
		Policy:        d.Policy,
	}, d, nil
}

func InspectState(path string) (StateInfo, error) {
	b, err := readStateFile(path)
	if err != nil {
		return StateInfo{}, err
	}
	info, _, err := inspectStateBytes(b)
	return info, err
}

func BackupState(path, out string) (StateInfo, error) {
	b, err := readStateFile(path)
	if err != nil {
		return StateInfo{}, err
	}
	info, _, err := inspectStateBytes(b)
	if err != nil {
		return StateInfo{}, err
	}
	if filepath.Clean(path) == filepath.Clean(out) {
		return StateInfo{}, fmt.Errorf("backup destination must differ from state path")
	}
	if err := atomicfile.WriteFile(out, b, 0o600); err != nil {
		return StateInfo{}, err
	}
	return info, nil
}

// RestoreState validates backup, preserves the current state as a timestamped
// pre-restore backup, and atomically replaces path. The caller must ensure the
// central server is not running against path (the CLI enforces this with the
// same state lock used by `jjp host`).
func RestoreState(path, backup string) (StateInfo, string, error) {
	b, err := readStateFile(backup)
	if err != nil {
		return StateInfo{}, "", err
	}
	info, _, err := inspectStateBytes(b)
	if err != nil {
		return StateInfo{}, "", err
	}
	if filepath.Clean(path) == filepath.Clean(backup) {
		return StateInfo{}, "", fmt.Errorf("backup path must differ from state path")
	}
	pre := ""
	if current, err := readStateFile(path); err == nil {
		if _, _, inspectErr := inspectStateBytes(current); inspectErr != nil {
			return StateInfo{}, "", fmt.Errorf("current state is invalid; refusing automatic overwrite: %w", inspectErr)
		}
		pre = fmt.Sprintf("%s.pre-restore-%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
		if err := atomicfile.WriteFile(pre, current, 0o600); err != nil {
			return StateInfo{}, "", fmt.Errorf("preserve current state: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return StateInfo{}, "", err
	}
	if err := atomicfile.WriteFile(path, b, 0o600); err != nil {
		return StateInfo{}, pre, err
	}
	return info, pre, nil
}
