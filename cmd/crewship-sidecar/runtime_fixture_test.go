//go:build crew_runtime_live

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"github.com/crewship-ai/crewship/internal/sidecar"
	"github.com/crewship-ai/crewship/internal/testutil/messagesmock"
)

// Populated only in an owned test executable via -ldflags -X. There is no
// production environment/configuration switch and no scenario file mount.
var fixtureScenarioBase64 string

func fixtureTransport(encoded string) (*messagesmock.Transport, error) {
	if len(encoded) > 256<<10 {
		return nil, fmt.Errorf("fixture scenario exceeds bound")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid fixture scenario encoding")
	}
	var cfg messagesmock.Config
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid fixture scenario configuration")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("fixture scenario has trailing data")
	}
	transport, err := messagesmock.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("fixture scenario refused")
	}
	return transport, nil
}

// The supervisor observes the serving child's actual wait status. A completion
// receipt cannot be emitted merely because a model response was served or a log
// marker appeared. Both script completion and a zero child exit are required.
func fixtureSupervisor(cmd *exec.Cmd, receiptDir string) int {
	reader, writer, err := os.Pipe()
	if err != nil {
		return 1
	}
	defer reader.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{writer}
	if err := cmd.Start(); err != nil {
		writer.Close()
		return 1
	}
	writer.Close()
	signals := make(chan os.Signal, 1)
	signal.Ignore(syscall.SIGHUP)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	done := make(chan struct{})
	defer close(done)
	defer signal.Stop(signals)
	go func() {
		select {
		case sig := <-signals:
			_ = cmd.Process.Signal(sig)
		case <-done:
		}
	}()
	// Drain concurrently so even a maximal bounded script cannot fill the pipe.
	payload := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(io.LimitReader(reader, 1<<20)); payload <- data }()
	err = cmd.Wait()
	data := <-payload
	if err != nil {
		return 1
	}
	// Non-serving modes (--version, --health-check, native helpers) produce no
	// script receipt; they still execute the exact production bootstrap.
	if len(data) == 0 {
		return 0
	}
	var evidence []messagesmock.Event
	if json.Unmarshal(data, &evidence) != nil || len(evidence) == 0 {
		return 1
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(fixtureScenarioBase64)))
	receipt := struct {
		ScenarioSHA256 string               `json:"scenario_sha256"`
		SupervisorPID  int                  `json:"supervisor_pid"`
		ChildPID       int                  `json:"child_pid"`
		ExitCode       int                  `json:"exit_code"`
		ScriptComplete bool                 `json:"script_complete"`
		Evidence       []messagesmock.Event `json:"evidence"`
	}{digest, os.Getpid(), cmd.Process.Pid, 0, true, evidence}
	encoded, _ := json.Marshal(receipt)
	// The serving sidecar runs as UID 1002. The agent (1001) cannot create,
	// overwrite, or alter its exclusive, mode-0600 receipt in sticky /tmp.
	path := receiptDir + "/crewship-runtime-fixture-" + digest + ".json"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 1
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return 1
	}
	fmt.Fprintln(os.Stderr, "RUNTIME_FIXTURE_COMPLETE")
	return 0
}

