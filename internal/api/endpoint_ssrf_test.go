package api

import "testing"

// TestValidateEndpointURL_SSRF locks the create-time SSRF gate (#961): a
// literal cloud-metadata/link-local/reserved IP is refused at credential
// create; a literal RFC1918/loopback IP is ACCEPTED here (the private-egress
// decision is per-crew at run time, so refusing it at create would break the
// legitimate on-prem/LAN endpoint case); a public URL is accepted.
func TestValidateEndpointURL_SSRF(t *testing.T) {
	rejected := []string{
		"http://169.254.169.254/",           // AWS/GCP/Azure metadata
		"http://169.254.169.254:8080/v1",    // metadata with port
		"https://[::ffff:169.254.169.254]/", // IPv4-mapped metadata bypass
		"http://224.0.0.1/",                 // multicast
		"http://255.255.255.255/",           // broadcast
		"http://0.0.0.0/",                   // unspecified
	}
	for _, v := range rejected {
		if msg := validateEndpointURL(v); msg == "" {
			t.Errorf("validateEndpointURL(%q) = accepted, want rejected", v)
		}
	}

	accepted := []string{
		"http://192.168.1.222:11434/v1", // dev2 LAN Ollama — private, gated per-crew at run time
		"http://10.0.0.5:11434/v1",      // RFC1918
		"http://127.0.0.1:11434/v1",     // loopback
		"https://llm.example.com/v1",    // public host
		"http://host.docker.internal:11434/v1",
	}
	for _, v := range accepted {
		if msg := validateEndpointURL(v); msg != "" {
			t.Errorf("validateEndpointURL(%q) = rejected (%q), want accepted", v, msg)
		}
	}

	// The auth-JSON shape (#961 Feature A) must be gated on its inner baseURL.
	if msg := validateEndpointURL(`{"baseURL":"http://169.254.169.254/","apiKey":"x"}`); msg == "" {
		t.Error("JSON endpoint with metadata baseURL was accepted, want rejected")
	}
}
