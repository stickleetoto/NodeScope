package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jjp-monitor/jjp/internal/agent"
	"github.com/jjp-monitor/jjp/internal/apiclient"
	"github.com/jjp-monitor/jjp/internal/autostart"
	"github.com/jjp-monitor/jjp/internal/filelock"
	"github.com/jjp-monitor/jjp/internal/history"
	"github.com/jjp-monitor/jjp/internal/mcpserver"
	"github.com/jjp-monitor/jjp/internal/protocol"
	"github.com/jjp-monitor/jjp/internal/server"
	"github.com/jjp-monitor/jjp/internal/store"
)

type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string { return "" }
func (e exitCodeError) ExitCode() int { return e.code }

func main() {
	if err := run(); err != nil {
		var ee interface{ ExitCode() int }
		if errors.As(err, &ee) {
			if strings.TrimSpace(err.Error()) != "" {
				fmt.Fprintln(os.Stderr, "error:", err)
			}
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		usage()
		return nil
	}
	switch os.Args[1] {
	case "host":
		return cmdHost(os.Args[2:])
	case "join":
		return cmdJoin(os.Args[2:])
	case "agent":
		return cmdAgent(os.Args[2:])
	case "install-agent":
		return cmdInstallAgent(os.Args[2:])
	case "service":
		return cmdService(os.Args[2:])
	case "mcp":
		return cmdMCP(os.Args[2:])
	case "metrics":
		return cmdMetrics(os.Args[2:])
	case "token":
		return cmdToken(os.Args[2:])
	case "state":
		return cmdState(os.Args[2:])
	case "overview":
		return cmdOverview(os.Args[2:])
	case "health":
		return cmdHealth(os.Args[2:])
	case "diagnose":
		return cmdDiagnose(os.Args[2:])
	case "doctor":
		return cmdDoctor(os.Args[2:])
	case "ls", "status":
		return cmdList(os.Args[2:])
	case "alerts":
		return cmdAlerts(os.Args[2:])
	case "events":
		return cmdEvents(os.Args[2:])
	case "incidents":
		return cmdIncidents(os.Args[2:])
	case "incident":
		return cmdIncident(os.Args[2:])
	case "show":
		return cmdShow(os.Args[2:])
	case "rename":
		return cmdRename(os.Args[2:])
	case "rm", "remove":
		return cmdRemove(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("nodescope", protocol.Version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func usage() {
	fmt.Print(`nodescope — lightweight distributed node monitoring

Usage:
  nodescope host [--listen :7443] [--data PATH] [--tls-cert FILE --tls-key FILE]
           [--cpu-alert 90] [--ram-alert 90] [--disk-alert 90]
           [--metric-for 5m] [--unstable-after 15s] [--offline-after 60s]
  nodescope join <server> <join-token> [--name NAME] [--interval 5s]
  nodescope agent [--config PATH] [--interval 5s]
  nodescope install-agent [--config PATH] [--interval 5s] [--system]
  nodescope mcp [--server URL] [--token TOKEN] [--allow-write]
  nodescope metrics history <node> <metric> [--attr key=value] [--since 1h] [--limit 500] [--json]
  nodescope metrics trend <node> <metric> [--attr key=value] [--since 1h] [--limit 500] [--json]
  nodescope metrics rollup <node> <metric> [--attr key=value] [--since 24h] [--bucket 1m] [--json]
  nodescope metrics stats [--json]
  nodescope metrics system [--json]
  nodescope token show [--kind all|join|read|admin] [--data PATH]
  nodescope token rotate <join|read|admin> [--server URL] [--admin-token TOKEN]
  nodescope state check [--data PATH] [--json]
  nodescope state backup [--data PATH] [--out PATH]
  nodescope state restore <backup> [--data PATH] --force

  nodescope service add <name> --tcp HOST:PORT
  nodescope service add <name> --http URL
  nodescope service add <name> --systemd UNIT
  nodescope service ls
  nodescope service rm <name>

  nodescope overview [--events 10] [--json] [--server URL] [--token TOKEN]
  nodescope health [--json] [--server URL] [--token TOKEN]
  nodescope diagnose <node> [--json] [--server URL] [--token TOKEN]
  nodescope doctor [--json] [--server URL] [--token TOKEN]
  nodescope ls [--json] [--server URL] [--token TOKEN]
  nodescope alerts [--node NODE] [--json] [--server URL] [--token TOKEN]
  nodescope events [--node NODE] [--since 24h] [--limit 50] [--json] [--server URL] [--token TOKEN]
  nodescope incidents [--node NODE] [--status open|resolved] [--since 24h] [--limit 50] [--json]
  nodescope incident <id> [--json] [--server URL] [--token TOKEN]
  nodescope show <node> [--json] [--server URL] [--token TOKEN]
  nodescope rename <node> <new-name> [--server URL] [--admin-token TOKEN]
  nodescope rm <node> [--server URL] [--admin-token TOKEN]
  nodescope version

Environment:
  JJP_SERVER       central server URL
  JJP_API_TOKEN    read token for API/MCP
  JJP_ADMIN_TOKEN  admin token for write/admin commands
`)
}

func dataDir() (string, error) {
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "jjp"), nil
}

func cmdHost(args []string) error {
	fs := flag.NewFlagSet("host", flag.ContinueOnError)
	listen := fs.String("listen", ":7443", "listen address")
	data := fs.String("data", "", "state file")
	tlsCert := fs.String("tls-cert", "", "TLS certificate PEM file")
	tlsKey := fs.String("tls-key", "", "TLS private key PEM file")
	cpuAlert := fs.String("cpu-alert", "", "CPU alert threshold percent (preserves stored value when omitted)")
	ramAlert := fs.String("ram-alert", "", "RAM alert threshold percent (preserves stored value when omitted)")
	diskAlert := fs.String("disk-alert", "", "disk alert threshold percent (preserves stored value when omitted)")
	metricFor := fs.String("metric-for", "", "metric threshold duration before alert (preserves stored value when omitted)")
	unstableAfter := fs.String("unstable-after", "", "heartbeat age before UNSTABLE (preserves stored value when omitted)")
	offlineAfter := fs.String("offline-after", "", "heartbeat age before OFFLINE (preserves stored value when omitted)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be provided together")
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	if *data == "" {
		*data = filepath.Join(dir, "server.json")
	}
	*data, err = filepath.Abs(*data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*data), 0o700); err != nil {
		return err
	}
	stateLock, err := filelock.Acquire(*data + ".lock")
	if err != nil {
		return err
	}
	defer stateLock.Release()
	bootstrap, err := server.RandomToken(18)
	if err != nil {
		return fmt.Errorf("generate join token: %w", err)
	}
	admin, err := server.RandomToken(32)
	if err != nil {
		return fmt.Errorf("generate admin token: %w", err)
	}
	st, err := store.Open(*data, admin, bootstrap)
	if err != nil {
		return err
	}

	historyDir := filepath.Join(filepath.Dir(*data), "history")
	hist, err := history.Open(historyDir, history.DefaultOptions())
	if err != nil {
		return fmt.Errorf("open telemetry history: %w", err)
	}

	if err := hist.Compact(time.Now().UTC()); err != nil {
		return fmt.Errorf("compact telemetry history: %w", err)
	}

	policy := st.AlertPolicy()
	policyChanged := false
	parsePercent := func(name, raw string, dst *float64) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = v
		policyChanged = true
		return nil
	}
	parseDuration := func(name, raw string, dst *time.Duration) error {
		if strings.TrimSpace(raw) == "" {
			return nil
		}
		v, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*dst = v
		policyChanged = true
		return nil
	}
	if err := parsePercent("--cpu-alert", *cpuAlert, &policy.CPUThreshold); err != nil {
		return err
	}
	if err := parsePercent("--ram-alert", *ramAlert, &policy.RAMThreshold); err != nil {
		return err
	}
	if err := parsePercent("--disk-alert", *diskAlert, &policy.DiskThreshold); err != nil {
		return err
	}
	if err := parseDuration("--metric-for", *metricFor, &policy.MetricFor); err != nil {
		return err
	}
	if err := parseDuration("--unstable-after", *unstableAfter, &policy.UnstableAfter); err != nil {
		return err
	}
	if err := parseDuration("--offline-after", *offlineAfter, &policy.OfflineAfter); err != nil {
		return err
	}
	if policyChanged {
		if err := st.SetAlertPolicy(policy); err != nil {
			return err
		}
	}

	fmt.Println("NodeScope central server")
	fmt.Println("Listen:     ", *listen)
	fmt.Println("State:      ", *data)
	fmt.Println("Schema:     ", st.SchemaVersion())
	fmt.Println("History:    ", historyDir)
	if *tlsCert != "" {
		fmt.Println("Transport:   HTTPS/TLS")
	} else {
		fmt.Println("Transport:   HTTP (use TLS or a trusted private network for remote access)")
	}
	if st.IsNew() {
		fmt.Println("Join token: ", st.BootstrapToken())
		fmt.Println("Read token: ", st.ReadToken())
		fmt.Println("Admin token:", st.AdminToken())
		fmt.Println("Store these tokens securely; existing tokens are not printed on later starts.")
	} else {
		fmt.Println("Tokens:      existing (use 'nodescope token show' locally when needed)")
	}
	fmt.Printf("Alerts:      CPU %.1f%% / RAM %.1f%% / Disk %.1f%% for %s; unstable %s; offline %s\n",
		policy.CPUThreshold, policy.RAMThreshold, policy.DiskThreshold, policy.MetricFor, policy.UnstableAfter, policy.OfflineAfter)
	fmt.Println()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           (&server.Server{Store: st, History: hist}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := st.Evaluate(now.UTC()); err != nil {
					fmt.Fprintln(os.Stderr, "alert evaluator:", err)
				}
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if err := hist.Compact(now.UTC()); err != nil {
					fmt.Fprintln(os.Stderr, "history compactor:", err)
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()

	if *tlsCert != "" {
		err = srv.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		err = srv.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	// Flush after HTTP shutdown so no accepted request can mutate state after
	// the final durable write.
	if flushErr := st.Flush(); flushErr != nil && err == nil {
		err = fmt.Errorf("final state flush: %w", flushErr)
	}
	return err
}
func cmdJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	name := fs.String("name", "", "node name")
	interval := fs.Duration("interval", 5*time.Second, "heartbeat interval")
	args = reorderKnownFlags(args, map[string]bool{"--name": true, "--interval": true})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("usage: nodescope join <server> <join-token> [--name NAME] [--interval 5s]")
	}
	if *name == "" {
		h, _ := os.Hostname()
		*name = h
	}
	c, err := agent.Join(fs.Arg(0), fs.Arg(1), *name)
	if err != nil {
		return err
	}
	p, err := agent.DefaultConfigPath()
	if err != nil {
		return err
	}
	if err := agent.SaveConfig(p, c); err != nil {
		return err
	}
	fmt.Printf("Joined as %s (%s)\n", c.Name, c.NodeID)
	fmt.Println("Agent config:", p)
	return runAgentLoop(c, *interval)
}

func cmdAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	interval := fs.Duration("interval", 5*time.Second, "heartbeat interval")
	config := fs.String("config", "", "agent config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := resolveAgentConfig(*config)
	if err != nil {
		return err
	}
	c, err := agent.LoadConfig(p)
	if err != nil {
		return fmt.Errorf("load %s: %w", p, err)
	}
	return runAgentLoop(c, *interval)
}

func runAgentLoop(c agent.Config, interval time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Printf("Sending heartbeat to %s every %s. Ctrl+C to stop.\n", c.Server, interval)
	lastFailed := false
	err := agent.Run(ctx, c, interval, func(_ protocol.Metrics, _ []protocol.ServiceStatus, e error) {
		if e != nil {
			fmt.Printf("[%s] heartbeat failed: %v (retrying)\n", time.Now().Format("15:04:05"), e)
			lastFailed = true
		} else if lastFailed {
			fmt.Printf("[%s] connection restored\n", time.Now().Format("15:04:05"))
			lastFailed = false
		}
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

type stringListFlag []string

func (v *stringListFlag) String() string {
	return strings.Join(*v, ",")
}

func (v *stringListFlag) Set(raw string) error {
	*v = append(*v, raw)
	return nil
}

func parseCLIAttrs(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 8 {
		return nil, fmt.Errorf("at most 8 --attr filters are allowed")
	}
	out := make(map[string]string, len(values))
	for _, raw := range values {
		k, val, ok := strings.Cut(raw, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || len(k) > 64 || len(val) > 128 {
			return nil, fmt.Errorf("--attr must use key=value with bounded lengths")
		}
		out[k] = val
	}
	return out, nil
}

func cmdMetrics(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nodescope metrics <history|trend|rollup|stats|system>")
	}
	switch args[0] {
	case "history":
		return cmdMetricHistory(args[1:], false)
	case "trend":
		return cmdMetricHistory(args[1:], true)
	case "rollup":
		return cmdMetricRollup(args[1:])
	case "stats":
		return cmdMetricStats(args[1:])
	case "system":
		return cmdMetricSystem(args[1:])
	default:
		return fmt.Errorf("unknown metrics command %q", args[0])
	}
}

func cmdMetricHistory(args []string, trend bool) error {
	name := "metrics history"
	if trend {
		name = "metrics trend"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	since := fs.Duration("since", time.Hour, "lookback duration")
	limit := fs.Int("limit", 500, "maximum newest raw points")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	var attrFlags stringListFlag
	fs.Var(&attrFlags, "attr", "exact series attribute key=value (repeatable)")
	serverURL := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	token := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read/admin API token")
	args = reorderKnownFlags(args, map[string]bool{"--since": true, "--limit": true, "--attr": true, "--json": false, "--server": true, "--token": true})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: nodescope metrics %s <node> <metric> [--since 1h] [--limit 500] [--json]", map[bool]string{false: "history", true: "trend"}[trend])
	}
	if *since <= 0 {
		return fmt.Errorf("--since must be greater than zero")
	}
	if *limit < 1 || *limit > 5000 {
		return fmt.Errorf("--limit must be between 1 and 5000")
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("read token required: set NODESCOPE_API_TOKEN or pass --token")
	}
	api, err := apiclient.New(*serverURL, *token)
	if err != nil {
		return err
	}
	attrs, err := parseCLIAttrs(attrFlags)
	if err != nil {
		return err
	}

	if trend {
		bucket := cliTrendBucket(*since)
		buckets, err := api.MetricRollupFiltered(context.Background(), fs.Arg(0), fs.Arg(1), attrs, time.Now().UTC().Add(-*since), time.Time{}, bucket)
		if err != nil {
			return err
		}
		out := summarizeRollupHistory(buckets, fs.Arg(0), fs.Arg(1), bucket)
		if *jsonOut {
			return writePrettyJSON(out)
		}
		if required, _ := out["requires_attribute_filter"].(bool); required {
			return fmt.Errorf("metric resolves to multiple series; repeat --attr key=value to select one series")
		}
		fmt.Printf("%s / %s  bucket=%s\n", fs.Arg(0), fs.Arg(1), bucket)
		fmt.Printf("samples: %v\n", out["count"])
		if len(buckets) == 0 {
			return nil
		}
		fmt.Printf("first:   %g %s\n", out["first_value"], out["unit"])
		fmt.Printf("last:    %g %s\n", out["last_value"], out["unit"])
		fmt.Printf("min:     %g %s\n", out["min"], out["unit"])
		fmt.Printf("max:     %g %s\n", out["max"], out["unit"])
		fmt.Printf("average: %g %s\n", out["average"], out["unit"])
		fmt.Printf("delta:   %g %s\n", out["delta"], out["unit"])
		if rate, ok := out["rate_per_hour"]; ok {
			fmt.Printf("rate/h:  %g %s/h\n", rate, out["unit"])
		}
		return nil
	}

	points, err := api.MetricHistoryFiltered(context.Background(), fs.Arg(0), fs.Arg(1), attrs, time.Now().UTC().Add(-*since), time.Time{}, *limit)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writePrettyJSON(points)
	}
	if len(points) == 0 {
		fmt.Println("no metric samples")
		return nil
	}
	for _, p := range points {
		fmt.Printf("%s  %-32s  %g %s\n", p.Sample.Timestamp.Local().Format("2006-01-02 15:04:05"), p.Sample.Name, p.Sample.Value, p.Sample.Unit)
	}
	return nil
}

func cliTrendBucket(window time.Duration) time.Duration {
	switch {
	case window <= 6*time.Hour:
		return time.Minute
	case window <= 7*24*time.Hour:
		return 5 * time.Minute
	default:
		return time.Hour
	}
}

func summarizeRollupHistory(buckets []history.RollupBucket, node, metric string, bucket time.Duration) map[string]any {
	out := map[string]any{"node": node, "metric": metric, "bucket": bucket.String(), "bucket_count": len(buckets)}
	if len(buckets) == 0 {
		out["count"] = 0
		return out
	}
	series := map[string]map[string]string{}
	for _, b := range buckets {
		keyBytes, _ := json.Marshal(b.Attributes)
		key := string(keyBytes)
		if _, ok := series[key]; !ok {
			attrs := make(map[string]string, len(b.Attributes))
			for k, v := range b.Attributes {
				attrs[k] = v
			}
			series[key] = attrs
		}
	}
	if len(series) > 1 {
		values := make([]map[string]string, 0, len(series))
		for _, attrs := range series {
			values = append(values, attrs)
		}
		out["count"] = 0
		out["series_count"] = len(series)
		out["series"] = values
		out["requires_attribute_filter"] = true
		return out
	}
	first := buckets[0]
	last := buckets[len(buckets)-1]
	minValue, maxValue := first.Min, first.Max
	totalCount := 0
	weightedSum := 0.0
	for _, b := range buckets {
		totalCount += b.Count
		weightedSum += b.Average * float64(b.Count)
		if b.Min < minValue {
			minValue = b.Min
		}
		if b.Max > maxValue {
			maxValue = b.Max
		}
	}
	out["count"] = totalCount
	out["kind"] = first.Kind
	out["unit"] = first.Unit
	out["first_value"] = first.First
	out["first_timestamp"] = first.Start
	out["last_value"] = last.Last
	out["last_timestamp"] = last.End
	out["min"] = minValue
	out["max"] = maxValue
	if totalCount > 0 {
		out["average"] = weightedSum / float64(totalCount)
	}
	out["delta"] = last.Last - first.First
	duration := last.End.Sub(first.Start)
	out["duration_seconds"] = duration.Seconds()
	if duration > 0 {
		out["rate_per_hour"] = (last.Last - first.First) / duration.Hours()
	}
	return out
}

func cmdMetricRollup(args []string) error {
	fs := flag.NewFlagSet("metrics rollup", flag.ContinueOnError)
	since := fs.Duration("since", 24*time.Hour, "lookback duration")
	bucket := fs.Duration("bucket", time.Minute, "rollup bucket")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	var attrFlags stringListFlag
	fs.Var(&attrFlags, "attr", "exact series attribute key=value (repeatable)")
	serverURL := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	token := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read/admin API token")
	args = reorderKnownFlags(args, map[string]bool{"--since": true, "--bucket": true, "--attr": true, "--json": false, "--server": true, "--token": true})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: nodescope metrics rollup <node> <metric> [--since 24h] [--bucket 1m] [--json]")
	}
	if *since <= 0 {
		return fmt.Errorf("--since must be greater than zero")
	}
	if *bucket < time.Minute || *bucket > 24*time.Hour {
		return fmt.Errorf("--bucket must be between 1m and 24h")
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("read token required: set NODESCOPE_API_TOKEN or pass --token")
	}
	api, err := apiclient.New(*serverURL, *token)
	if err != nil {
		return err
	}
	attrs, err := parseCLIAttrs(attrFlags)
	if err != nil {
		return err
	}
	out, err := api.MetricRollupFiltered(context.Background(), fs.Arg(0), fs.Arg(1), attrs, time.Now().UTC().Add(-*since), time.Time{}, *bucket)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writePrettyJSON(out)
	}
	if len(out) == 0 {
		fmt.Println("no rollup buckets")
		return nil
	}
	for _, b := range out {
		fmt.Printf("%s  count=%d min=%g max=%g avg=%g last=%g %s%s\n",
			b.Start.Local().Format("2006-01-02 15:04:05"), b.Count, b.Min, b.Max, b.Average, b.Last, b.Unit, formatAttrs(b.Attributes))
	}
	return nil
}

func cmdMetricSystem(args []string) error {
	fs := flag.NewFlagSet("metrics system", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	serverURL := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	token := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read/admin API token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: nodescope metrics system [--json]")
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("read token required: set NODESCOPE_API_TOKEN or pass --token")
	}
	api, err := apiclient.New(*serverURL, *token)
	if err != nil {
		return err
	}
	health, err := api.SystemHealth(context.Background())
	if err != nil {
		return err
	}
	if *jsonOut {
		return writePrettyJSON(health)
	}
	fmt.Printf("uptime:           %s\n", fmtDuration(health.UptimeSeconds))
	fmt.Printf("requests:         %d  4xx=%d  5xx=%d\n", health.RequestsTotal, health.ClientErrorsTotal, health.ServerErrorsTotal)
	fmt.Printf("request latency:  avg=%.2fms max=%.2fms\n", health.RequestAverageMS, health.RequestMaxMS)
	fmt.Printf("heartbeats:       %d\n", health.HeartbeatsTotal)
	fmt.Printf("telemetry:        %d batches / %d samples\n", health.TelemetryBatchesTotal, health.TelemetrySamplesTotal)
	fmt.Printf("nodes:            %d total / %d online\n", health.NodesTotal, health.NodesOnline)
	fmt.Printf("history:          %s / %s\n", bytesText(uint64(health.HistoryBytes)), bytesText(uint64(health.HistoryMaxBytes)))
	fmt.Printf("history files:    %d raw segments / %d rollups (%s)\n", health.HistoryRawSegments, health.HistoryRollupFiles, bytesText(uint64(health.HistoryRollupBytes)))
	return nil
}

func cmdMetricStats(args []string) error {
	fs := flag.NewFlagSet("metrics stats", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	serverURL := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	token := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read/admin API token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: nodescope metrics stats [--json]")
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("read token required: set NODESCOPE_API_TOKEN or pass --token")
	}
	api, err := apiclient.New(*serverURL, *token)
	if err != nil {
		return err
	}
	stats, err := api.MetricHistoryStats(context.Background())
	if err != nil {
		return err
	}
	if *jsonOut {
		return writePrettyJSON(stats)
	}
	fmt.Printf("history: %s / %s max\n", bytesText(uint64(stats.Bytes)), bytesText(uint64(stats.MaxBytes)))
	fmt.Printf("head:    %s\n", bytesText(uint64(stats.HeadBytes)))
	fmt.Printf("segments: %d\n", stats.Segments)
	fmt.Printf("nodes:    %d\n", len(stats.NodeAcks))
	return nil
}

func summarizeHistory(points []history.Point, node, metric string) map[string]any {
	out := map[string]any{"node": node, "metric": metric, "count": len(points)}
	if len(points) == 0 {
		return out
	}
	first := points[0].Sample
	last := points[len(points)-1].Sample
	minValue, maxValue := first.Value, first.Value
	sum := 0.0
	for _, p := range points {
		if p.Sample.Value < minValue {
			minValue = p.Sample.Value
		}
		if p.Sample.Value > maxValue {
			maxValue = p.Sample.Value
		}
		sum += p.Sample.Value
	}
	out["kind"] = first.Kind
	out["unit"] = first.Unit
	out["first_value"] = first.Value
	out["first_timestamp"] = first.Timestamp
	out["last_value"] = last.Value
	out["last_timestamp"] = last.Timestamp
	out["min"] = minValue
	out["max"] = maxValue
	out["average"] = sum / float64(len(points))
	out["delta"] = last.Value - first.Value
	duration := last.Timestamp.Sub(first.Timestamp)
	out["duration_seconds"] = duration.Seconds()
	if duration > 0 {
		out["rate_per_hour"] = (last.Value - first.Value) / duration.Hours()
	}
	return out
}

func formatAttrs(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+attrs[k])
	}
	return " [" + strings.Join(parts, ",") + "]"
}

func writePrettyJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "central server URL")
	token := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", ""), "API bearer token")
	allowWrite := fs.Bool("allow-write", false, "expose rename/remove MCP tools (requires admin token)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *allowWrite {
		if strings.TrimSpace(*token) == "" {
			*token = envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")
		}
		if strings.TrimSpace(*token) == "" {
			*token = localToken("admin_token")
		}
	} else {
		if strings.TrimSpace(*token) == "" {
			*token = envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")
		}
		if strings.TrimSpace(*token) == "" {
			*token = localToken("read_token")
		}
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("MCP token required: set NODESCOPE_API_TOKEN (read-only) or NODESCOPE_ADMIN_TOKEN (legacy JJP_* names are also accepted)")
	}
	api, err := apiclient.New(*srv, *token)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// MCP stdio reserves stdout for JSON-RPC. Human diagnostics belong on stderr.
	fmt.Fprintf(os.Stderr, "NodeScope MCP %s -> %s (write=%t)\n", protocol.Version, *srv, *allowWrite)
	err = (&mcpserver.Server{API: api, AllowWrite: *allowWrite}).Run(ctx, os.Stdin, os.Stdout)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func cmdToken(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: nodescope token <show|rotate>")
	}
	switch args[0] {
	case "show":
		return cmdTokenShow(args[1:])
	case "rotate":
		return cmdTokenRotate(args[1:])
	default:
		return fmt.Errorf("unknown token command %q", args[0])
	}
}

