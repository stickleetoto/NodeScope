//go:build windows

package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
)

type winMetrics struct{ CPU, RAMUsed, RAMTotal, DiskUsed, DiskTotal, Uptime float64 }

func Collect() (protocol.Metrics, error) {
	ps := `$os=Get-CimInstance Win32_OperatingSystem;$cpu=(Get-CimInstance Win32_Processor|Measure-Object LoadPercentage -Average).Average;$d=Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='C:'";[pscustomobject]@{CPU=[double]$cpu;RAMUsed=[double](($os.TotalVisibleMemorySize-$os.FreePhysicalMemory)*1024);RAMTotal=[double]($os.TotalVisibleMemorySize*1024);DiskUsed=[double]($d.Size-$d.FreeSpace);DiskTotal=[double]$d.Size;Uptime=[double]((Get-Date)-$os.LastBootUpTime).TotalSeconds}|ConvertTo-Json -Compress`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps).Output()
	if ctx.Err() != nil {
		return protocol.Metrics{}, fmt.Errorf("collect Windows metrics: %w", ctx.Err())
	}
	if err != nil {
		return protocol.Metrics{}, err
	}
	var w winMetrics
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &w); err != nil {
		return protocol.Metrics{}, fmt.Errorf("parse metrics: %w", err)
	}
	m := protocol.Metrics{CPUPercent: w.CPU, RAMUsedBytes: uint64(w.RAMUsed), RAMTotalBytes: uint64(w.RAMTotal), DiskUsedBytes: uint64(w.DiskUsed), DiskTotalBytes: uint64(w.DiskTotal), UptimeSeconds: uint64(w.Uptime)}
	if m.RAMTotalBytes > 0 {
		m.RAMPercent = float64(m.RAMUsedBytes) * 100 / float64(m.RAMTotalBytes)
	}
	if m.DiskTotalBytes > 0 {
		m.DiskPercent = float64(m.DiskUsedBytes) * 100 / float64(m.DiskTotalBytes)
	}
	return m, nil
}
