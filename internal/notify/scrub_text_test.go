package notify

import (
	"strings"
	"testing"
)

// #1518 removed the agent-send handler's own title scrub, on the reasoning
// that DeliverCategoryMessage now redacts the whole envelope. That was true
// and beside the point: DeliverCategoryMessage takes CategoryMessage BY
// VALUE, so scrubMessage mutates a copy. The caller still holds the raw title
// and writes it to the Activity timeline afterwards.
//
// Delivery is one way out of the instance. The journal is another — it is
// rendered in the UI, exported, and captured in backups. A redaction that
// covers one egress and is described as covering "the envelope" invites
// exactly this mistake.

func TestScrubText_RedactsASecretAndLeavesOrdinaryTextAlone(t *testing.T) {
	const secret = "sk-ant-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	got := ScrubText("Deploy failed: token " + secret + " rejected")
	if strings.Contains(got, secret) {
		t.Errorf("the secret survived: %q", got)
	}
	if !strings.Contains(got, "[REDACTED") {
		t.Errorf("nothing was redacted: %q", got)
	}
	if !strings.HasPrefix(got, "Deploy failed: token ") {
		t.Errorf("surrounding text should be preserved: %q", got)
	}
}

func TestScrubText_EmptyStaysEmpty(t *testing.T) {
	if got := ScrubText(""); got != "" {
		t.Errorf("ScrubText(\"\") = %q", got)
	}
}

func TestScrubText_ReusesOneScrubber(t *testing.T) {
	// The version this replaces built a fresh scrubber — seventeen compiled
	// patterns — on every call, on a path that runs per delivery.
	first, second := scrubberOnce(), scrubberOnce()
	if first != second {
		t.Error("ScrubText must reuse a single scrubber rather than compiling per call")
	}
}