func cmdTokenShow(args []string) error {
	fs := flag.NewFlagSet("token show", flag.ContinueOnError)
	kind := fs.String("kind", "all", "token kind: all, join, read, or admin")
	data := fs.String("data", "", "state file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*kind = strings.ToLower(strings.TrimSpace(*kind))
	if *kind != "all" && *kind != "join" && *kind != "read" && *kind != "admin" {
		return fmt.Errorf("kind must be all, join, read, or admin")
	}
	if *data == "" {
		dir, err := dataDir()
		if err != nil {
			return err
		}
		*data = filepath.Join(dir, "server.json")
	}
	b, err := os.ReadFile(*data)
	if err != nil {
		return err
	}
	var raw struct {
		SchemaVersion  int    `json:"schema_version"`
		BootstrapToken string `json:"bootstrap_token"`
		ReadToken      string `json:"read_token"`
		AdminToken     string `json:"admin_token"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	if raw.SchemaVersion > store.CurrentSchemaVersion {
		return fmt.Errorf("state schema %d is newer than this nodescope supports", raw.SchemaVersion)
	}
	printOne := func(label, token string) {
		fmt.Printf("%-5s %s\n", label+":", token)
	}
	switch *kind {
	case "join":
		printOne("join", raw.BootstrapToken)
	case "read":
		printOne("read", raw.ReadToken)
	case "admin":
		printOne("admin", raw.AdminToken)
	default:
		printOne("join", raw.BootstrapToken)
		printOne("read", raw.ReadToken)
		printOne("admin", raw.AdminToken)
	}
	return nil
}

func cmdTokenRotate(args []string) error {
	fs, srv, tok, err := adminFlags("token rotate", args)
	if err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope token rotate <join|read|admin>")
	}
	kind := strings.ToLower(strings.TrimSpace(fs.Arg(0)))
	if kind != "join" && kind != "read" && kind != "admin" {
		return fmt.Errorf("token kind must be join, read, or admin")
	}
	if *tok == "" {
		*tok = localAdminToken()
	}
	if strings.TrimSpace(*tok) == "" {
		return fmt.Errorf("admin token required")
	}
	api, err := apiclient.New(*srv, *tok)
	if err != nil {
		return err
	}
	out, err := api.RotateToken(context.Background(), kind)
	if err != nil {
		return err
	}
	fmt.Printf("Rotated %s token.\n", kind)
	fmt.Println(out.Token)
	if kind == "admin" {
		fmt.Println("The previous admin token is now invalid.")
	}
	return nil
}

func cmdState(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: nodescope state <check|backup|restore>")
	}
	switch args[0] {
	case "check":
		return cmdStateCheck(args[1:])
	case "backup":
		return cmdStateBackup(args[1:])
	case "restore":
		return cmdStateRestore(args[1:])
	default:
		return fmt.Errorf("unknown state command %q", args[0])
	}
}

func defaultStatePath(v string) (string, error) {
	path := strings.TrimSpace(v)
	if path == "" {
		dir, err := dataDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(dir, "server.json")
	}
	return filepath.Abs(path)
}

func cmdStateCheck(args []string) error {
	fs := flag.NewFlagSet("state check", flag.ContinueOnError)
	data := fs.String("data", "", "state file")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := defaultStatePath(*data)
	if err != nil {
		return err
	}
	info, err := store.InspectState(path)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(info)
	}
	fmt.Printf("State OK: %s\n", path)
	fmt.Printf("Schema: %d", info.SchemaVersion)
	if info.Legacy {
		fmt.Print(" (legacy; will migrate on next host start)")
	}
	fmt.Println()
	fmt.Printf("Nodes: %d  Alerts: %d  Events: %d  Incidents: %d\n", info.Nodes, info.Alerts, info.Events, info.Incidents)
	fmt.Printf("Policy: CPU %.1f%% / RAM %.1f%% / Disk %.1f%% for %s; unstable %s; offline %s\n",
		info.Policy.CPUThreshold, info.Policy.RAMThreshold, info.Policy.DiskThreshold,
		info.Policy.MetricFor, info.Policy.UnstableAfter, info.Policy.OfflineAfter)
	return nil
}

func cmdStateBackup(args []string) error {
	fs := flag.NewFlagSet("state backup", flag.ContinueOnError)
	data := fs.String("data", "", "state file")
	out := fs.String("out", "", "backup destination")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := defaultStatePath(*data)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*out) == "" {
		*out = fmt.Sprintf("%s.backup-%s.bak", path, time.Now().UTC().Format("20060102T150405Z"))
	}
	info, err := store.BackupState(path, *out)
	if err != nil {
		return err
	}
	fmt.Printf("Backup OK: %s\n", *out)
	fmt.Printf("Schema %d, %d nodes, %d incidents\n", info.SchemaVersion, info.Nodes, info.Incidents)
	return nil
}

func cmdStateRestore(args []string) error {
	fs := flag.NewFlagSet("state restore", flag.ContinueOnError)
	data := fs.String("data", "", "state file")
	force := fs.Bool("force", false, "confirm destructive restore")
	args = reorderKnownFlags(args, map[string]bool{"--data": true, "--force": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope state restore <backup> [--data PATH] --force")
	}
	if !*force {
		return fmt.Errorf("restore requires --force; the current state will be replaced after an automatic pre-restore backup")
	}
	path, err := defaultStatePath(*data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lk, err := filelock.Acquire(path + ".lock")
	if err != nil {
		return fmt.Errorf("cannot restore while nodescope host is using this state: %w", err)
	}
	defer lk.Release()
	info, pre, err := store.RestoreState(path, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("Restore OK: %s\n", path)
	if pre != "" {
		fmt.Printf("Previous state preserved: %s\n", pre)
	}
	fmt.Printf("Restored schema %d with %d nodes and %d incidents\n", info.SchemaVersion, info.Nodes, info.Incidents)
	return nil
}

func cmdInstallAgent(args []string) error {
	fs := flag.NewFlagSet("install-agent", flag.ContinueOnError)
	config := fs.String("config", "", "agent config path")
	interval := fs.Duration("interval", 5*time.Second, "heartbeat interval")
	system := fs.Bool("system", false, "install system-wide systemd unit (requires root)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := resolveAgentConfig(*config)
	if err != nil {
		return err
	}
	unit, err := autostart.Install(p, *interval, *system)
	if err != nil {
		return err
	}
	fmt.Println("Agent service installed:", unit)
	if !*system {
		fmt.Println("Tip: run 'loginctl enable-linger $USER' if you need the user service to start at boot without logging in.")
	}
	return nil
}

func cmdService(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: nodescope service <add|ls|rm>")
	}
	switch args[0] {
	case "add":
		return cmdServiceAdd(args[1:])
	case "ls", "list":
		return cmdServiceList(args[1:])
	case "rm", "remove":
		return cmdServiceRemove(args[1:])
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func cmdServiceAdd(args []string) error {
	fs := flag.NewFlagSet("service add", flag.ContinueOnError)
	tcpTarget := fs.String("tcp", "", "TCP target host:port")
	httpTarget := fs.String("http", "", "HTTP health URL")
	systemdTarget := fs.String("systemd", "", "systemd unit")
	config := fs.String("config", "", "agent config path")
	args = reorderKnownFlags(args, map[string]bool{"--tcp": true, "--http": true, "--systemd": true, "--config": true})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope service add <name> (--tcp HOST:PORT | --http URL | --systemd UNIT)")
	}
	typeName, target, count := "", "", 0
	for kind, value := range map[string]string{"tcp": *tcpTarget, "http": *httpTarget, "systemd": *systemdTarget} {
		if strings.TrimSpace(value) != "" {
			typeName, target = kind, strings.TrimSpace(value)
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("choose exactly one check type: --tcp, --http, or --systemd")
	}
	p, err := resolveAgentConfig(*config)
	if err != nil {
		return err
	}
	c, err := agent.LoadConfig(p)
	if err != nil {
		return err
	}
	spec := agent.ServiceSpec{Name: fs.Arg(0), Type: typeName, Target: target}
	if err := agent.ValidateService(spec); err != nil {
		return err
	}
	for _, s := range c.Services {
		if strings.EqualFold(s.Name, spec.Name) {
			return fmt.Errorf("service %q already exists", spec.Name)
		}
	}
	c.Services = append(c.Services, spec)
	if err := agent.SaveConfig(p, c); err != nil {
		return err
	}
	fmt.Printf("Added service %s (%s: %s)\n", spec.Name, spec.Type, spec.Target)
	fmt.Println("Restart the agent for the new check to take effect.")
	return nil
}

func cmdServiceList(args []string) error {
	fs := flag.NewFlagSet("service ls", flag.ContinueOnError)
	config := fs.String("config", "", "agent config path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := resolveAgentConfig(*config)
	if err != nil {
		return err
	}
	c, err := agent.LoadConfig(p)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tTARGET")
	for _, s := range c.Services {
		fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, s.Type, s.Target)
	}
	return w.Flush()
}

func cmdServiceRemove(args []string) error {
	fs := flag.NewFlagSet("service rm", flag.ContinueOnError)
	config := fs.String("config", "", "agent config path")
	args = reorderKnownFlags(args, map[string]bool{"--config": true})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope service rm <name>")
	}
	p, err := resolveAgentConfig(*config)
	if err != nil {
		return err
	}
	c, err := agent.LoadConfig(p)
	if err != nil {
		return err
	}
	name := fs.Arg(0)
	out := c.Services[:0]
	found := false
	for _, s := range c.Services {
		if strings.EqualFold(s.Name, name) {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return fmt.Errorf("service %q not found", name)
	}
	c.Services = out
	if err := agent.SaveConfig(p, c); err != nil {
		return err
	}
	fmt.Println("Removed service:", name)
	return nil
}

func resolveAgentConfig(v string) (string, error) {
	if strings.TrimSpace(v) != "" {
		return v, nil
	}
	return agent.DefaultConfigPath()
}

func readFlags(name string, args []string) (*flag.FlagSet, *string, *string, *bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	defaultToken := envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", "")
	if defaultToken == "" {
		defaultToken = envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")
	}
	tok := fs.String("token", defaultToken, "read or admin API token")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--json": false})
	err := fs.Parse(args)
	return fs, srv, tok, jsonOut, err
}

func adminFlags(name string, args []string) (*flag.FlagSet, *string, *string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("admin-token", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", ""), "admin token")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--admin-token": true})
	err := fs.Parse(args)
	return fs, srv, tok, err
}

func reorderKnownFlags(args []string, known map[string]bool) []string {
	flags := make([]string, 0, len(args))
	pos := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := a
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			name = a[:eq]
		}
		if takesValue, ok := known[name]; ok {
			flags = append(flags, a)
			if takesValue && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	return append(flags, pos...)
}

func resolveReadToken(v string) string {
	if strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	if t := localToken("read_token"); t != "" {
		return t
	}
	return localAdminToken()
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func parseSinceFlag(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("since duration must be greater than zero")
		}
		return time.Now().UTC().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("since must be a duration such as 24h or an RFC3339 timestamp")
}

func cmdHealth(args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read or admin API token")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	*tok = resolveReadToken(*tok)
	api, err := apiclient.New(*srv, *tok)
	if err != nil {
		return err
	}
	o, err := api.Overview(context.Background(), 0)
	if err != nil {
		return err
	}
	if *jsonOut {
		if err := printJSON(map[string]any{
			"status": o.Status, "attention_required": o.AttentionRequired, "headline": o.Headline, "generated_at": o.GeneratedAt,
		}); err != nil {
			return err
		}
	} else {
		fmt.Printf("%s — %s\n", strings.ToUpper(o.Status), o.Headline)
	}
	if o.AttentionRequired {
		return exitCodeError{code: 2}
	}
	return nil
}

func cmdOverview(args []string) error {
	fs := flag.NewFlagSet("overview", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read or admin API token")
	events := fs.Int("events", 10, "recent events to include (0-50)")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--events": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *events < 0 || *events > 50 {
		return fmt.Errorf("events must be between 0 and 50")
	}
	*tok = resolveReadToken(*tok)
	api, err := apiclient.New(*srv, *tok)
	if err != nil {
		return err
	}
	o, err := api.Overview(context.Background(), *events)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(o)
	}
	fmt.Printf("NodeScope overview  [%s]\n", strings.ToUpper(o.Status))
	fmt.Println(o.Headline)
	if !o.AttentionRequired {
		fmt.Println("No active problems need attention.")
		return nil
	}
	if len(o.ActiveIncidents) > 0 {
		fmt.Println("\nOpen incidents")
		for _, inc := range o.ActiveIncidents {
			fmt.Printf("  %-8s %-16s %-20s %s (%s)\n", strings.ToUpper(inc.Severity), inc.NodeName, inc.Title, inc.ID, ageText(inc.StartedAt))
		}
	}
	if len(o.ActiveAlerts) > 0 {
		fmt.Println("\nAttention")
		for _, a := range o.ActiveAlerts {
			fmt.Printf("  %-8s %-16s %-16s %s (%s)\n", strings.ToUpper(a.Severity), a.NodeName, a.Subject, a.Message, ageText(a.Since))
		}
	}
	if len(o.UnhealthyNodes) > 0 {
		fmt.Println("\nUnhealthy nodes")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATUS\tCPU\tRAM\tDISK\tSERVICES")
		for _, n := range o.UnhealthyNodes {
			svcs := "-"
			if n.TotalServices > 0 {
				svcs = fmt.Sprintf("%d/%d", n.HealthyServices, n.TotalServices)
			}
			fmt.Fprintf(w, "%s\t%s\t%.1f%%\t%.1f%%\t%.1f%%\t%s\n", n.Name, n.Status, n.CPUPercent, n.RAMPercent, n.DiskPercent, svcs)
		}
		_ = w.Flush()
	}
	return nil
}

func cmdDiagnose(args []string) error {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read or admin API token")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope diagnose <node> [--json]")
	}
	*tok = resolveReadToken(*tok)
	api, err := apiclient.New(*srv, *tok)
	if err != nil {
		return err
	}
	d, err := api.Diagnosis(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(d)
	}
	fmt.Printf("%s  [%s]\n", d.Node.Name, strings.ToUpper(d.Overall))
	for _, f := range d.Findings {
		fmt.Printf("- %-8s %s", strings.ToUpper(f.Severity), f.Summary)
		if f.Evidence != "" {
			fmt.Printf(" — %s", f.Evidence)
		}
		fmt.Println()
	}
	if len(d.SuggestedActions) > 0 {
		fmt.Println("\nSuggested checks")
		for i, a := range d.SuggestedActions {
			fmt.Printf("%d. %s\n", i+1, a)
		}
	}
	return nil
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read or admin API token")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	*tok = resolveReadToken(*tok)
	report := protocol.DoctorReport{ServerURL: *srv, Overall: "ok"}
	add := func(name, status, msg string) {
		report.Checks = append(report.Checks, protocol.DoctorCheck{Name: name, Status: status, Message: msg})
		if status == "fail" {
			report.Overall = "fail"
		} else if status == "warn" && report.Overall == "ok" {
			report.Overall = "warning"
		}
	}
	api, err := apiclient.New(*srv, *tok)
	if err != nil {
		add("server_url", "fail", err.Error())
	} else {
		start := time.Now()
		info, infoErr := api.Info(context.Background())
		if infoErr != nil {
			add("server", "fail", infoErr.Error())
		} else {
			add("server", "pass", fmt.Sprintf("reachable in %s", time.Since(start).Round(time.Millisecond)))
			if info.APIVersion != protocol.APIVersion {
				add("api_version", "fail", fmt.Sprintf("server API %s, client expects %s", info.APIVersion, protocol.APIVersion))
			} else if info.Version != protocol.Version {
				add("version", "warn", fmt.Sprintf("server %s, client %s", info.Version, protocol.Version))
			} else {
				add("version", "pass", fmt.Sprintf("server/client %s, API %s", info.Version, info.APIVersion))
			}
			if info.StateSchema == 0 {
				add("state_schema", "warn", "server did not report a state schema (pre-v0.7 server)")
			} else if info.StateSchema > store.CurrentSchemaVersion {
				add("state_schema", "fail", fmt.Sprintf("server state schema %d is newer than client support %d", info.StateSchema, store.CurrentSchemaVersion))
			} else if info.StateSchema < store.CurrentSchemaVersion {
				add("state_schema", "warn", fmt.Sprintf("server state schema %d, client supports %d", info.StateSchema, store.CurrentSchemaVersion))
			} else {
				add("state_schema", "pass", fmt.Sprintf("schema %d", info.StateSchema))
			}
			if *tok == "" {
				add("authentication", "fail", "no read/admin token found; set NODESCOPE_API_TOKEN or pass --token")
			} else if _, e := api.Summary(context.Background()); e != nil {
				add("authentication", "fail", e.Error())
			} else {
				add("authentication", "pass", "read access works")
			}
		}
	}
	if p, e := agent.DefaultConfigPath(); e == nil {
		if c, e := agent.LoadConfig(p); e == nil {
			configured := strings.TrimRight(strings.TrimSpace(c.Server), "/")
			target := strings.TrimRight(strings.TrimSpace(*srv), "/")
			if configured != target {
				add("agent_config", "warn", fmt.Sprintf("local agent points to %s, doctor checked %s", c.Server, *srv))
			} else {
				add("agent_config", "pass", fmt.Sprintf("%s -> %s", p, c.Server))
			}
		} else if errors.Is(e, os.ErrNotExist) {
			add("agent_config", "info", "no local agent config; okay on a controller-only machine")
		} else {
			add("agent_config", "warn", fmt.Sprintf("%s: %v", p, e))
		}
	}
	if *jsonOut {
		return printJSON(report)
	}
	fmt.Printf("NodeScope doctor  [%s]\n", strings.ToUpper(report.Overall))
	for _, c := range report.Checks {
		mark := "·"
		switch c.Status {
		case "pass":
			mark = "OK"
		case "warn":
			mark = "!!"
		case "fail":
			mark = "XX"
		}
		fmt.Printf("%-2s %-16s %s\n", mark, c.Name, c.Message)
	}
	return nil
}

func cmdList(args []string) error {
	_, srv, tok, jsonOut, err := readFlags("ls", args)
	if err != nil {
		return err
	}
	if *tok == "" {
		*tok = localToken("read_token")
		if *tok == "" {
			*tok = localAdminToken()
		}
	}
	var nodes []protocol.NodeView
	if err := getJSON(*srv+"/api/v1/nodes", *tok, &nodes); err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(nodes)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tCPU\tRAM\tDISK\tSERVICES\tUPTIME")
	for _, n := range nodes {
		healthy, total := serviceCounts(n.Services)
		svcs := "-"
		if total > 0 {
			svcs = fmt.Sprintf("%d/%d", healthy, total)
		}
		fmt.Fprintf(w, "%s\t%s\t%.1f%%\t%.1f%%\t%.1f%%\t%s\t%s\n", n.Name, n.Status, n.Metrics.CPUPercent, n.Metrics.RAMPercent, n.Metrics.DiskPercent, svcs, fmtDuration(n.Metrics.UptimeSeconds))
	}
	return w.Flush()
}

func cmdAlerts(args []string) error {
	fs := flag.NewFlagSet("alerts", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read or admin API token")
	node := fs.String("node", "", "filter by node name or ID")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--node": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tok == "" {
		*tok = localToken("read_token")
		if *tok == "" {
			*tok = localAdminToken()
		}
	}
	endpoint := strings.TrimRight(*srv, "/") + "/api/v1/alerts"
	if strings.TrimSpace(*node) != "" {
		endpoint += "?node=" + url.QueryEscape(*node)
	}
	var alerts []protocol.Alert
	if err := getJSON(endpoint, *tok, &alerts); err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(alerts)
	}
	if len(alerts) == 0 {
		fmt.Println("No active alerts.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SEVERITY\tNODE\tKIND\tSUBJECT\tSINCE\tMESSAGE")
	for _, a := range alerts {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", strings.ToUpper(a.Severity), a.NodeName, a.Kind, a.Subject, ageText(a.Since), a.Message)
	}
	return w.Flush()
}

func cmdEvents(args []string) error {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read or admin API token")
	node := fs.String("node", "", "filter by node name or ID")
	sinceRaw := fs.String("since", "", "only include events at or after this duration ago (e.g. 24h) or RFC3339 timestamp")
	limit := fs.Int("limit", 50, "number of newest events (1-500)")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--node": true, "--since": true, "--limit": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *limit < 1 || *limit > 500 {
		return fmt.Errorf("limit must be between 1 and 500")
	}
	if *tok == "" {
		*tok = localToken("read_token")
		if *tok == "" {
			*tok = localAdminToken()
		}
	}
	since, err := parseSinceFlag(*sinceRaw)
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", *limit))
	if strings.TrimSpace(*node) != "" {
		q.Set("node", *node)
	}
	if !since.IsZero() {
		q.Set("since", since.Format(time.RFC3339))
	}
	endpoint := strings.TrimRight(*srv, "/") + "/api/v1/events?" + q.Encode()
	var events []protocol.Event
	if err := getJSON(endpoint, *tok, &events); err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(events)
	}
	if len(events) == 0 {
		fmt.Println("No events recorded.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TIME\tSEVERITY\tNODE\tKIND\tSUBJECT\tMESSAGE")
	for _, e := range events {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", e.OccurredAt.Local().Format("2006-01-02 15:04:05"), strings.ToUpper(e.Severity), e.NodeName, e.Kind, e.Subject, e.Message)
	}
	return w.Flush()
}

func cmdIncidents(args []string) error {
	fs := flag.NewFlagSet("incidents", flag.ContinueOnError)
	srv := fs.String("server", envCompat("NODESCOPE_SERVER", "JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envCompat("NODESCOPE_API_TOKEN", "JJP_API_TOKEN", envCompat("NODESCOPE_ADMIN_TOKEN", "JJP_ADMIN_TOKEN", "")), "read or admin API token")
	node := fs.String("node", "", "filter by node name or ID")
	status := fs.String("status", "", "filter by incident status: open or resolved")
	sinceRaw := fs.String("since", "", "only include incidents active at or after this duration ago (e.g. 24h) or RFC3339 timestamp")
	limit := fs.Int("limit", 50, "number of newest incidents (1-500)")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--node": true, "--status": true, "--since": true, "--limit": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *limit < 1 || *limit > 500 {
		return fmt.Errorf("limit must be between 1 and 500")
	}
	*status = strings.ToLower(strings.TrimSpace(*status))
	if *status != "" && *status != "open" && *status != "resolved" {
		return fmt.Errorf("status must be open or resolved")
	}
	*tok = resolveReadToken(*tok)
	api, err := apiclient.New(*srv, *tok)
	if err != nil {
		return err
	}
	since, err := parseSinceFlag(*sinceRaw)
	if err != nil {
		return err
	}
	incs, err := api.IncidentsSince(context.Background(), *limit, *node, *status, since)
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(incs)
	}
	if len(incs) == 0 {
		fmt.Println("No incidents recorded.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STARTED\tSTATUS\tSEVERITY\tNODE\tTITLE\tDURATION\tEVENTS\tID")
	for _, inc := range incs {
		dur := time.Duration(inc.DurationSec) * time.Second
		if inc.Status == "open" {
			dur = time.Since(inc.StartedAt).Round(time.Second)
			if dur < 0 {
				dur = 0
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n", inc.StartedAt.Local().Format("2006-01-02 15:04:05"), strings.ToUpper(inc.Status), strings.ToUpper(inc.Severity), inc.NodeName, inc.Title, dur, inc.EventCount, inc.ID)
	}
	return w.Flush()
}

func cmdIncident(args []string) error {
	fs, srv, tok, jsonOut, err := readFlags("incident", args)
	if err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope incident <id> [--json]")
	}
	*tok = resolveReadToken(*tok)
	api, err := apiclient.New(*srv, *tok)
	if err != nil {
		return err
	}
	d, err := api.Incident(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(d)
	}
	inc := d.Incident
	fmt.Printf("%s  [%s / %s]\n", inc.Title, strings.ToUpper(inc.Status), strings.ToUpper(inc.Severity))
	fmt.Printf("ID:       %s\nNode:     %s\nStarted:  %s\n", inc.ID, inc.NodeName, inc.StartedAt.Local().Format(time.RFC3339))
	if inc.ResolvedAt != nil {
		fmt.Printf("Resolved: %s\n", inc.ResolvedAt.Local().Format(time.RFC3339))
	}
	dur := time.Duration(inc.DurationSec) * time.Second
	if inc.Status == "open" {
		dur = time.Since(inc.StartedAt).Round(time.Second)
		if dur < 0 {
			dur = 0
		}
	}
	fmt.Printf("Duration: %s\nEvents:   %d\nSummary:  %s\n", dur, inc.EventCount, inc.Summary)
	if len(inc.Subjects) > 0 {
		fmt.Printf("Subjects: %s\n", strings.Join(inc.Subjects, ", "))
	}
	if len(d.Events) > 0 {
		fmt.Println("\nTimeline")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "TIME\tSEVERITY\tKIND\tSUBJECT\tMESSAGE")
		for _, e := range d.Events {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.OccurredAt.Local().Format("15:04:05"), strings.ToUpper(e.Severity), e.Kind, e.Subject, e.Message)
		}
		_ = w.Flush()
	}
	return nil
}

func ageText(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t).Round(time.Second)
	if d < 0 {
		d = 0
	}
	return d.String()
}

func cmdShow(args []string) error {
	fs, srv, tok, jsonOut, err := readFlags("show", args)
	if err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope show <node>")
	}
	if *tok == "" {
		*tok = localToken("read_token")
		if *tok == "" {
			*tok = localAdminToken()
		}
	}
	var n protocol.NodeView
	if err := getJSON(*srv+"/api/v1/nodes/"+url.PathEscape(fs.Arg(0)), *tok, &n); err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(n)
	}
	fmt.Printf("%s  [%s]\n", n.Name, n.Status)
	fmt.Printf("ID:       %s\nOS:       %s/%s\nAgent:    %s\n", n.ID, n.OS, n.Arch, n.AgentVersion)
	fmt.Printf("CPU:      %.1f%%\nRAM:      %.1f%% (%s / %s)\nDisk:     %.1f%% (%s / %s)\nUptime:   %s\n", n.Metrics.CPUPercent, n.Metrics.RAMPercent, bytesText(n.Metrics.RAMUsedBytes), bytesText(n.Metrics.RAMTotalBytes), n.Metrics.DiskPercent, bytesText(n.Metrics.DiskUsedBytes), bytesText(n.Metrics.DiskTotalBytes), fmtDuration(n.Metrics.UptimeSeconds))
	if n.Metrics.TemperatureC != nil {
		fmt.Printf("Temp:     %.1f°C\n", *n.Metrics.TemperatureC)
	}
	if !n.LastHeartbeat.IsZero() {
		fmt.Printf("Last beat: %s (%s ago)\n", n.LastHeartbeat.Local().Format(time.RFC3339), time.Since(n.LastHeartbeat).Round(time.Second))
	}
	if len(n.Services) > 0 {
		fmt.Println("\nServices")
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tTYPE\tTARGET\tLATENCY\tMESSAGE")
		for _, s := range n.Services {
			state := "DOWN"
			if s.Healthy {
				state = "OK"
			}
			lat := "-"
			if s.LatencyMS > 0 {
				lat = fmt.Sprintf("%dms", s.LatencyMS)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, state, s.Type, s.Target, lat, s.Message)
		}
		_ = w.Flush()
	}
	return nil
}

func cmdRename(args []string) error {
	fs, srv, tok, err := adminFlags("rename", args)
	if err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return fmt.Errorf("usage: nodescope rename <node> <new-name>")
	}
	if *tok == "" {
		*tok = localAdminToken()
	}
	body := protocol.RenameNodeRequest{Name: fs.Arg(1)}
	if err := requestJSON(http.MethodPatch, *srv+"/api/v1/nodes/"+url.PathEscape(fs.Arg(0)), *tok, body, http.StatusNoContent, nil); err != nil {
		return err
	}
	fmt.Printf("Renamed %s -> %s\n", fs.Arg(0), fs.Arg(1))
	return nil
}

func cmdRemove(args []string) error {
	fs, srv, tok, err := adminFlags("rm", args)
	if err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: nodescope rm <node>")
	}
	if *tok == "" {
		*tok = localAdminToken()
	}
	if err := requestJSON(http.MethodDelete, *srv+"/api/v1/nodes/"+url.PathEscape(fs.Arg(0)), *tok, nil, http.StatusNoContent, nil); err != nil {
		return err
	}
	fmt.Println("Removed node:", fs.Arg(0))
	return nil
}

func getJSON(endpoint, token string, dst any) error {
	return requestJSON(http.MethodGet, endpoint, token, nil, http.StatusOK, dst)
}

func requestJSON(method, endpoint, token string, body any, expected int, dst any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(endpoint, "/"), r)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c := &http.Client{Timeout: 8 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != expected {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(msg))
		if detail != "" {
			return fmt.Errorf("server returned %s: %s", resp.Status, detail)
		}
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}

func localAdminToken() string { return localToken("admin_token") }

func localToken(key string) string {
	d, e := dataDir()
	if e != nil {
		return ""
	}
	b, e := os.ReadFile(filepath.Join(d, "server.json"))
	if e != nil {
		return ""
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	var out string
	_ = json.Unmarshal(v[key], &out)
	return out
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envCompat(primary, legacy, d string) string {
	if v := os.Getenv(primary); v != "" {
		return v
	}
	if v := os.Getenv(legacy); v != "" {
		return v
	}
	return d
}

func fmtDuration(sec uint64) string {
	d := time.Duration(sec) * time.Second
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd%dh", int(d/(24*time.Hour)), int((d%(24*time.Hour))/time.Hour))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh%dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}

func bytesText(v uint64) string {
	const u = 1024
	if v < u {
		return fmt.Sprintf("%d B", v)
	}
	div, exp := uint64(u), 0
	for n := v / u; n >= u; n /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(v)/float64(div), "KMGTPE"[exp])
}

func serviceCounts(v []protocol.ServiceStatus) (healthy, total int) {
	for _, s := range v {
		total++
		if s.Healthy {
			healthy++
		}
	}
	return healthy, total
}
