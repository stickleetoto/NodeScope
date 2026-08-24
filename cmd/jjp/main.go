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
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jjp-monitor/jjp/internal/agent"
	"github.com/jjp-monitor/jjp/internal/apiclient"
	"github.com/jjp-monitor/jjp/internal/autostart"
	"github.com/jjp-monitor/jjp/internal/filelock"
	"github.com/jjp-monitor/jjp/internal/mcpserver"
	"github.com/jjp-monitor/jjp/internal/protocol"
	"github.com/jjp-monitor/jjp/internal/server"
	"github.com/jjp-monitor/jjp/internal/store"
)

func main() {
	if err := run(); err != nil {
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
	case "token":
		return cmdToken(os.Args[2:])
	case "state":
		return cmdState(os.Args[2:])
	case "overview":
		return cmdOverview(os.Args[2:])
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
		fmt.Println("jjp", protocol.Version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func usage() {
	fmt.Print(`jjp — lightweight distributed node monitoring

Usage:
  jjp host [--listen :7443] [--data PATH] [--tls-cert FILE --tls-key FILE]
           [--cpu-alert 90] [--ram-alert 90] [--disk-alert 90]
           [--metric-for 5m] [--unstable-after 15s] [--offline-after 60s]
  jjp join <server> <join-token> [--name NAME] [--interval 5s]
  jjp agent [--config PATH] [--interval 5s]
  jjp install-agent [--config PATH] [--interval 5s] [--system]
  jjp mcp [--server URL] [--token TOKEN] [--allow-write]
  jjp token show [--kind all|join|read|admin] [--data PATH]
  jjp token rotate <join|read|admin> [--server URL] [--admin-token TOKEN]
  jjp state check [--data PATH] [--json]
  jjp state backup [--data PATH] [--out PATH]
  jjp state restore <backup> [--data PATH] --force

  jjp service add <name> --tcp HOST:PORT
  jjp service add <name> --http URL
  jjp service add <name> --systemd UNIT
  jjp service ls
  jjp service rm <name>

  jjp overview [--events 10] [--json] [--server URL] [--token TOKEN]
  jjp diagnose <node> [--json] [--server URL] [--token TOKEN]
  jjp doctor [--json] [--server URL] [--token TOKEN]
  jjp ls [--json] [--server URL] [--token TOKEN]
  jjp alerts [--node NODE] [--json] [--server URL] [--token TOKEN]
  jjp events [--node NODE] [--limit 50] [--json] [--server URL] [--token TOKEN]
  jjp incidents [--node NODE] [--status open|resolved] [--limit 50] [--json]
  jjp incident <id> [--json] [--server URL] [--token TOKEN]
  jjp show <node> [--json] [--server URL] [--token TOKEN]
  jjp rename <node> <new-name> [--server URL] [--admin-token TOKEN]
  jjp rm <node> [--server URL] [--admin-token TOKEN]
  jjp version

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

	fmt.Println("Jjamppong central server")
	fmt.Println("Listen:     ", *listen)
	fmt.Println("State:      ", *data)
	fmt.Println("Schema:     ", st.SchemaVersion())
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
		fmt.Println("Tokens:      existing (use 'jjp token show' locally when needed)")
	}
	fmt.Printf("Alerts:      CPU %.1f%% / RAM %.1f%% / Disk %.1f%% for %s; unstable %s; offline %s\n",
		policy.CPUThreshold, policy.RAMThreshold, policy.DiskThreshold, policy.MetricFor, policy.UnstableAfter, policy.OfflineAfter)
	fmt.Println()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           (&server.Server{Store: st}).Handler(),
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
		return fmt.Errorf("usage: jjp join <server> <join-token> [--name NAME] [--interval 5s]")
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

func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "central server URL")
	token := fs.String("token", os.Getenv("JJP_API_TOKEN"), "API bearer token")
	allowWrite := fs.Bool("allow-write", false, "expose rename/remove MCP tools (requires admin token)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *allowWrite {
		if strings.TrimSpace(*token) == "" {
			*token = os.Getenv("JJP_ADMIN_TOKEN")
		}
		if strings.TrimSpace(*token) == "" {
			*token = localToken("admin_token")
		}
	} else {
		if strings.TrimSpace(*token) == "" {
			*token = os.Getenv("JJP_ADMIN_TOKEN")
		}
		if strings.TrimSpace(*token) == "" {
			*token = localToken("read_token")
		}
	}
	if strings.TrimSpace(*token) == "" {
		return fmt.Errorf("MCP token required: set JJP_API_TOKEN (read-only) or JJP_ADMIN_TOKEN")
	}
	api, err := apiclient.New(*srv, *token)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// MCP stdio reserves stdout for JSON-RPC. Human diagnostics belong on stderr.
	fmt.Fprintf(os.Stderr, "jjp MCP %s -> %s (write=%t)\n", protocol.Version, *srv, *allowWrite)
	err = (&mcpserver.Server{API: api, AllowWrite: *allowWrite}).Run(ctx, os.Stdin, os.Stdout)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func cmdToken(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: jjp token <show|rotate>")
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
		return fmt.Errorf("state schema %d is newer than this jjp supports", raw.SchemaVersion)
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
		return fmt.Errorf("usage: jjp token rotate <join|read|admin>")
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
		return fmt.Errorf("usage: jjp state <check|backup|restore>")
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
		return fmt.Errorf("usage: jjp state restore <backup> [--data PATH] --force")
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
		return fmt.Errorf("cannot restore while jjp host is using this state: %w", err)
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
		return fmt.Errorf("usage: jjp service <add|ls|rm>")
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
		return fmt.Errorf("usage: jjp service add <name> (--tcp HOST:PORT | --http URL | --systemd UNIT)")
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
		return fmt.Errorf("usage: jjp service rm <name>")
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
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	defaultToken := os.Getenv("JJP_API_TOKEN")
	if defaultToken == "" {
		defaultToken = os.Getenv("JJP_ADMIN_TOKEN")
	}
	tok := fs.String("token", defaultToken, "read or admin API token")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--json": false})
	err := fs.Parse(args)
	return fs, srv, tok, jsonOut, err
}

func adminFlags(name string, args []string) (*flag.FlagSet, *string, *string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("admin-token", os.Getenv("JJP_ADMIN_TOKEN"), "admin token")
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

func cmdOverview(args []string) error {
	fs := flag.NewFlagSet("overview", flag.ContinueOnError)
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envOr("JJP_API_TOKEN", os.Getenv("JJP_ADMIN_TOKEN")), "read or admin API token")
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
	fmt.Printf("JJP overview  [%s]\n", strings.ToUpper(o.Status))
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
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envOr("JJP_API_TOKEN", os.Getenv("JJP_ADMIN_TOKEN")), "read or admin API token")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--json": false})
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: jjp diagnose <node> [--json]")
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
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envOr("JJP_API_TOKEN", os.Getenv("JJP_ADMIN_TOKEN")), "read or admin API token")
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
				add("authentication", "fail", "no read/admin token found; set JJP_API_TOKEN or pass --token")
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
	fmt.Printf("JJP doctor  [%s]\n", strings.ToUpper(report.Overall))
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
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envOr("JJP_API_TOKEN", os.Getenv("JJP_ADMIN_TOKEN")), "read or admin API token")
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
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envOr("JJP_API_TOKEN", os.Getenv("JJP_ADMIN_TOKEN")), "read or admin API token")
	node := fs.String("node", "", "filter by node name or ID")
	limit := fs.Int("limit", 50, "number of newest events (1-500)")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--node": true, "--limit": true, "--json": false})
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
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", *limit))
	if strings.TrimSpace(*node) != "" {
		q.Set("node", *node)
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
	srv := fs.String("server", envOr("JJP_SERVER", "http://127.0.0.1:7443"), "server URL")
	tok := fs.String("token", envOr("JJP_API_TOKEN", os.Getenv("JJP_ADMIN_TOKEN")), "read or admin API token")
	node := fs.String("node", "", "filter by node name or ID")
	status := fs.String("status", "", "filter by incident status: open or resolved")
	limit := fs.Int("limit", 50, "number of newest incidents (1-500)")
	jsonOut := fs.Bool("json", false, "print machine-readable JSON")
	args = reorderKnownFlags(args, map[string]bool{"--server": true, "--token": true, "--node": true, "--status": true, "--limit": true, "--json": false})
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
	incs, err := api.Incidents(context.Background(), *limit, *node, *status)
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
		return fmt.Errorf("usage: jjp incident <id> [--json]")
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
		return fmt.Errorf("usage: jjp show <node>")
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
		return fmt.Errorf("usage: jjp rename <node> <new-name>")
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
		return fmt.Errorf("usage: jjp rm <node>")
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
