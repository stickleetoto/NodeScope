//go:build linux

package metrics

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stickleetoto/NodeScope/internal/protocol"
)

func collectLegacy() (protocol.Metrics, error) {
	cpu, err := cpuPercent()
	if err != nil {
		return protocol.Metrics{}, err
	}
	used, total, err := memory()
	if err != nil {
		return protocol.Metrics{}, err
	}
	du, dt, err := disk()
	if err != nil {
		return protocol.Metrics{}, err
	}
	up, err := uptime()
	if err != nil {
		return protocol.Metrics{}, err
	}
	m := protocol.Metrics{CPUPercent: cpu, RAMUsedBytes: used, RAMTotalBytes: total, DiskUsedBytes: du, DiskTotalBytes: dt, UptimeSeconds: up}
	if total > 0 {
		m.RAMPercent = float64(used) * 100 / float64(total)
	}
	if dt > 0 {
		m.DiskPercent = float64(du) * 100 / float64(dt)
	}
	m.TemperatureC = temperature()
	return m, nil
}

type cpuSample struct{ idle, total uint64 }

func readCPU() (cpuSample, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuSample{}, err
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuSample{}, fmt.Errorf("unexpected /proc/stat")
	}
	var vals []uint64
	for _, x := range f[1:] {
		v, e := strconv.ParseUint(x, 10, 64)
		if e != nil {
			return cpuSample{}, e
		}
		vals = append(vals, v)
	}
	var total uint64
	for _, v := range vals {
		total += v
	}
	idle := vals[3]
	if len(vals) > 4 {
		idle += vals[4]
	}
	return cpuSample{idle: idle, total: total}, nil
}
func cpuPercent() (float64, error) {
	a, err := readCPU()
	if err != nil {
		return 0, err
	}
	time.Sleep(120 * time.Millisecond)
	b, err := readCPU()
	if err != nil {
		return 0, err
	}
	dt, di := b.total-a.total, b.idle-a.idle
	if dt == 0 {
		return 0, nil
	}
	return float64(dt-di) * 100 / float64(dt), nil
}
func memory() (uint64, uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var total, avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Fields(sc.Text())
		if len(p) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(p[1], 10, 64)
		switch strings.TrimSuffix(p[0], ":") {
		case "MemTotal":
			total = v * 1024
		case "MemAvailable":
			avail = v * 1024
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("MemTotal missing")
	}
	return total - avail, total, nil
}
func disk() (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0, 0, err
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	return total - free, total, nil
}
func uptime() (uint64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0, fmt.Errorf("invalid uptime")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return uint64(v), err
}
func temperature() *float64 {
	paths := []string{"/sys/class/thermal/thermal_zone0/temp", "/sys/class/hwmon/hwmon0/temp1_input"}
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil {
			continue
		}
		v, e := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if e == nil {
			if v > 1000 {
				v /= 1000
			}
			return &v
		}
	}
	return nil
}
