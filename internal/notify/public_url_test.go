package notify

import "testing"

// The public URL is an instance-wide fact, and there are four production
// sites that build a Dispatcher (two API handlers, two in cmd_start). Making
// each pass it is four chances to forget, and a site that forgets does not
// fail — it silently emits relative links that no chat client can open.
//
// So NewDispatcher reads it once, the way NewFromEnv already resolves SMTP
// for the mailer, and WithPublicURL stays as the explicit override.

func TestNewDispatcher_PicksUpThePublicURLWithoutBeingAsked(t *testing.T) {
	t.Setenv(publicURLEnv, "https://crewship.example.com")
	d := NewDispatcher(staticLister{}, nil, nil, nil)
	if d.publicURL != "https://crewship.example.com" {
		t.Errorf("publicURL = %q, want it resolved from %s", d.publicURL, publicURLEnv)
	}
}

func TestNewDispatcher_TrimsAndToleratesUnset(t *testing.T) {
	t.Setenv(publicURLEnv, "  https://x.test  ")
	if got := NewDispatcher(staticLister{}, nil, nil, nil).publicURL; got != "https://x.test" {
		t.Errorf("publicURL = %q, want it trimmed", got)
	}
	t.Setenv(publicURLEnv, "")
	if got := NewDispatcher(staticLister{}, nil, nil, nil).publicURL; got != "" {
		t.Errorf("publicURL = %q, want empty when unset", got)
	}
}

func TestWithPublicURL_OverridesTheEnvironment(t *testing.T) {
	// Wiring code that has the value from config must win over the ambient
	// environment, or a YAML setting added later would be quietly ignored.
	t.Setenv(publicURLEnv, "https://from-env.test")
	d := NewDispatcher(staticLister{}, nil, nil, nil).WithPublicURL("https://from-config.test")
	if d.publicURL != "https://from-config.test" {
		t.Errorf("publicURL = %q, want the explicit override", d.publicURL)
	}
}
