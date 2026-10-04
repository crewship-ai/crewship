//go:build linux

package stagedstart

import (
	"crypto/rand"
	"net"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
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

func TestPeerAdmissionReservesIndependentTrustedCapacity(t *testing.T) {
	slots := newPeerSlots()
	for _, cred := range []*unix.Ucred{nil, {Uid: 0}, {Uid: 1000}, {Uid: 1003}} {
		if _, ok := slots.reserve(cred); ok {
			t.Fatalf("unauthorized peer admitted: %+v", cred)
		}
	}
	var agents, controls []func()
	for i := 0; i < cap(slots.agent); i++ {
		release, ok := slots.reserve(&unix.Ucred{Uid: 1001})
		if !ok {
			t.Fatal("agent pool prematurely full")
		}
		agents = append(agents, release)
	}
	if _, ok := slots.reserve(&unix.Ucred{Uid: 1001}); ok {
		t.Fatal("agent overflow admitted")
	}
	for i := 0; i < cap(slots.control); i++ {
		release, ok := slots.reserve(&unix.Ucred{Uid: 1002})
		if !ok {
			t.Fatal("agent saturation denied trusted control")
		}
		controls = append(controls, release)
	}
	if _, ok := slots.reserve(&unix.Ucred{Uid: 1002}); ok {
		t.Fatal("control overflow admitted")
	}
	for _, release := range append(agents, controls...) {
		release()
	}
	for _, uid := range []uint32{1001, 1002} {
		release, ok := slots.reserve(&unix.Ucred{Uid: uid})
		if !ok {
			t.Fatalf("released UID%d capacity unavailable", uid)
		}
		release()
	}
}
