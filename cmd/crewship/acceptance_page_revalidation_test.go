package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"
)

// Called by the real CLI/Docker acceptance after publication. Requests traverse
// the real token/workspace middleware; changing a DB role models an admin revoke
// without forging roles in request context.
func exercisePageRevalidation(t *testing.T, db *sql.DB, server, ws string) {
	t.Helper()
	token := "crewship_cli_pagesviewer00000000000000000"
	for _, query := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('pages-load-viewer','viewer@example.invalid','Load Viewer')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pages-load-member','cabcdefghijklmnopqrs','pages-load-viewer','ADMIN')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('pages-load-token','pages-load-viewer','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	get := func(etag string) (int, string, int64, error) {
		req, err := http.NewRequest("GET", server+"/api/v1/pages/health/application?workspace_id="+ws, nil)
		if err != nil {
			return 0, "", 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		response, err := client.Do(req)
		if err != nil {
			return 0, "", 0, err
		}
		defer response.Body.Close()
		bytes, err := io.Copy(io.Discard, response.Body)
		return response.StatusCode, response.Header.Get("ETag"), bytes, err
	}
	status, etag, initialBytes, err := get("")
	if err != nil || status != 200 || etag == "" || initialBytes == 0 {
		t.Fatalf("initial application: %d %v", status, err)
	}
	const viewers = 24
	const reads = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	var durations []time.Duration
	var transferred int64
	started := time.Now()
	for i := 0; i < viewers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < reads; j++ {
				begin := time.Now()
				status, _, size, err := get(etag)
				elapsed := time.Since(begin)
				mu.Lock()
				durations = append(durations, elapsed)
				transferred += size
				if err != nil || status != 304 || size != 0 {
					failures = append(failures, fmt.Sprintf("status=%d bytes=%d error=%v", status, size, err))
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(started)
	if len(failures) > 0 {
		t.Fatalf("conditional readers: %v", failures)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("24 concurrent viewers, %d conditional reads: elapsed=%s p50=%s p95=%s p99=%s transferred=%d bytes (initial artifact=%d bytes)", len(durations), elapsed, durations[len(durations)/2], durations[len(durations)*95/100], durations[len(durations)*99/100], transferred, initialBytes)
	if _, err := db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id='pages-load-viewer'`, ws); err != nil {
		t.Fatal(err)
	}
	if status, _, _, err := get(etag); err != nil || status != 404 {
		t.Fatalf("revoked role reused cached artifact: %d %v", status, err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id='pages-load-viewer'`, ws); err != nil {
		t.Fatal(err)
	}
	if status, _, _, err := get(etag); err != nil || status != 304 {
		t.Fatalf("restored access: %d %v", status, err)
	}
	// Closing transport connections forces a reconnect, independent of cache state.
	client.CloseIdleConnections()
	if status, _, size, err := get(etag); err != nil || status != 304 || size != 0 {
		t.Fatalf("reconnect: %d %v", status, err)
	}
}
