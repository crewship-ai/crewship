// ping-go — a Go producer for the `sit` page.
//
// It measures TCP round-trip to a handful of well-known addresses every five
// seconds and pushes two panels: a status grid (reachable / slow / down) and a
// metric with a sparkline.
//
// The point of this example is not the measurement, it is the shape. A
// producer is any process that can compute something and make one HTTP call.
// It holds no schema library, imports nothing from Crewship, and knows nothing
// about the page beyond two panel ids. The page holds no query, no connection
// string and no credential for the thing being measured (§0) — this program is
// the only thing that ever talks to 8.8.8.8, and Crewship never does.
//
// Provenance is not this program's to claim: `produced_at`, the run reference
// and the freshness verdict are attached by the server, and a payload carrying
// any of them is refused (§4 rules 2 and 5).
//
//	CREWSHIP_SERVER=http://localhost:8083 CREWSHIP_TOKEN=… \
//	CREWSHIP_WORKSPACE=… go run ./examples/pages/ping-go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"time"
)

// target is one thing worth being able to reach. Latency thresholds turn a
// number into a state, which is the whole job of a status panel: 8.8.8.8 at
// 5 ms is healthy, at 300 ms something is wrong even though it answered.
type target struct {
	name string
	addr string
	warn time.Duration
}

var targets = []target{
	{"8.8.8.8 (DNS)", "8.8.8.8:53", 80 * time.Millisecond},
	{"1.1.1.1 (DNS)", "1.1.1.1:53", 80 * time.Millisecond},
	{"google.com", "google.com:443", 200 * time.Millisecond},
	{"github.com", "github.com:443", 300 * time.Millisecond},
}

type statusItem struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Label string `json:"label"`
}

func main() {
	base := env("CREWSHIP_SERVER", "http://localhost:8083")
	token := os.Getenv("CREWSHIP_TOKEN")
	ws := os.Getenv("CREWSHIP_WORKSPACE")
	if token == "" || ws == "" {
		fmt.Fprintln(os.Stderr, "CREWSHIP_TOKEN and CREWSHIP_WORKSPACE are required")
		os.Exit(2)
	}

	// The sparkline is this program's memory. §11b.16 makes even spacing a
	// CONTRACT rather than an implication, so the producer is the thing
	// responsible for only appending points it took at an even cadence — the
	// panel cannot check that for us and does not try.
	var spark []float64

	// A five-second cadence is well clear of the server's floor. §10b.3 caps a
	// panel at 12 pushes a minute with a burst of 30, and the floor at the
	// write is one push per two seconds; a tighter loop would not get more
	// data, it would get 429s.
	for {
		items := make([]statusItem, 0, len(targets))
		var primary time.Duration
		for i, t := range targets {
			d, err := dial(t.addr)
			switch {
			case err != nil:
				items = append(items, statusItem{t.name, "critical", "nedostupné"})
			case d > t.warn:
				items = append(items, statusItem{t.name, "warning", fmt.Sprintf("%d ms — pomalé", d.Milliseconds())})
			default:
				items = append(items, statusItem{t.name, "ok", fmt.Sprintf("%d ms", d.Milliseconds())})
			}
			if i == 0 && err == nil {
				primary = d
			}
		}

		if err := push(base, token, ws, "dosah", map[string]any{"items": items}); err != nil {
			fmt.Fprintln(os.Stderr, "dosah:", err)
		}

		if primary > 0 {
			ms := float64(primary.Microseconds()) / 1000
			spark = append(spark, ms)
			if len(spark) > 40 {
				spark = spark[len(spark)-40:]
			}
			// `delta` is omitted on the first pass rather than sent as null:
			// "no change" (0) and "nothing to compare with" (absent) are
			// different claims and the panel draws them differently (§9b.4).
			payload := map[string]any{
				"value":      round1(ms),
				"unit":       "ms",
				"sparkline":  spark,
				"delta_good": "down", // lower latency is the good direction
				"target":     50,
			}
			if len(spark) > 1 {
				payload["delta"] = round1(ms - spark[len(spark)-2])
			}
			if err := push(base, token, ws, "latence", payload); err != nil {
				fmt.Fprintln(os.Stderr, "latence:", err)
			}
		}

		time.Sleep(5 * time.Second)
	}
}

func dial(addr string) (time.Duration, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return 0, err
	}
	_ = conn.Close()
	return time.Since(start), nil
}

// push is the entire Crewship dependency: one PUT with the payload as the
// body. There is no client library to import and no schema to compile against
// — a 4xx here means the payload broke the panel's contract, and the server's
// own sentence is more useful than anything this program could invent.
func push(base, token, ws, panel string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/api/v1/pages/sit/panels/%s/data?workspace_id=%s", base, panel, ws)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := pushClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, trim(buf.String(), 200))
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// pushClient exists because http.DefaultClient has NO timeout: a server that
// accepts the connection and then says nothing hangs this loop forever, and a
// producer that stops pushing is exactly what the freshness contract reports as
// a fault. Better to fail the push, print it, and be back in five seconds.
var pushClient = &http.Client{Timeout: 15 * time.Second}

// round1 rounds to one decimal. math.Round rather than int(f*10+0.5), which
// truncates toward zero and so rounds NEGATIVE values the wrong way — and a
// delta here is negative half the time.
func round1(f float64) float64 { return math.Round(f*10) / 10 }

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
