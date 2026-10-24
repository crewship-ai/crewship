package mooring

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Bootstrap has a deliberately closed two-object shape. Do not let the JSON
// struct decoder silently accept duplicate or case-variant negotiation fields.
// This is not the semantic validator for subsequent private control traffic.
func validateRuntimeReady(raw []byte, expected RuntimeVersions, profile string) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	if !runtimeDelimiter(d, '{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		if name == "selected" {
			if !runtimeReadyVersions(d, expected) {
				return false
			}
			continue
		}
		var want string
		switch name {
		case "type":
			want = "runtime_ready"
		case "request_id":
			want = "host-start"
		case "selected_control_profile":
			if profile != PrivatePrepareProfile {
				return false
			}
			want = profile
		default:
			return false
		}
		value, err := d.Token()
		if err != nil || value != want {
			return false
		}
	}
	if !runtimeDelimiter(d, '}') || !seen["type"] || !seen["request_id"] || !seen["selected"] || (profile != "" && !seen["selected_control_profile"]) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func runtimeDelimiter(d *json.Decoder, want json.Delim) bool {
	token, err := d.Token()
	return err == nil && token == want
}

func runtimeReadyVersions(d *json.Decoder, expected RuntimeVersions) bool {
	if !runtimeDelimiter(d, '{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return false
		}
		seen[name] = true
		var want string
		switch name {
		case "protocol_version":
			want = expected.Protocol
		case "model_version":
			want = expected.Model
		case "engine_version":
			want = expected.Engine
		default:
			return false
		}
		value, err := d.Token()
		text, ok := value.(string)
		if err != nil || !ok || text != want || !runtimeVersionSyntax(text) {
			return false
		}
	}
	return runtimeDelimiter(d, '}') && len(seen) == 3
}

func runtimeVersionSyntax(text string) bool {
	if len(text) > 29 {
		return false
	}
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || len(part) > 9 || (len(part) > 1 && part[0] == '0') {
			return false
		}
		for _, digit := range []byte(part) {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}
