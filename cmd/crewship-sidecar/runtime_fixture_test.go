//go:build crew_runtime_live

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
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

func TestMain(m *testing.M) {
	if fixtureScenarioBase64 == "" {
		os.Exit(m.Run())
	}
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
		fmt.Fprintln(os.Stderr, "RUNTIME_FIXTURE_COMPLETE")
	}
	os.Exit(0)
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
