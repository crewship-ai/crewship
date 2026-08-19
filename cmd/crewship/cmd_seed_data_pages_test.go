package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/pages"
)

// Every demo payload must satisfy the schema its panel declares.
//
// This is the guard that keeps a broken seed from being discovered by whoever
// runs `dev.sh seed` and opens an empty page. The payloads are hand-written
// YAML against five JSON Schemas; nothing else in the build would notice a
// renamed key or a string where a number belongs, because the seeder learns
// about it only at runtime, from a 400 it prints to stderr and steps over.
func TestSeedPages_EveryDemoPayloadSatisfiesItsSchema(t *testing.T) {
	t.Parallel()

	if len(seeddata.Pages) == 0 {
		t.Fatal("no demo pages — the catalogue decoded to nothing")
	}
	for _, page := range seeddata.Pages {
		for _, panel := range page.Panels {
			if panel.Demo == nil {
				t.Errorf("%s/%s has no demo payload — a seeded panel with no data reads as "+
					"never_produced, which is how the app says nobody wired it up",
					page.Slug, panel.ID)
				continue
			}
			raw, err := json.Marshal(panel.Demo)
			if err != nil {
				t.Errorf("%s/%s: demo payload is not JSON-encodable: %v", page.Slug, panel.ID, err)
				continue
			}
			if _, err := pages.ValidatePayload(pages.PanelSchema(panel.Schema), raw); err != nil {
				t.Errorf("%s/%s: demo payload does not satisfy %s: %v",
					page.Slug, panel.ID, panel.Schema, err)
			}
		}
	}
}

// The spec half has to be valid too — an owner crew that the seed does not
// create, or an SLA the parser refuses, fails at seed time as a 400 nobody
// reads.
func TestSeedPages_EverySpecFieldIsWellFormed(t *testing.T) {
	t.Parallel()

	crews := map[string]bool{}
	for _, c := range seeddata.Crews {
		crews["crew/"+c.Slug] = true
	}

	for _, page := range seeddata.Pages {
		if page.Slug == "" || page.Name == "" {
			t.Errorf("page %+v has no slug or no name", page)
		}
		if len(page.Panels) == 0 {
			t.Errorf("page %s has no panels", page.Slug)
		}
		for _, panel := range page.Panels {
			if _, err := seedPageSLASeconds(panel.SLA); err != nil {
				t.Errorf("%s/%s: %v", page.Slug, panel.ID, err)
			}
			if !pages.PanelSchema(panel.Schema).Producible() {
				t.Errorf("%s/%s declares schema %q, which this build cannot produce",
					page.Slug, panel.ID, panel.Schema)
			}
			// The owner must be a crew the seed itself creates, or the page
			// create fails with an unresolvable reference.
			if !crews[panel.Owner] {
				t.Errorf("%s/%s is owned by %q, which crews.yaml does not create",
					page.Slug, panel.ID, panel.Owner)
			}
			// The seed pushes as the page's owner, and §7.1 rule 4 allows that
			// only for a script or webhook producer. A routine producer would
			// make the panel unfillable at seed time.
			kindStr, _, ok := strings.Cut(strings.TrimSpace(panel.Producer), "/")
			if !ok {
				t.Errorf("%s/%s: producer %q is not <kind>/<ref>", page.Slug, panel.ID, panel.Producer)
				continue
			}
			kind := pages.ProducerKind(kindStr)
			if kind != pages.ProducerScript && kind != pages.ProducerWebhook {
				t.Errorf("%s/%s is produced by a %s; the seed can only fill a script- or "+
					"webhook-produced panel", page.Slug, panel.ID, kind)
			}
			if panel.Icon != "" && !pages.PanelIcon(panel.Icon).Known() {
				t.Errorf("%s/%s declares icon %q, which is outside the closed set",
					page.Slug, panel.ID, panel.Icon)
			}
		}
	}
}

// A duration the seeder converts must round-trip to the integer the wire wants.
func TestSeedPageSLASeconds(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]int{"30s": 30, "60s": 60, "2m": 120, "1h": 3600, "12h": 43200} {
		got, err := seedPageSLASeconds(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "soon", "0s", "-5m"} {
		if _, err := seedPageSLASeconds(bad); err == nil {
			t.Errorf("%q was accepted as an SLA", bad)
		}
	}
	_ = time.Second
}
