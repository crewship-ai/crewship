package manifest

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeCrewFileDest(t *testing.T) {
	cases := []struct {
		name, src, dest, want string
		ok                    bool
	}{
		{"default under shared", "scripts/parse.py", "", "shared/parse.py", true},
		{"explicit shared path", "x.py", "shared/scripts/x.py", "shared/scripts/x.py", true},
		{"crew-absolute normalized", "x.py", "/crew/shared/scripts/x.py", "shared/scripts/x.py", true},
		{"traversal rejected", "x.py", "shared/../../etc/passwd", "", false},
		{"outside shared rejected", "x.py", "output/x.py", "", false},
		{"bare shared dir rejected", "x.py", "shared", "", false},
		{"empty src empty dest", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeCrewFileDest(tc.src, tc.dest)
			if tc.ok && (err != nil || got != tc.want) {
				t.Errorf("got (%q, %v), want %q", got, err, tc.want)
			}
			if !tc.ok && err == nil {
				t.Errorf("expected error, got %q", got)
			}
		})
	}
}

func TestLoadCrewFile_SizeCap(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, crewFileMaxBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCrewFile(dir, "big.bin"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("oversized file must be rejected with a size error, got %v", err)
	}
	ok := filepath.Join(dir, "ok.py")
	if err := os.WriteFile(ok, []byte("print('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := loadCrewFile(dir, "ok.py")
	if err != nil || string(data) != "print('hi')" {
		t.Errorf("got (%q, %v)", data, err)
	}
}

func TestValidateCrewFiles(t *testing.T) {
	yaml := `
apiVersion: crewship/v1
kind: Crew
metadata:
  name: T
  slug: t
spec:
  files:
    - src: scripts/parse.py
      dest: shared/scripts/parse.py
    - src: ""
    - src: x.py
      dest: ../../etc/passwd
  agents:
    - slug: lead
      name: Lead
      role: LEAD
`
	b, err := Load([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	err = b.Validate()
	if err == nil {
		t.Fatal("expected validation errors for missing src + traversal dest")
	}
	msg := err.Error()
	if !strings.Contains(msg, "files[1]") || !strings.Contains(msg, "files[2]") {
		t.Errorf("errors should name the offending entries, got: %v", msg)
	}
	// The valid entry must have parsed.
	if len(b.Documents[0].Spec.Files) != 3 || b.Documents[0].Spec.Files[0].Src != "scripts/parse.py" {
		t.Errorf("files did not parse: %+v", b.Documents[0].Spec.Files)
	}
}

// putCapturingAPI wraps the standard fake with a rawPutter so SaveCrewFile
// has a transport in tests.
type putCapturingAPI struct {
	APIClient
	puts []string
}

func (p *putCapturingAPI) PutBytes(_ context.Context, path string, body []byte) (*http.Response, error) {
	p.puts = append(p.puts, path+" ["+string(body)+"]")
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}

func TestSaveCrewFile_PutsToFilesSave(t *testing.T) {
	api := &putCapturingAPI{}
	c := NewClient(api)
	if err := c.SaveCrewFile(context.Background(), "crew_1", "shared/scripts/x.py", []byte("data")); err != nil {
		t.Fatalf("SaveCrewFile: %v", err)
	}
	if len(api.puts) != 1 || !strings.Contains(api.puts[0], "/api/v1/crews/crew_1/files/save?path=shared%2Fscripts%2Fx.py") {
		t.Errorf("unexpected PUT: %v", api.puts)
	}
	// An APIClient without raw PUT support must fail loudly, not no-op.
	plain := NewClient(nilAPI{})
	if err := plain.SaveCrewFile(context.Background(), "c", "shared/x", nil); err == nil {
		t.Error("expected 'not supported' error for a client without PutBytes")
	}
}

type nilAPI struct{}

func (nilAPI) Get(context.Context, string) (*http.Response, error)        { return nil, nil }
func (nilAPI) Post(context.Context, string, any) (*http.Response, error)  { return nil, nil }
func (nilAPI) Patch(context.Context, string, any) (*http.Response, error) { return nil, nil }
func (nilAPI) Delete(context.Context, string) (*http.Response, error)     { return nil, nil }
func (nilAPI) GetWorkspaceID() string                                     { return "" }
