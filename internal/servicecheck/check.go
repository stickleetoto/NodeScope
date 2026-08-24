package servicecheck

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
)

var httpClient = &http.Client{Timeout: 3 * time.Second}

func CheckAll(ctx context.Context, specs []protocol.ServiceSpec) []protocol.ServiceStatus {
	if len(specs) == 0 {
		return nil
	}
	out := make([]protocol.ServiceStatus, 0, len(specs))
	ch := make(chan protocol.ServiceStatus, len(specs))
	var wg sync.WaitGroup
	for _, spec := range specs {
		spec := spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch <- Check(ctx, spec)
		}()
	}
	wg.Wait()
	close(ch)
	for s := range ch {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func Check(parent context.Context, spec protocol.ServiceSpec) protocol.ServiceStatus {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	start := time.Now()
	st := protocol.ServiceStatus{Name: spec.Name, Type: spec.Type, Target: spec.Target}
	var err error
	switch spec.Type {
	case "tcp":
		var conn net.Conn
		var d net.Dialer
		conn, err = d.DialContext(ctx, "tcp", spec.Target)
		if conn != nil {
			_ = conn.Close()
		}
	case "http":
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, spec.Target, nil)
		if err == nil {
			var resp *http.Response
			resp, err = httpClient.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
				if err == nil && (resp.StatusCode < 200 || resp.StatusCode >= 400) {
					err = fmt.Errorf("HTTP %d", resp.StatusCode)
				}
			}
		}
	case "systemd":
		if runtime.GOOS != "linux" {
			err = fmt.Errorf("systemd checks require Linux")
		} else {
			err = checkSystemd(ctx, spec.Target)
		}
	default:
		err = fmt.Errorf("unsupported check type %q", spec.Type)
	}
	st.LatencyMS = time.Since(start).Milliseconds()
	st.Healthy = err == nil
	if err != nil {
		st.Message = concise(err.Error(), 160)
	} else {
		st.Message = "ok"
	}
	return st
}

func concise(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
