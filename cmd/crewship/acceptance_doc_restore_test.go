package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Real binary/flag/HTTP contract; the HTTP stub is not engine or ACL evidence.
func TestAcceptance_DocumentRestoreOriginalPacket(t *testing.T) {
	packet := `{"page_id":"page-one","id":"restore-one","schema":3,"epoch":"captured-epoch","ifRev":8,"checkpoint":"pin-one"}`
	calls := 0
	status := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/pages/@page-one/document/restore" {
			t.Errorf("unexpected restore route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		calls++
		body, err := io.ReadAll(r.Body)
		var got, want any
		if err != nil || json.Unmarshal(body, &got) != nil || json.Unmarshal([]byte(packet), &want) != nil || !reflect.DeepEqual(got, want) {
			t.Error("binary changed original restore packet")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == 200 {
			_, _ = io.WriteString(w, `{"page_id":"page-one","id":"restore-one","rev":9,"schema":2,"epoch":"restored-epoch","kind":"restore"}`)
		} else {
			_, _ = io.WriteString(w, `{"error":"original revision changed"}`)
		}
	}))
	defer server.Close()
	binary := buildCrewshipBinary(t)
	config := askCLIConfig(t)
	// The same immutable file is used for both uncertain-confirmation retries.
	file := filepath.Join(t.TempDir(), "restore.json")
	if err := os.WriteFile(file, []byte(packet), 0600); err != nil {
		t.Fatal(err)
	}
	for _, code := range []int{200, 200, 409} {
		status = code
		before := calls
		out, err := runCrewship(t, binary, config, server.URL, "doc", "restore", "@page-one", "--file", file, "-f", "json")
		if code == 200 && (err != nil || !strings.Contains(out, `"restore-one"`)) {
			t.Fatalf("restore command unavailable or unconfirmed: %v %s", err, out)
		}
		if code != 200 && err == nil {
			t.Fatal("conflicting restore succeeded")
		}
		if calls != before+1 {
			t.Fatal("restore silently retried or made an extra read")
		}
	}
}
