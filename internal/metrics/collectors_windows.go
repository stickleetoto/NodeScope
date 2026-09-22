//go:build windows

package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type windowsExtendedCollector struct{}

func (windowsExtendedCollector) Name() string { return "windows" }

type windowsExtendedSnapshot struct {
	Processes int `json:"Processes"`
	PageTotal float64 `json:"PageTotal"`
	PageUsed  float64 `json:"PageUsed"`
	Disks []struct {
		Device string  `json:"Device"`
		Size   float64 `json:"Size"`
		Free   float64 `json:"Free"`
	} `json:"Disks"`
}

func (windowsExtendedCollector) Collect(ctx context.Context) ([]Sample, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ps := `$disks=@(Get-CimInstance Win32_LogicalDisk -Filter "DriveType=3" | ForEach-Object {[pscustomobject]@{Device=[string]$_.DeviceID;Size=[double]$_.Size;Free=[double]$_.FreeSpace}});$p=Get-CimInstance Win32_PageFileUsage;$pt=[double](($p|Measure-Object AllocatedBaseSize -Sum).Sum)*1MB;$pu=[double](($p|Measure-Object CurrentUsage -Sum).Sum)*1MB;[pscustomobject]@{Processes=[int](Get-Process).Count;PageTotal=$pt;PageUsed=$pu;Disks=$disks}|ConvertTo-Json -Compress -Depth 4`
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("collect Windows extended metrics: %w", ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	var snap windowsExtendedSnapshot
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &snap); err != nil {
		return nil, fmt.Errorf("parse Windows extended metrics: %w", err)
	}
	ts := time.Now().UTC()
	samples := []Sample{{
		Name: "system.process.count", Value: float64(snap.Processes), Unit: "{process}", Kind: Gauge, Timestamp: ts,
		Attributes: map[string]string{"state": "total"},
	}}
	if snap.PageTotal > 0 {
		samples = append(samples,
			Sample{Name: "system.paging.usage", Value: snap.PageUsed, Unit: "By", Kind: Gauge, Timestamp: ts},
			Sample{Name: "system.paging.limit", Value: snap.PageTotal, Unit: "By", Kind: Gauge, Timestamp: ts},
			Sample{Name: "system.paging.utilization", Value: snap.PageUsed / snap.PageTotal, Unit: "1", Kind: Gauge, Timestamp: ts},
		)
	}
	for _, disk := range snap.Disks {
		if disk.Device == "" || disk.Size <= 0 {
			continue
		}
		used := disk.Size - disk.Free
		if used < 0 {
			used = 0
		}
		attrs := map[string]string{"mount": disk.Device, "fstype": "windows"}
		samples = append(samples,
			Sample{Name: "system.filesystem.usage", Value: used, Unit: "By", Kind: Gauge, Timestamp: ts, Attributes: attrs},
			Sample{Name: "system.filesystem.limit", Value: disk.Size, Unit: "By", Kind: Gauge, Timestamp: ts, Attributes: attrs},
			Sample{Name: "system.filesystem.utilization", Value: used / disk.Size, Unit: "1", Kind: Gauge, Timestamp: ts, Attributes: attrs},
		)
	}
	return samples, nil
}

func platformCollectors() []Collector {
	return []Collector{windowsExtendedCollector{}}
}
