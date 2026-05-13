package main

import (
	"fmt"
	"strings"
)

func checkEgressTargets(def map[string]interface{}) []doctorCheck {
	targets, ok := def["egress_targets"].([]interface{})
	if !ok || len(targets) == 0 {
		// Empty list is OK for routines without http steps. Only
		// warn when http steps are declared but the list is empty.
		if hasHTTPStep(def) {
			return []doctorCheck{{
				Name:    "egress_allowlist",
				Level:   doctorWarn,
				Message: "DSL has http step(s) but egress_targets is empty",
				Hint:    "add the target hostnames to egress_targets so the runtime allowlist permits them",
			}}
		}
		return []doctorCheck{{Name: "egress_allowlist", Level: doctorOK, Message: "no http steps; allowlist not required"}}
	}
	// Collect every issue rather than returning on the first.
	// An operator iterating on a routine with both `*` and
	// `localhost` in the allowlist gets BOTH problems in a
	// single doctor pass — fewer round-trips while fixing.
	issues := make([]doctorCheck, 0, 2)
	for _, raw := range targets {
		host, _ := raw.(string)
		if host == "*" || host == "*.*" || host == "" {
			issues = append(issues, doctorCheck{
				Name:    "egress_allowlist",
				Level:   doctorWarn,
				Message: fmt.Sprintf("egress_targets contains wildcard %q", host),
				Hint:    "wildcards open the routine to SSRF; pin to specific hostnames",
			})
			continue
		}
		// strings.HasPrefix(host, "127.") catches the full IPv4
		// loopback /8 range — original "127.0.0.1" check missed
		// 127.0.0.2 and other valid loopback aliases.
		if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.") {
			issues = append(issues, doctorCheck{
				Name:    "egress_allowlist",
				Level:   doctorWarn,
				Message: fmt.Sprintf("egress_targets includes loopback host %q", host),
				Hint:    "remove loopback from production routines; it points at the agent container, not the operator's machine",
			})
		}
	}
	if len(issues) > 0 {
		return issues
	}
	return []doctorCheck{{
		Name:    "egress_allowlist",
		Level:   doctorOK,
		Message: fmt.Sprintf("%d target(s) declared, no wildcards or loopback", len(targets)),
	}}
}

func hasHTTPStep(def map[string]interface{}) bool {
	steps, ok := def["steps"].([]interface{})
	if !ok {
		return false
	}
	for _, raw := range steps {
		s, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if t, _ := s["type"].(string); t == "http" {
			return true
		}
	}
	return false
}
