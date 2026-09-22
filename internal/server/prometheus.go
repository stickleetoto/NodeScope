package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) prometheusMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "read or admin token required")
		return
	}

	now := time.Now().UTC()
	nodes := s.Store.Views(now)
	alerts := s.Store.Alerts("")
	incidents := s.Store.Incidents(2000, "", "open")
	health := s.systemHealthSnapshot()

	var online, unstable, offline int
	for _, node := range nodes {
		switch node.Status {
		case "ONLINE":
			online++
		case "UNSTABLE":
			unstable++
		default:
			offline++
		}
	}

	var b strings.Builder
	writePromHelp(&b, "nodescope_nodes_total", "Registered NodeScope nodes.", "gauge")
	writePromSample(&b, "nodescope_nodes_total", nil, float64(len(nodes)))
	writePromHelp(&b, "nodescope_nodes_online", "Nodes currently online.", "gauge")
	writePromSample(&b, "nodescope_nodes_online", nil, float64(online))
	writePromHelp(&b, "nodescope_nodes_unstable", "Nodes with delayed heartbeats.", "gauge")
	writePromSample(&b, "nodescope_nodes_unstable", nil, float64(unstable))
	writePromHelp(&b, "nodescope_nodes_offline", "Nodes considered offline.", "gauge")
	writePromSample(&b, "nodescope_nodes_offline", nil, float64(offline))
	writePromHelp(&b, "nodescope_active_alerts", "Current active alerts.", "gauge")
	writePromSample(&b, "nodescope_active_alerts", nil, float64(len(alerts)))
	writePromHelp(&b, "nodescope_active_incidents", "Current open incidents.", "gauge")
	writePromSample(&b, "nodescope_active_incidents", nil, float64(len(incidents)))

	writePromHelp(&b, "nodescope_node_up", "Whether a node is online (1) or not (0).", "gauge")
	writePromHelp(&b, "nodescope_node_cpu_ratio", "Latest node CPU utilization ratio.", "gauge")
	writePromHelp(&b, "nodescope_node_memory_ratio", "Latest node memory utilization ratio.", "gauge")
	writePromHelp(&b, "nodescope_node_disk_ratio", "Latest legacy root disk utilization ratio.", "gauge")
	writePromHelp(&b, "nodescope_node_heartbeat_age_seconds", "Age of the latest node heartbeat.", "gauge")
	writePromHelp(&b, "nodescope_service_up", "Whether a configured service check is healthy (1) or not (0).", "gauge")
	writePromHelp(&b, "nodescope_service_latency_seconds", "Latest service-check latency.", "gauge")

	for _, node := range nodes {
		labels := map[string]string{
			"node_id": node.ID,
			"node_name": node.Name,
			"os": node.OS,
			"arch": node.Arch,
		}
		up := 0.0
		if node.Status == "ONLINE" {
			up = 1
		}
		writePromSample(&b, "nodescope_node_up", labels, up)
		writePromSample(&b, "nodescope_node_cpu_ratio", labels, node.Metrics.CPUPercent/100)
		writePromSample(&b, "nodescope_node_memory_ratio", labels, node.Metrics.RAMPercent/100)
		writePromSample(&b, "nodescope_node_disk_ratio", labels, node.Metrics.DiskPercent/100)
		age := 0.0
		if !node.LastHeartbeat.IsZero() {
			age = now.Sub(node.LastHeartbeat).Seconds()
			if age < 0 {
				age = 0
			}
		}
		writePromSample(&b, "nodescope_node_heartbeat_age_seconds", labels, age)

		for _, svc := range node.Services {
			svcLabels := map[string]string{
				"node_id": node.ID,
				"node_name": node.Name,
				"service": svc.Name,
				"type": svc.Type,
			}
			value := 0.0
			if svc.Healthy {
				value = 1
			}
			writePromSample(&b, "nodescope_service_up", svcLabels, value)
			writePromSample(&b, "nodescope_service_latency_seconds", svcLabels, float64(svc.LatencyMS)/1000)
		}
	}

	writePromHelp(&b, "nodescope_http_requests_total", "HTTP requests observed by NodeScope.", "counter")
	writePromSample(&b, "nodescope_http_requests_total", nil, float64(health.RequestsTotal))
	writePromHelp(&b, "nodescope_http_client_errors_total", "HTTP 4xx responses.", "counter")
	writePromSample(&b, "nodescope_http_client_errors_total", nil, float64(health.ClientErrorsTotal))
	writePromHelp(&b, "nodescope_http_server_errors_total", "HTTP 5xx responses.", "counter")
	writePromSample(&b, "nodescope_http_server_errors_total", nil, float64(health.ServerErrorsTotal))
	writePromHelp(&b, "nodescope_http_request_duration_average_seconds", "Average HTTP request duration since process start.", "gauge")
	writePromSample(&b, "nodescope_http_request_duration_average_seconds", nil, health.RequestAverageMS/1000)
	writePromHelp(&b, "nodescope_http_request_duration_max_seconds", "Maximum HTTP request duration since process start.", "gauge")
	writePromSample(&b, "nodescope_http_request_duration_max_seconds", nil, health.RequestMaxMS/1000)

	writePromHelp(&b, "nodescope_heartbeats_total", "Successful node heartbeats processed.", "counter")
	writePromSample(&b, "nodescope_heartbeats_total", nil, float64(health.HeartbeatsTotal))
	writePromHelp(&b, "nodescope_telemetry_batches_total", "Telemetry batches accepted or deduplicated by the endpoint.", "counter")
	writePromSample(&b, "nodescope_telemetry_batches_total", nil, float64(health.TelemetryBatchesTotal))
	writePromHelp(&b, "nodescope_telemetry_samples_total", "New telemetry samples durably accepted.", "counter")
	writePromSample(&b, "nodescope_telemetry_samples_total", nil, float64(health.TelemetrySamplesTotal))

	writePromHelp(&b, "nodescope_history_bytes", "Bytes used by raw and rollup telemetry history.", "gauge")
	writePromSample(&b, "nodescope_history_bytes", nil, float64(health.HistoryBytes))
	writePromHelp(&b, "nodescope_history_max_bytes", "Configured total telemetry history byte limit.", "gauge")
	writePromSample(&b, "nodescope_history_max_bytes", nil, float64(health.HistoryMaxBytes))
	writePromHelp(&b, "nodescope_history_raw_segments", "Number of sealed raw history segments.", "gauge")
	writePromSample(&b, "nodescope_history_raw_segments", nil, float64(health.HistoryRawSegments))
	writePromHelp(&b, "nodescope_history_rollup_files", "Number of persistent one-minute rollup files.", "gauge")
	writePromSample(&b, "nodescope_history_rollup_files", nil, float64(health.HistoryRollupFiles))
	writePromHelp(&b, "nodescope_history_rollup_bytes", "Bytes used by persistent rollup files.", "gauge")
	writePromSample(&b, "nodescope_history_rollup_bytes", nil, float64(health.HistoryRollupBytes))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(b.String()))
}

func writePromHelp(b *strings.Builder, name, help, typ string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, strings.ReplaceAll(help, "\n", " "))
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
}

func writePromSample(b *strings.Builder, name string, labels map[string]string, value float64) {
	b.WriteString(name)
	if len(labels) > 0 {
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sortStrings(keys)
		b.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(key)
			b.WriteString("=\"")
			b.WriteString(promLabelEscape(labels[key]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	b.WriteByte('\n')
}

func promLabelEscape(v string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(v)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
