//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeRunnerModeIdentityAndBootstrapBoundaries(t *testing.T) {
	oldArgs, oldInput := os.Args, os.Stdin
	defer func() { os.Args, os.Stdin = oldArgs, oldInput }()
	for _, args := range [][]string{{"runner"}, {"runner", "unknown"}, {"runner", "verify-project-inputs", "extra"}, {"runner", "project-input-hold", "extra"}, {"runner", "hold", "extra"}, {"runner", "lease", "extra"}, {"runner", "broker", "extra"}, {"runner", "launch", "extra"}, {"runner", "native"}, {"runner", "native", "bad/model"}} {
		os.Args = args
		if err := run(); err == nil {
			t.Fatalf("unexpected command accepted: %v", args)
		}
	}
	for _, mode := range []string{"verify-project-inputs", "project-input-hold", "hold", "lease", "broker", "launch", "native"} {
		uid := 1001
		if mode == "hold" || mode == "lease" || mode == "broker" {
			uid = 1002
		}
		if os.Getuid() == uid {
			continue
		}
		os.Args = []string{"runner", mode}
		if mode == "native" {
			os.Args = append(os.Args, "fixture-model")
		}
		if err := run(); err == nil || err.Error() != "identity" {
			t.Fatalf("uid %d admitted %s: %v", os.Getuid(), mode, err)
		}
	}
	invoke := func(mode, body string) error {
		t.Helper()
		file, err := os.CreateTemp(t.TempDir(), "stdin")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString(body); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		os.Stdin = file
		os.Args = []string{"runner", mode}
		return run()
	}
	if os.Getuid() == 1002 {
		if err := invoke("lease", "not JSON"); err == nil {
			t.Fatal("invalid lease admitted")
		}
		expired, _ := json.Marshal(time.Now().Add(-time.Hour))
		if err := invoke("lease", string(expired)); err == nil {
			t.Fatal("expired lease admitted")
		}
		if err := invoke("broker", ""); err == nil {
			t.Fatal("empty broker configuration admitted")
		}
	}
	if os.Getuid() != 1001 {
		return
	}
	if err := invoke("verify-project-inputs", `{"unknown":true}`); err == nil {
		t.Fatal("ambiguous input manifest admitted")
	}
	for _, body := range []string{"not JSON", `{"unknown":true}`, `{}`, `{"command":["/bin/sh","-c","true"],"env":{"a":"b","c":"d"}}`} {
		if err := invoke("launch", body); err == nil {
			t.Fatal("invalid bootstrap admitted")
		}
	}
	if _, err := os.Stat("/opt/crewship-native-runner"); os.IsNotExist(err) {
		raw, err := json.Marshal(map[string]any{"command": []string{"/opt/crewship-native-runner", "native", "fixture-model"}, "env": map[string]string{"CREWSHIP_BROKER_URL": "http://127.0.0.1:9121", "CREWSHIP_BROKER_TOKEN": strings.Repeat("a", 64)}})
		if err != nil {
			t.Fatal(err)
		}
		if err := invoke("launch", string(raw)); err == nil {
			t.Fatal("missing fixed executable replaced by a fallback")
		}
	}
	t.Setenv("CREWSHIP_BROKER_URL", "")
	os.Args = []string{"runner", "native", "fixture-model"}
	if err := run(); err == nil {
		t.Fatal("native worker accepted missing broker configuration")
	}
}

func TestNativeRevisionAndBrokerAdmission(t *testing.T) {
	t.Setenv("CREWSHIP_BROKER_URL", "")
	t.Setenv("CREWSHIP_BROKER_TOKEN", "")
	err := native(t.Context(), "fixture-model", io.Discard)
	if err == nil {
		t.Fatal("native execution accepted absent broker settings")
	}
	// This opt-in fixture is a new read-only, network-less container, with
	// /home/agent mounted as a fresh tmpfs. It carries the actual pinned binaries.
	if os.Getenv("CREWSHIP_TEST_NATIVE_SANDBOX") != "1" {
		return
	}
	if err.Error() != "broker policy" {
		t.Fatalf("pinned executable verification failed: %v", err)
	}
	for _, path := range []string{"/home/agent/work", "/home/agent/.codex"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("acceptance scratch path already exists: %s", path)
		}
	}
	t.Setenv("CREWSHIP_BROKER_URL", "http://127.0.0.1:9121")
	t.Setenv("CREWSHIP_BROKER_TOKEN", strings.Repeat("b", 64))
	if err := os.Mkdir("/home/agent/work", 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove("/home/agent/work"); _ = os.Remove("/home/agent/.codex") })
	if err := native(t.Context(), "fixture-model", io.Discard); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing workspace reused: %v", err)
	}
	if err := os.Remove("/home/agent/work"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := native(ctx, "fixture-model", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled native process admitted: %v", err)
	}
}
