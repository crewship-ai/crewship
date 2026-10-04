//go:build linux

package stagedstart

import (
	"crypto/rand"
	"net"
	"strings"
	"testing"
)

func TestWorkloadEnvironmentKeepsLibraryPathWithoutStartupInjection(t *testing.T) {
	for key, value := range map[string]string{"LD_LIBRARY_PATH": "/opt/fixture/lib", "LD_PRELOAD": "fixture-loader", "LD_AUDIT": "fixture-audit", "ENV": "fixture-shell", "BASH_ENV": "fixture-bash", "STAGED_VALUE": "retained"} {
		t.Setenv(key, value)
	}
	values := map[string]string{}
	for _, entry := range workloadEnv() {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if values["LD_LIBRARY_PATH"] != "/opt/fixture/lib" || values["STAGED_VALUE"] != "retained" {
		t.Fatal("ordinary workload environment changed")
	}
	for _, key := range []string{"LD_PRELOAD", "LD_AUDIT", "ENV", "BASH_ENV"} {
		if _, ok := values[key]; ok {
			t.Fatalf("startup injection key retained: %s", key)
		}
	}
}

func TestExchangeRejectsImpersonatingNonPID1Socket(t *testing.T) {
	socket := "@crewship-staged-test-" + rand.Text()
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, e := l.AcceptUnix()
		if e == nil {
			c.Close()
		}
	}()
	_, _, e = exchangeAt(socket, Request{Operation: "status", Challenge: "challenge"})
	if e == nil || !strings.Contains(e.Error(), "PID1 keeper") {
		t.Fatalf("impersonating peer admitted: %v", e)
	}
	<-done
}
