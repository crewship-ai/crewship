package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestArtifactSlidesCLIOriginalRunAndSelectedVersion(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.slides-fixture")
	for _, operation := range []struct{ path, name string }{{"slides", "artifact_slides_read"}, {"export.pdf", "artifact_export_pdf"}} {
		t.Run(operation.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			calls := 0
			transport := artifactAgentRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, _ := io.ReadAll(r.Body)
				if r.Header.Get("Authorization") != "Bearer agtv2.slides-fixture" || r.URL.String() != selectedAgentOrigin+"/mcp/routines" {
					t.Error("original live-run transport lost")
				}
				var packet struct {
					Params struct {
						Name      string                     `json:"name"`
						Arguments map[string]json.RawMessage `json:"arguments"`
					} `json:"params"`
				}
				if json.Unmarshal(raw, &packet) != nil || packet.Params.Name != operation.name || string(packet.Params.Arguments["version"]) != "2" || string(packet.Params.Arguments["slug"]) != `"@page-id"` || len(packet.Params.Arguments) != 2 {
					t.Fatalf("wrong immutable request: %s", raw)
				}
				response := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"deck\":{},\"anchors\":{}}"}]}}`
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			_, err := captureStdoutCov(t, func() error {
				return artifactCLIRequestWithTransport(cmd, "GET", "/api/v1/pages/%40page-id/artifact/"+operation.path+"?version=2", nil, transport)
			})
			if err != nil || calls != 1 {
				t.Fatalf("read-only tool transport: %v calls%d", err, calls)
			}
		})
	}
}

func TestArtifactSlidesCLIRequiresExplicitSafePinAndCommands(t *testing.T) {
	root := newPageArtifactCommand()
	for _, name := range []string{"slides", "export-pdf"} {
		if findSubcommand(root.Commands(), name) == nil {
			t.Errorf("missing %s command", name)
		}
	}
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.slides-fixture")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	for _, suffix := range []string{"", "?version=0", "?version=-1", "?version=9007199254740992"} {
		calls := 0
		transport := artifactAgentRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
		if err := artifactCLIRequestWithTransport(cmd, "GET", "/api/v1/pages/demo/artifact/slides"+suffix, nil, transport); err == nil || calls != 0 {
			t.Errorf("accepted unsafe pin %q: %v calls%d", suffix, err, calls)
		}
	}
}

func TestArtifactSlidesCLIHumanPDFValidationBeforeReplacingOutput(t *testing.T) {
	for _, fixture := range []struct {
		name, mime, body string
		ok               bool
	}{{"valid", "application/pdf", "%PDF-1.7\nfixture", true}, {"wrong-mime", "text/html", "%PDF-1.7\nfixture", false}, {"wrong-magic", "application/pdf", "not a pdf", false}} {
		t.Run(fixture.name, func(t *testing.T) {
			stub := pageStub(t)
			artifact := findSubcommand(pageCmd.Commands(), "artifact")
			if artifact == nil || findSubcommand(artifact.Commands(), "export-pdf") == nil {
				t.Fatal("export-pdf command missing")
			}
			stub.OnGet("/api/v1/pages/@page-id/artifact/export.pdf", func(r *http.Request, _ []byte) (int, []byte, string) {
				if r.URL.Query().Get("version") != "2" {
					t.Error("missing exact version")
				}
				return 200, []byte(fixture.body), fixture.mime
			})
			out := filepath.Join(t.TempDir(), "out.pdf")
			if err := os.WriteFile(out, []byte("keep existing"), 0600); err != nil {
				t.Fatal(err)
			}
			command := findSubcommand(artifact.Commands(), "export-pdf")
			command.Flags().Set("version", "2")
			command.Flags().Set("out", out)
			output, err := runPageCLI(t, "", "page", "artifact", "export-pdf", "@page-id", "--version", "2", "--out", out, "--format", "json")
			got, _ := os.ReadFile(out)
			if fixture.ok {
				if err != nil || !bytes.Equal(got, []byte(fixture.body)) || !strings.Contains(output, `"byte_length"`) {
					t.Fatalf("export: %v %q %s", err, got, output)
				}
			} else if err == nil || string(got) != "keep existing" {
				t.Fatalf("invalid PDF replaced output: %v %q", err, got)
			}
		})
	}
}

func TestArtifactSlidesCLIPDFDTORequiresExactBytesHashAndPin(t *testing.T) {
	pdf := []byte("%PDF-1.7\nfixture")
	sum := sha256.Sum256(pdf)
	valid := map[string]any{"media_type": "application/pdf", "encoding": "base64", "code_version": 2, "byte_length": len(pdf), "sha256": hex.EncodeToString(sum[:]), "data": base64.StdEncoding.EncodeToString(pdf)}
	raw, _ := json.Marshal(valid)
	got, err := decodeArtifactSlidesPDF(raw, 2)
	if err != nil || !bytes.Equal(got, pdf) {
		t.Fatalf("valid DTO refused: %v", err)
	}
	for _, change := range []struct {
		key   string
		value any
	}{{"code_version", 3}, {"byte_length", len(pdf) + 1}, {"sha256", strings.Repeat("0", 64)}, {"encoding", "hex"}, {"media_type", "text/html"}, {"data", "invalid base64"}, {"out", "/host/forbidden.pdf"}} {
		t.Run(change.key, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range valid {
				copy[k] = v
			}
			copy[change.key] = change.value
			bad, _ := json.Marshal(copy)
			if _, err := decodeArtifactSlidesPDF(bad, 2); err == nil {
				t.Fatal("invalid DTO accepted")
			}
		})
	}
	for _, bad := range []string{strings.Replace(string(raw), `"code_version":2`, `"code_version":2,"Code_Version":3`, 1), strings.Replace(string(raw), `"code_version":2`, `"code_version":2,"code_version":3`, 1), strings.Replace(string(raw), `"code_version":2`, `"code_version":2.0`, 1)} {
		if _, err := decodeArtifactSlidesPDF([]byte(bad), 2); err == nil {
			t.Error("ambiguous or fractional pin accepted")
		}
	}
}

func TestArtifactSlidesCLIProtectedPDFValidatesBeforeWrite(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.slides-fixture")
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			pdf := []byte("%PDF-1.7\nprotected fixture")
			sum := sha256.Sum256(pdf)
			hash := hex.EncodeToString(sum[:])
			if !valid {
				hash = strings.Repeat("0", 64)
			}
			dto, _ := json.Marshal(map[string]any{"media_type": "application/pdf", "encoding": "base64", "code_version": 2, "byte_length": len(pdf), "sha256": hash, "data": base64.StdEncoding.EncodeToString(pdf)})
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			path := filepath.Join(t.TempDir(), "caller.pdf")
			if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			transport := artifactAgentRoundTripper(func(r *http.Request) (*http.Response, error) {
				raw, _ := io.ReadAll(r.Body)
				if bytes.Contains(raw, []byte(path)) || bytes.Contains(raw, []byte(`"out"`)) || r.Header.Get("Authorization") != "Bearer agtv2.slides-fixture" {
					t.Fatal("local output path or run identity leaked")
				}
				envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": string(dto)}}}})
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(envelope))}, nil
			})
			output, err := captureStdoutCov(t, func() error {
				return artifactCLIRequestWithResultTransport(cmd, "GET", "/api/v1/pages/demo/artifact/export.pdf?version=2", nil, transport, func(raw []byte) error {
					got, e := decodeArtifactSlidesPDF(raw, 2)
					if e != nil {
						return e
					}
					return saveArtifactSlidesPDF(cmd, path, got, 2)
				})
			})
			got, _ := os.ReadFile(path)
			if valid {
				if err != nil || !bytes.Equal(got, pdf) || strings.Contains(output, `"data"`) {
					t.Fatalf("protected PDF failed or stdout contained bytes: %v %s", err, output)
				}
			} else if err == nil || string(got) != "existing" {
				t.Fatalf("corrupt PDF replaced existing file: %v %q", err, got)
			}
		})
	}
}

// This is a byte-transport fixture, not a rendered/semantically valid PDF.
func TestArtifactSlidesCLINearEightMiBPDFAndOldProjectLimit(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.large-slides-fixture")
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), (8<<20)-9)...)
	if len(pdf) != 8<<20 {
		t.Fatal("fixture must exercise exact PDF byte limit")
	}
	sum := sha256.Sum256(pdf)
	dto, _ := json.Marshal(map[string]any{"media_type": "application/pdf", "encoding": "base64", "code_version": 2, "byte_length": len(pdf), "sha256": hex.EncodeToString(sum[:]), "data": base64.StdEncoding.EncodeToString(pdf)})
	envelope, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": string(dto)}}}})
	if len(dto) <= 8<<20 || len(dto) >= 16<<20 || len(envelope) >= 32<<20 {
		t.Fatal("fixture does not exercise actual DTO/envelope boundaries")
	}
	var calls int
	transport := artifactAgentRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer agtv2.large-slides-fixture" {
			t.Error("original live bearer lost")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(envelope))}, nil
	})
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	path := filepath.Join(t.TempDir(), "large.pdf")
	output, err := captureStdoutCov(t, func() error {
		return artifactCLIRequestWithResultTransport(cmd, "GET", "/api/v1/pages/demo/artifact/export.pdf?version=2", nil, transport, func(raw []byte) error {
			decoded, err := decodeArtifactSlidesPDF(raw, 2)
			if err != nil {
				return err
			}
			return saveArtifactSlidesPDF(cmd, path, decoded, 2)
		})
	})
	got, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || !bytes.Equal(got, pdf) || len(output) > 1024 || calls != 1 {
		t.Fatalf("legal large PDF transport failed: %v read%v output%d calls%d", err, readErr, len(output), calls)
	}
	consumed := false
	err = artifactCLIRequestWithResultTransport(cmd, "GET", "/api/v1/pages/demo/artifact?version=2", nil, transport, func([]byte) error { consumed = true; return nil })
	if err == nil || consumed || calls != 2 {
		t.Fatalf("old Project response cap widened: %v consumed%v calls%d", err, consumed, calls)
	}
}
