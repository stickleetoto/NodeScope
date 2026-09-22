package agent

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jjp-monitor/jjp/internal/atomicfile"
	"github.com/jjp-monitor/jjp/internal/protocol"
)

type ServiceSpec = protocol.ServiceSpec

type Config struct {
	Server   string        `json:"server"`
	NodeID   string        `json:"node_id"`
	Secret   string        `json:"secret"`
	Name     string        `json:"name"`
	Services []ServiceSpec `json:"services,omitempty"`
}

func DefaultConfigPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "jjp", "agent.json"), nil
}


func DefaultTelemetryWALPath() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "jjp", "telemetry.wal"), nil
}

func SaveConfig(path string, c Config) error {
	if err := ValidateConfig(c); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, b, 0o600)
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if err := ValidateConfig(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func ValidateConfig(c Config) error {
	if strings.TrimSpace(c.Server) == "" || strings.TrimSpace(c.NodeID) == "" || strings.TrimSpace(c.Secret) == "" {
		return fmt.Errorf("invalid agent config: server, node_id and secret are required")
	}
	if len(c.Services) > 128 {
		return fmt.Errorf("too many services: maximum is 128")
	}
	seen := map[string]bool{}
	for _, s := range c.Services {
		if err := ValidateService(s); err != nil {
			return err
		}
		key := strings.ToLower(s.Name)
		if seen[key] {
			return fmt.Errorf("duplicate service name %q", s.Name)
		}
		seen[key] = true
	}
	return nil
}

func ValidateService(s ServiceSpec) error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 64 {
		return fmt.Errorf("service name must be 1-64 characters")
	}
	if strings.TrimSpace(s.Target) == "" {
		return fmt.Errorf("service %q has an empty target", s.Name)
	}
	switch s.Type {
	case "tcp":
		if _, _, err := net.SplitHostPort(s.Target); err != nil {
			return fmt.Errorf("service %q has invalid TCP target: %w", s.Name, err)
		}
		return nil
	case "http":
		u, err := url.Parse(s.Target)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("service %q has invalid HTTP URL", s.Name)
		}
		return nil
	case "systemd":
		return nil
	default:
		return fmt.Errorf("service %q has unsupported type %q", s.Name, s.Type)
	}
}
