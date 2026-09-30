//go:build linux

package restrictedruntime

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNativeSecurityOptionsRejectDrift(t *testing.T) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, nativeSeccomp); err != nil {
		t.Fatal(err)
	}
	p := Plan{NativeSandbox: NativeSandboxFingerprint()}
	options := []string{"no-new-privileges", "apparmor=" + nativeAppArmorName, "seccomp=" + compact.String()}
	if !validSecurityOptions(p, options) {
		t.Fatal("exact native policy denied")
	}
	for _, mutated := range [][]string{options[:2], append(append([]string{}, options...), "seccomp=unconfined"), {"no-new-privileges", "apparmor=unconfined", options[2]}, {"no-new-privileges", options[1], "seccomp={}"}} {
		if validSecurityOptions(p, mutated) {
			t.Fatal("policy drift allowed")
		}
	}
	p.NativeSandbox = "foreign"
	if validSecurityOptions(p, options) {
		t.Fatal("foreign policy accepted")
	}
	if validSecurityOptions(Plan{}, options) {
		t.Fatal("native policy attached to normal plan")
	}
}

func TestNativeMarkerCannotBypassClosedBrokerProfile(t *testing.T) {
	for _, profile := range []string{"", "brokered-http-v1"} {
		if err := (Plan{Profile: profile, NativeSandbox: NativeSandboxFingerprint()}).validateNetwork(); err == nil {
			t.Fatalf("native sandbox admitted profile %q", profile)
		}
	}
}