func TestMain(m *testing.M) {
	// These private production helpers use their original argv and stdin
	// protocol. Wrapping them as a serving child changes that protocol and
	// must never fabricate a fixture completion receipt for a helper.
	if fixtureProductionHelper(os.Args) {
		main()
		os.Exit(0)
	}
	if fixtureScenarioBase64 == "" {
		os.Exit(m.Run())
	}
	if len(os.Args) < 2 || os.Args[1] != "--runtime-fixture-child" {
		cmd := exec.Command(os.Args[0], append([]string{"--runtime-fixture-child"}, os.Args[1:]...)...)
		os.Exit(fixtureSupervisor(cmd, "/tmp"))
	}
	os.Args = append(os.Args[:1], os.Args[2:]...)
	transport, err := fixtureTransport(fixtureScenarioBase64)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	serving := false
	newServer = func(cfg sidecar.ServerConfig) *sidecar.Server {
		serving = true
		cfg.ProxyTransport = transport
		return sidecar.NewServer(cfg)
	}
	// Use the exact production bootstrap, including native launch, health,
	// fence, stdin credential/configuration and graceful shutdown handling.
	main()
	if serving && transport.Complete() != nil {
		fmt.Fprintln(os.Stderr, "RUNTIME_FIXTURE_INCOMPLETE")
		os.Exit(1)
	}
	if serving {
		receipt := os.NewFile(3, "fixture-completion")
		if receipt == nil || json.NewEncoder(receipt).Encode(transport.Evidence()) != nil {
			os.Exit(1)
		}
		if receipt.Close() != nil {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func fixtureProductionHelper(args []string) bool {
	return len(args) >= 2 && (args[1] == "--host-capture-probe-v1" || args[1] == "run-stop-process")
}

func TestFixtureProductionHelperDispatch(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"sidecar", "--host-capture-probe-v1"}, true},
		{[]string{"sidecar", "run-stop-process", "--input-fd", "0"}, true},
		{[]string{"sidecar"}, false},
		{[]string{"sidecar", "--runtime-fixture-child", "run-stop-process"}, false},
		{[]string{"sidecar", "--host-capture-probe-v1=other"}, false},
		{[]string{"sidecar", "--version"}, false},
	} {
		if got := fixtureProductionHelper(tc.args); got != tc.want {
			t.Errorf("helper dispatch %q = %v; want %v", tc.args, got, tc.want)
		}
	}
}

func TestFixtureTransportRejectsMalformedEmbeddedConfiguration(t *testing.T) {
	valid := `{"ExpectedKey":"synthetic-fixture-key","Model":"fixture-model","Script":[{"Role":"lead","MatchMessage":"FIXTURE_LEAD","Text":"done"}]}`
	for name, encoded := range map[string]string{
		"encoding":       "not-base64",
		"oversize":       strings.Repeat("A", (256<<10)+1),
		"unknown_field":  base64.StdEncoding.EncodeToString([]byte(`{"Unexpected":"synthetic-fixture-key"}`)),
		"trailing_data":  base64.StdEncoding.EncodeToString([]byte(valid + `{}`)),
		"missing_script": base64.StdEncoding.EncodeToString([]byte(`{"ExpectedKey":"synthetic-fixture-key","Model":"fixture-model"}`)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixtureTransport(encoded); err == nil || strings.Contains(err.Error(), "synthetic-fixture-key") {
				t.Fatalf("malformed scenario admitted or leaked synthetic key: %v", err)
			}
		})
	}
	if transport, err := fixtureTransport(base64.StdEncoding.EncodeToString([]byte(valid))); err != nil || transport == nil {
		t.Fatalf("valid owned scenario refused: %v", err)
	}
}

// A successful-looking completion payload must never hide a failed child.
func TestFixtureSupervisorRequiresSuccessfulChildExit(t *testing.T) {
	for _, tc := range []struct {
		name, shell string
		code        int
		receipt     bool
	}{
		{"successful_exit", `printf '[{"Accepted":true}]' >&3; exit 0`, 0, true},
		{"failed_exit_after_payload", `printf '[{"Accepted":true}]' >&3; exit 9`, 1, false},
		{"malformed_payload", `printf 'invalid' >&3; exit 0`, 1, false},
		{"non_serving_helper", `exit 0`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if got := fixtureSupervisor(exec.Command("sh", "-c", tc.shell), dir); got != tc.code {
				t.Fatalf("supervisor exit=%d want=%d", got, tc.code)
			}
			files, err := os.ReadDir(dir)
			if err != nil || (len(files) == 1) != tc.receipt {
				t.Fatalf("receipt=%v err=%v", files, err)
			}
			if tc.receipt {
				raw, err := os.ReadFile(dir + "/" + files[0].Name())
				var receipt map[string]any
				if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt["exit_code"] != float64(0) || receipt["script_complete"] != true {
					t.Fatalf("invalid successful-exit receipt: %v", err)
				}
				if info, err := files[0].Info(); err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("receipt permissions: %v", err)
				}
			}
		})
	}
}
