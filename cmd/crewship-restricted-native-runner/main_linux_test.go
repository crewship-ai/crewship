//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestNativeCommandPinsSandboxAndClosedProvider(t *testing.T) {
	args := strings.Join(nativeArgs("gpt-5-mini"), "\n")
	for _, required := range []string{`default_permissions="crewship_native"`, `permissions.crewship_native.network.enabled=false`, `permissions.crewship_native.extends=":workspace"`, `permissions.crewship_native.filesystem={":root"="deny"`, "approval_policy=\"never\"", "web_search=\"disabled\"", "supports_websockets=false", "request_max_retries=0", "stream_max_retries=0", "http://127.0.0.1:9121/v1"} {
		if !strings.Contains(args, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"--sandbox", "danger-full-access", "--yolo", "auth.json", "OPENAI_API_KEY", "require_escalated"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("unsafe command %s", forbidden)
		}
	}
}
