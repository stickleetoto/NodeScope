//go:build linux

package metrics

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	maxFilesystems = 32
	maxDiskDevices = 32
)

var ignoredFilesystemTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"cgroup": true, "cgroup2": true, "securityfs": true, "pstore": true, "debugfs": true,
	"tracefs": true, "configfs": true, "fusectl": true, "mqueue": true, "hugetlbfs": true,
}

type filesystemCollector struct{}

func (filesystemCollector) Name() string { return "filesystem" }

func (filesystemCollector) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ts := time.Now().UTC()
	var out []Sample
	seen := map[string]bool{}
	count := 0
	truncated := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		mount := unescapeMount(fields[1])
		fsType := fields[2]
		if ignoredFilesystemTypes[fsType] || seen[mount] {
			continue
		}
		seen[mount] = true
		if count >= maxFilesystems {
			truncated = true
			continue
		}
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var st syscall.Statfs_t
		if err := syscall.Statfs(mount, &st); err != nil {
			continue
		}
		total := st.Blocks * uint64(st.Bsize)
		avail := st.Bavail * uint64(st.Bsize)
		used := uint64(0)
		if total >= avail {
			used = total - avail
		}
		attrs := map[string]string{"mount": mount, "fstype": fsType}
		out = append(out,
			Sample{Name: "system.filesystem.usage", Value: float64(used), Unit: "By", Kind: Gauge, Timestamp: ts, Attributes: attrs},
			Sample{Name: "system.filesystem.limit", Value: float64(total), Unit: "By", Kind: Gauge, Timestamp: ts, Attributes: attrs},
		)
		if total > 0 {
			out = append(out, Sample{Name: "system.filesystem.utilization", Value: float64(used) / float64(total), Unit: "1", Kind: Gauge, Timestamp: ts, Attributes: attrs})
		}
		count++
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	if truncated {
		return out, fmt.Errorf("filesystem limit %d reached", maxFilesystems)
	}
	return out, nil
}

func unescapeMount(v string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, "\\")
	return replacer.Replace(v)
}

type diskIOCollector struct{}

func (diskIOCollector) Name() string { return "diskio" }

func (diskIOCollector) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return nil, err
	}
	return parseDiskStats(b, time.Now().UTC())
}

func parseDiskStats(b []byte, ts time.Time) ([]Sample, error) {
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	var out []Sample
	count := 0
	truncated := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 14 {
			continue
		}
		dev := fields[2]
		if strings.HasPrefix(dev, "loop") || strings.HasPrefix(dev, "ram") || strings.HasPrefix(dev, "fd") {
			continue
		}
		if count >= maxDiskDevices {
			truncated = true
			continue
		}
		vals := make([]uint64, len(fields)-3)
		valid := true
		for i, raw := range fields[3:] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				valid = false
				break
			}
			vals[i] = v
		}
		if !valid || len(vals) < 11 {
			continue
		}
		attrsRead := map[string]string{"device": dev, "direction": "read"}
		attrsWrite := map[string]string{"device": dev, "direction": "write"}
		out = append(out,
			Sample{Name: "system.disk.operations", Value: float64(vals[0]), Unit: "{operation}", Kind: Counter, Timestamp: ts, Attributes: attrsRead},
			Sample{Name: "system.disk.io", Value: float64(vals[2] * 512), Unit: "By", Kind: Counter, Timestamp: ts, Attributes: attrsRead},
			Sample{Name: "system.disk.operation.time", Value: float64(vals[3]), Unit: "ms", Kind: Counter, Timestamp: ts, Attributes: attrsRead},
			Sample{Name: "system.disk.operations", Value: float64(vals[4]), Unit: "{operation}", Kind: Counter, Timestamp: ts, Attributes: attrsWrite},
			Sample{Name: "system.disk.io", Value: float64(vals[6] * 512), Unit: "By", Kind: Counter, Timestamp: ts, Attributes: attrsWrite},
			Sample{Name: "system.disk.operation.time", Value: float64(vals[7]), Unit: "ms", Kind: Counter, Timestamp: ts, Attributes: attrsWrite},
			Sample{Name: "system.disk.operations.in_progress", Value: float64(vals[8]), Unit: "{operation}", Kind: Gauge, Timestamp: ts, Attributes: map[string]string{"device": dev}},
			Sample{Name: "system.disk.io.time", Value: float64(vals[9]), Unit: "ms", Kind: Counter, Timestamp: ts, Attributes: map[string]string{"device": dev}},
			Sample{Name: "system.disk.weighted_io.time", Value: float64(vals[10]), Unit: "ms", Kind: Counter, Timestamp: ts, Attributes: map[string]string{"device": dev}},
		)
		count++
	}
	if err := sc.Err(); err != nil {
		return out, err
	}
	if truncated {
		return out, fmt.Errorf("disk device limit %d reached", maxDiskDevices)
	}
	return out, nil
}
