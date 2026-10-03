package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// listingS3 is miniS3 plus ListObjectsV2 (GET /<bucket>?list-type=2&prefix=),
// which listing a destination's bundles needs.
type listingS3 struct{ *miniS3 }

func (l listingS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Query().Get("list-type") != "2" {
		l.miniS3.ServeHTTP(w, r)
		return
	}
	bucket := "/" + strings.Trim(r.URL.Path, "/") + "/"
	prefix := r.URL.Query().Get("prefix")
	type content struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	}
	var res struct {
		XMLName     xml.Name  `xml:"ListBucketResult"`
		IsTruncated bool      `xml:"IsTruncated"`
		Contents    []content `xml:"Contents"`
	}
	l.mu.Lock()
	for k, b := range l.objects {
		key := strings.TrimPrefix(k, bucket)
		if strings.HasPrefix(k, bucket) && strings.HasPrefix(key, prefix) {
			res.Contents = append(res.Contents, content{Key: key, Size: int64(len(b)), LastModified: time.Now().UTC().Format(time.RFC3339)})
		}
	}
	l.mu.Unlock()
	sort.Slice(res.Contents, func(i, j int) bool { return res.Contents[i].Key < res.Contents[j].Key })
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(res)
}

// A real CLI process against a real router and a local S3 stand-in: restore
// from an off-site copy. The admin lists what the destination holds, fetches
// a bundle back as a server job and follows it, and the bundle lands in this
// server's backups directory, verified. Nothing in the server is stubbed.
func TestAcceptance_AdminInstanceBackupCopies(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv(encryption.KeyEnvVar("v1"), strings.Repeat("c4", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_instbackcopies00000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('bc-people','People','people','2026-01-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('bc-boss','boss@people.invalid','Boss')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('bc-m1','bc-people','bc-boss','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('bc-token','bc-boss','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	s3 := &miniS3{objects: map[string][]byte{}, sums: map[string]string{}}
	s3srv := httptest.NewServer(listingS3{s3})
	defer s3srv.Close()

	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: "+token+"\nformat: table\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	cli := func(args ...string) []string { return append([]string{"admin", "instance", "backups"}, args...) }

	secret := filepath.Join(tmp, "s3.secret")
	if err := os.WriteFile(secret, []byte("s3-secret-access-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := must(cli("destinations", "add", "--name", "lab-minio", "--endpoint", s3srv.URL, "--bucket", "backups", "--prefix", "crewship",
		"--access-key-id", "AKIDTEST", "--secret-file", secret, "--path-style", "--allow-private-network", "--format", "json")...)
	var created struct {
		Destination struct {
			ID string `json:"id"`
		} `json:"destination"`
	}
	if err := json.Unmarshal([]byte(raw), &created); err != nil || created.Destination.ID == "" {
		t.Fatalf("destinations add: %v\n%s", err, raw)
	}
	destID := created.Destination.ID

	// What an earlier server copied off-site: a bundle, a workspace bundle
	// with its refs object, and a shared layer. Only bundles are listed.
	put := func(key string, b []byte) {
		sum := sha256.Sum256(b)
		s3.mu.Lock()
		s3.objects["/backups/crewship/"+key], s3.sums["/backups/crewship/"+key] = b, hex.EncodeToString(sum[:])
		s3.mu.Unlock()
	}
	const inst = "instance/crewship-instance-20260929T010000Z.tar.zst"
	var bundle bytes.Buffer
	manifest := &backup.Manifest{FormatVersion: backup.FormatVersion, Scope: backup.ScopeInstance, CreatedBy: backup.Actor{UserID: "fixture-owner"}, CreatedAt: time.Now().UTC(), CompatibleTargets: []backup.Target{backup.TargetAnyInstance}}
	if err := backup.WriteBundle(&bundle, manifest, strings.NewReader("the instance payload"), backup.WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatal(err)
	}
	put(inst, bundle.Bytes())
	var workspaceBundle bytes.Buffer
	workspaceManifest := *manifest
	workspaceManifest.Scope = backup.ScopeWorkspace
	if err := backup.WriteBundle(&workspaceBundle, &workspaceManifest, strings.NewReader("workspace payload"), backup.WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatal(err)
	}
	put("workspaces/bc-people/crewship-workspace-people-20260929T010000Z.tar.zst", workspaceBundle.Bytes())
	put("workspaces/bc-people/crewship-workspace-people-20260929T010000Z.tar.zst.environments.json", []byte(`{"blobs":[]}`))

	if out, err := run(cli("copies", "list")...); err == nil || !strings.Contains(out, "--destination") {
		t.Fatalf("list without a destination: %v\n%s", err, out)
	}
	out := must(cli("copies", "list", "--destination", destID)...)
	if !strings.Contains(out, inst) || !strings.Contains(out, "workspace bc-people") || strings.Contains(out, "environments.json") {
		t.Fatalf("copies list:\n%s", out)
	}

	out = must(cli("copies", "fetch", "--destination", destID, "--key", inst, "--wait")...)
	if !strings.Contains(out, "Fetched "+inst) || !strings.Contains(out, "crewship recover --bundle") {
		t.Fatalf("copies fetch:\n%s", out)
	}
	dir, _ := backup.DefaultBackupsDir()
	local := filepath.Join(dir, filepath.Base(inst))
	if b, err := os.ReadFile(local); err != nil || !bytes.Equal(b, bundle.Bytes()) {
		t.Fatalf("fetched bundle: %q %v", b, err)
	}

	if entry, err := backup.GetCatalogEntry(t.Context(), db, local); err != nil || entry.Scope != string(backup.ScopeInstance) {
		t.Fatalf("fetch did not register catalog: %+v %v", entry, err)
	}

	raw = must(cli("copies", "list", "--destination", destID, "--format", "json")...)
	var list struct {
		Copies []struct {
			Key       string  `json:"key"`
			Local     bool    `json:"local"`
			LocalPath *string `json:"local_path"`
		} `json:"copies"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list.Copies) != 2 || !list.Copies[0].Local || *list.Copies[0].LocalPath != local || list.Copies[1].Local {
		t.Fatalf("copies list json: %v\n%s", err, raw)
	}
	if out, err := run(cli("copies", "fetch", "--destination", destID, "--key", inst)...); err == nil || !strings.Contains(out, "already on this server") {
		t.Fatalf("a second fetch: %v\n%s", err, out)
	}

	// A fetch the CLI does not wait for is followed by id.
	raw = must(cli("copies", "fetch", "--destination", destID, "--key", "workspaces/bc-people/crewship-workspace-people-20260929T010000Z.tar.zst", "--format", "json")...)
	var job struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &job); err != nil || job.ID == "" {
		t.Fatalf("fetch without --wait: %v\n%s", err, raw)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		out = must(cli("copies", "status", job.ID)...)
		if strings.Contains(out, "Fetched ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("copies status:\n%s", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := run(cli("copies", "status", "nope")...); err == nil {
		t.Fatalf("status of an unknown fetch succeeded:\n%s", out)
	}
	if out, err := run(cli("copies", "fetch", "--destination", destID, "--key", "instance/missing.tar.zst")...); err == nil {
		t.Fatalf("fetching a missing key succeeded:\n%s", out)
	}
}
