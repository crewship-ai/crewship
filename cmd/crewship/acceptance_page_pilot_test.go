package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Optional actual collector output from the isolated Docker pilot. The CLI runs
// outside those containers: no human token is injected into a producer container.
func exerciseOperationalPageResults(t *testing.T, must func(...string) string) {
	t.Helper()
	file := os.Getenv("PAGES_TEST_PILOT_RESULTS")
	if file == "" {
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 65536 {
		t.Fatal("oversized pilot output")
	}
	var results map[string]struct {
		Name          string `json:"name"`
		State         string `json:"state"`
		Label         string `json:"label"`
		ProducerState string `json:"producer_state"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"mysql_healthy", "mysql_unavailable", "ansible_check", "ansible_failed"} {
		result, ok := results[key]
		if !ok || result.Name == "" || result.Label == "" {
			t.Fatal("missing operational result", key)
		}
		want := "ok"
		if strings.Contains(key, "unavailable") || strings.Contains(key, "failed") {
			want = "failed"
		}
		if result.ProducerState != want {
			t.Fatal("unexpected collector verdict", key)
		}
		if i > 0 {
			time.Sleep(2100 * time.Millisecond)
		} // obey the real default push floor
		body, err := json.Marshal(map[string]any{"items": []map[string]string{{"name": result.Name, "state": result.State, "label": result.Label}}})
		if err != nil {
			t.Fatal(err)
		}
		must("page", "set", "health/mysql", "--data", string(body), "--state", result.ProducerState)
		page := must("page", "get", "health")
		if !strings.Contains(page, result.Label) {
			t.Fatalf("collector verdict absent from Page: %s", key)
		}
		t.Log("real collector -> CLI -> Page snapshot:", key, result.ProducerState)
	}
}
