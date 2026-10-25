//go:build linux

package mooring

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPrivateDiagnosticSinkExactChunkBoundaries(t *testing.T) {
	for _, line := range privateDiagnosticLines {
		if len(line) > privateDiagnosticLineLimit {
			t.Fatal("literal exceeds fixed parser bound")
		}
		for cut := 0; cut <= len(line)+1; cut++ {
			s := newPrivateDiagnosticSink()
			input := line + "\n"
			_, _ = s.Write([]byte(input[:cut]))
			_, _ = s.Write([]byte(input[cut:]))
			s.finish()
			got, truncated := s.snapshot()
			if !reflect.DeepEqual(got, []string{line}) || truncated {
				t.Fatalf("known literal lost at cut %d", cut)
			}
		}
	}
}

func TestPrivateDiagnosticSinkDropsUnknownOversizeAndPartial(t *testing.T) {
	known := privateDiagnosticLines[15]
	cases := []string{
		"diagnostic-fixture-secret-NEVER-RETAIN\n",
		"prefix " + known + "\n",
		known + " diagnostic-fixture-secret\n",
		known + "\r\n",
		known + strings.Repeat("x", 1<<20) + "\n",
		strings.Repeat("x", 1<<20) + known + "\n",
		string([]byte{0xff, 0, '\n'}),
	}
	for i, input := range cases {
		s := newPrivateDiagnosticSink()
		_, _ = s.Write([]byte(input))
		_, _ = s.Write([]byte(known)) // No newline: never promote on finish.
		s.finish()
		_, _ = s.Write([]byte("\n" + known + "\n")) // No writes after drain.
		got, truncated := s.snapshot()
		if len(got) != 0 || truncated || s.position != 0 || s.candidates != 0 {
			t.Fatalf("invalid diagnostic retained in vector %d", i)
		}
	}
	// An oversized line cannot hide a valid later complete line or retain suffixes.
	s := newPrivateDiagnosticSink()
	_, _ = s.Write([]byte(known + strings.Repeat("x", 100) + "\n" + known + "\n"))
	s.finish()
	got, _ := s.snapshot()
	if !reflect.DeepEqual(got, []string{known}) {
		t.Fatal("newline recovery changed exact known event")
	}
	// State stores no raw byte slice/string. Retained payload is enum ordinals.
	typ := reflect.TypeOf(s).Elem()
	for i := range typ.NumField() {
		kind := typ.Field(i).Type.Kind()
		if kind == reflect.String || kind == reflect.Slice {
			t.Fatal("diagnostic state can retain raw input")
		}
	}
}

func TestPrivateDiagnosticSinkBoundedAndConcurrent(t *testing.T) {
	s := newPrivateDiagnosticSink()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_, _ = s.Write([]byte(privateDiagnosticLines[0] + "\n"))
				_, _ = s.snapshot()
			}
		}()
	}
	wg.Wait()
	s.finish()
	got, truncated := s.snapshot()
	if len(got) != privateDiagnosticRetained || !truncated {
		t.Fatal("finite retention bound not enforced")
	}
	for _, line := range got {
		if line != privateDiagnosticLines[0] {
			t.Fatal("unexpected retained enum")
		}
	}
	got[0] = "caller-modified"
	again, _ := s.snapshot()
	if again[0] != privateDiagnosticLines[0] {
		t.Fatal("snapshot aliases retained state")
	}
}

func TestPrivateDiagnosticRustAllowlistParity(t *testing.T) {
	raw, err := os.ReadFile("../../mooring/crates/mooring-collab/src/session_actor.rs")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`b"(mooring-diag-v1:[a-z_]+)\\n"`)
	found := map[string]bool{}
	for _, match := range pattern.FindAllSubmatch(raw, -1) {
		found[string(match[1])] = true
	}
	if len(found) != len(privateDiagnosticLines) {
		t.Fatal("Rust diagnostic literals differ from finite capture catalog")
	}
	for _, line := range privateDiagnosticLines {
		if !found[line] {
			t.Fatal("capture literal is not emitted by this Rust overlay")
		}
	}
}

// Physical inherited-FD peer only: no SQL, application, Saved or auth authority.
func TestPrivateDiagnosticInheritedChild(t *testing.T) {
	selected := false
	for _, arg := range os.Args {
		selected = selected || arg == "--private-diag-peer"
	}
	if !selected {
		return
	}
	f := os.NewFile(3, "diagnostic-fixture-rpc")
	conn, err := net.FileConn(f)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	rpc := &RPC{conn: conn}
	defer rpc.Close()
	if _, err = rpc.Read(); err != nil {
		t.Fatal(err)
	}
	ready, _ := json.Marshal(map[string]any{"type": "runtime_ready", "request_id": "host-start", "selected": sessionTestVersions, "selected_control_profile": PrivateSessionProfile})
	if err = rpc.Write(ready); err != nil {
		t.Fatal(err)
	}
	if _, err = rpc.Read(); err != nil {
		t.Fatal(err)
	}
	_, _ = os.Stderr.WriteString("diagnostic-fixture-secret-NEVER-RETAIN\n")
	_, _ = os.Stderr.WriteString("mooring-diag-v1:delivery_")
	_, _ = os.Stderr.WriteString("expired\n")
	_, _ = os.Stderr.WriteString(strings.Repeat("x", 1<<17) + "\n")
	_, _ = os.Stderr.WriteString("mooring-diag-v1:actor_fatal") // Unterminated.
	_, _ = os.Stdout.WriteString("diagnostic-fixture-stdout-NEVER-RETAIN\n")
	os.Exit(7)
}

func TestPrivateDiagnosticActualProcessDrainPreservesFirstError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPrivateDiagnosticInheritedChild$", "--", "--private-diag-peer")
	process, err := startRuntimeSelected(ctx, RuntimeConfig{Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}, cmd, true)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	// A live child has no complete snapshot; the observer returns without waiting.
	session := &SessionRuntime{process: process, err: io.EOF}
	if stages, truncated, captured := session.PrivateDiagnosticStages(); stages != nil || truncated || captured {
		t.Fatal("live child incorrectly reported as drained")
	}
	if err = process.RPC.Write([]byte(`{"type":"fixture-trigger"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	case <-ctx.Done():
		t.Fatal("fixture child did not terminate within existing test context")
	}
	// Deliberately model the existing EOF-first observation without replacing it.
	stages, truncated, captured := session.PrivateDiagnosticStages()
	if !captured || truncated || !reflect.DeepEqual(stages, []string{"mooring-diag-v1:delivery_expired"}) {
		t.Fatal("actual child diagnostic capture failed")
	}
	if session.Err() != io.EOF {
		t.Fatal("diagnostic observer changed first runtime failure")
	}
	var exit *exec.ExitError
	if !errors.As(process.Err(), &exit) || exit.ExitCode() != 7 {
		t.Fatal("original child result changed")
	}
	if cmd.Stdout != nil {
		t.Fatal("stdout capture was enabled")
	}
}
