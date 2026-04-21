package daemon

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/skmtkytr/stor/engine"
)

// contentTypePrometheus is the Prometheus text-format Content-Type.
// https://prometheus.io/docs/instrumenting/exposition_formats/#text-based-format
const contentTypePrometheus = "text/plain; version=0.0.4; charset=utf-8"

// metricKind is the Prometheus metric kind used for `# TYPE` lines.
type metricKind string

const (
	kindCounter metricKind = "counter"
	kindGauge   metricKind = "gauge"
)

// writeMetric emits one metric family: HELP + TYPE header, then one or more
// samples. For a simple (no-label) metric, pass a single sample with nil
// labels and the value.
type sample struct {
	labels map[string]string
	value  float64
}

func writeMetric(w io.Writer, name, help string, kind metricKind, samples []sample) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, escapeHelp(help))
	fmt.Fprintf(w, "# TYPE %s %s\n", name, kind)
	for _, s := range samples {
		if len(s.labels) == 0 {
			fmt.Fprintf(w, "%s %s\n", name, formatFloat(s.value))
			continue
		}
		keys := make([]string, 0, len(s.labels))
		for k := range s.labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString(name)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(k)
			b.WriteString(`="`)
			b.WriteString(escapeLabelValue(s.labels[k]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
		fmt.Fprintf(w, "%s %s\n", b.String(), formatFloat(s.value))
	}
}

// escapeHelp escapes backslashes and newlines per the Prometheus text format.
func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// escapeLabelValue escapes backslashes, double quotes, and newlines.
func escapeLabelValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// formatFloat renders a value in a way Prometheus accepts. Integer-valued
// floats are rendered without trailing zeros to keep output compact.
func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// handleMetrics renders engine stats in Prometheus text format.
func (d *Daemon) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", contentTypePrometheus)

	stats := d.engine.GetStats()
	torrents := d.engine.ListTorrents()

	// Count per state so clients can alert on "too many errored", etc.
	stateCounts := map[engine.State]int{
		engine.StateAdding:      0,
		engine.StateMetadata:    0,
		engine.StateVerifying:   0,
		engine.StateDownloading: 0,
		engine.StateSeeding:     0,
		engine.StateComplete:    0,
		engine.StatePaused:      0,
		engine.StateError:       0,
	}
	for _, t := range torrents {
		stateCounts[t.State]++
	}

	torrentSamples := make([]sample, 0, len(stateCounts))
	for state, n := range stateCounts {
		torrentSamples = append(torrentSamples, sample{
			labels: map[string]string{"state": string(state)},
			value:  float64(n),
		})
	}

	writeMetric(w, "stor_torrents_total",
		"Number of torrents tracked by the engine, labeled by state.",
		kindGauge, torrentSamples)

	writeMetric(w, "stor_peers_total",
		"Total number of currently connected peers across all torrents.",
		kindGauge, []sample{{value: float64(stats.TotalPeers)}})

	writeMetric(w, "stor_dht_nodes_total",
		"Number of nodes in the DHT routing table.",
		kindGauge, []sample{{value: float64(stats.DHTNodes)}})

	writeMetric(w, "stor_download_bytes_total",
		"Total bytes downloaded since the daemon started (cumulative).",
		kindCounter, []sample{{value: float64(stats.TotalDownloaded)}})

	writeMetric(w, "stor_upload_bytes_total",
		"Total bytes uploaded since the daemon started (cumulative).",
		kindCounter, []sample{{value: float64(stats.TotalUploaded)}})

	writeMetric(w, "stor_download_speed_bytes_per_second",
		"Current aggregate download speed in bytes per second.",
		kindGauge, []sample{{value: float64(stats.TotalDownSpeed)}})

	writeMetric(w, "stor_upload_speed_bytes_per_second",
		"Current aggregate upload speed in bytes per second.",
		kindGauge, []sample{{value: float64(stats.TotalUpSpeed)}})

	writeMetric(w, "stor_free_space_bytes",
		"Free disk space on the download volume, in bytes.",
		kindGauge, []sample{{value: float64(stats.FreeSpace)}})

	writeMetric(w, "stor_build_info",
		"Build metadata for stor. Constant 1 with version/go_version labels.",
		kindGauge, []sample{{
			labels: map[string]string{
				"version":    Version,
				"go_version": runtime.Version(),
			},
			value: 1,
		}})

	// Go runtime metrics. Prometheus's official client exposes these under
	// the `go_*` namespace; keep parity so existing dashboards work.
	writeMetric(w, "go_goroutines",
		"Number of goroutines that currently exist.",
		kindGauge, []sample{{value: float64(runtime.NumGoroutine())}})

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	writeMetric(w, "go_memstats_alloc_bytes",
		"Number of bytes allocated and still in use.",
		kindGauge, []sample{{value: float64(memStats.Alloc)}})
	writeMetric(w, "go_memstats_sys_bytes",
		"Number of bytes obtained from the OS.",
		kindGauge, []sample{{value: float64(memStats.Sys)}})
	writeMetric(w, "go_gc_duration_seconds_sum",
		"Sum of all GC pauses in seconds.",
		kindCounter, []sample{{value: float64(memStats.PauseTotalNs) / 1e9}})
	writeMetric(w, "go_gc_duration_seconds_count",
		"Total number of GC cycles.",
		kindCounter, []sample{{value: float64(memStats.NumGC)}})
}
