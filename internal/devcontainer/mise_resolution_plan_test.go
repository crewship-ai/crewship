package devcontainer

import (
	"bytes"
	"encoding/binary"
	"github.com/moby/moby/api/pkg/stdcopy"
	"strings"
	"testing"
)

func TestMiseNativeResolutionPlan(t *testing.T) {
	raw := `[{"name":"node","backend":"core:node","lockfile":"~/mise.lock","old_versions":["22.0.0"],"new_versions":["22.23.3"]},{"name":"claude","backend":"aqua:anthropics/claude-code","old_versions":["2.1.288"],"new_versions":["2.1.288"]}]`
	tools, err := parseMiseResolutionReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Name != "claude" || tools[0].VersionChanged || tools[1].Name != "node" || !tools[1].VersionChanged || tools[1].ResolvedVersions[0] != "22.23.3" {
		t.Fatalf("incorrect native plan: %+v", tools)
	}
	for _, bad := range []string{`null`, `{}`, `[{"name":"node","backend":"core:node"}]`, `[] trailing`, `[{"name":"node\nsecret","backend":"core:node","new_versions":["22.0.0"]}]`, `[{"name":"node","backend":"https://registry/?token=secret","new_versions":["22.0.0"]}]`, `[{"name":"node","backend":"core:node","new_versions":["22.0.0"]},{"name":"node","backend":"core:node","new_versions":["22.1.0"]}]`} {
		if _, err := parseMiseResolutionReport(bad); err == nil {
			t.Fatalf("accepted malformed report %q", bad)
		}
	}
	if tools, err := parseMiseResolutionReport(`[]`); err != nil || tools == nil || len(tools) != 0 {
		t.Fatalf("empty valid report: %+v %v", tools, err)
	}
}

func TestMisePlanDigestsBindAllFilesAndSelectors(t *testing.T) {
	before := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "native", ".mise/locks/tool/package.json": "original"}}
	after := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{".mise/locks/tool/package.json": "original", "mise.lock": "native"}}
	if miseLockDigest(before) != miseLockDigest(after) {
		t.Fatal("map ordering changed the digest")
	}
	after.Files[".mise/locks/tool/package.json"] = "updated dependency tree"
	if miseLockDigest(before) == miseLockDigest(after) {
		t.Fatal("auxiliary dependency change escaped digest")
	}
	if miseLockDigest(nil) != "" {
		t.Fatal("missing old lock must have no digest")
	}
	pin := miseSelectorsDigest(map[string]string{"node": "22.0.0"})
	floating := miseSelectorsDigest(map[string]string{"node": "22"})
	if pin == floating || !strings.HasPrefix(pin, "sha256:") || len(pin) != 71 {
		t.Fatal("selector binding missing")
	}
}

func TestResolverJSONIsSeparateFromBoundedDiagnostics(t *testing.T) {
	var framed bytes.Buffer
	writeFrame := func(stream byte, payload string) {
		header := make([]byte, 8)
		header[0] = stream
		binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
		framed.Write(header)
		framed.WriteString(payload)
	}
	writeFrame(byte(stdcopy.Stderr), "mise lock progress\n")
	writeFrame(byte(stdcopy.Stdout), "[]")
	out := &resolverOutput{limit: 64 << 10}
	if err := copyResolverOutput(out, &framed); err != nil || out.String() != "[]" {
		t.Fatalf("diagnostics contaminated JSON: %q %v", out.String(), err)
	}
	writeFrame(byte(stdcopy.Stderr), strings.Repeat("x", (8<<10)+1))
	if err := copyResolverOutput(&resolverOutput{limit: 64 << 10}, &framed); err == nil {
		t.Fatal("unbounded diagnostics accepted")
	}
}
