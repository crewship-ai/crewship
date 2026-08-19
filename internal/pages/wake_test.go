package pages

import (
	"strings"
	"testing"
	"time"
)

func statusPayload(states ...StatusState) *StatusPayload {
	p := &StatusPayload{}
	for i, s := range states {
		p.Items = append(p.Items, StatusItem{Name: string(rune('a'+i)) + "-svc", State: s})
	}
	return p
}

func metricPayload(v *float64) *MetricPayload { return &MetricPayload{Value: v} }

func wakeNum(v float64) *float64 { return &v }

func TestParseWakePredicate(t *testing.T) {
	tests := []struct {
		name    string
		schema  PanelSchema
		when    string
		wantErr string
	}{
		{name: "the PRD's own example", schema: SchemaStatus, when: `any(state == "critical")`},
		{name: "single quotes", schema: SchemaStatus, when: `any(state == 'warning')`},
		{name: "all clear", schema: SchemaStatus, when: `all(state == "ok")`},
		{name: "not ok", schema: SchemaStatus, when: `any(state != "ok")`},
		{name: "untidy spacing", schema: SchemaStatus, when: `  any( state=="critical" )  `},
		{name: "metric over", schema: SchemaMetric, when: `value > 90`},
		{name: "metric at or under", schema: SchemaMetric, when: `value <= 0.5`},
		{name: "metric negative bound", schema: SchemaMetric, when: `value < -3`},

		{
			name: "empty", schema: SchemaStatus, when: "  ",
			wantErr: "declares no `when`",
		},
		{
			name:   "a state predicate on a metric panel can never match",
			schema: SchemaMetric, when: `any(state == "critical")`,
			wantErr: "could never match",
		},
		{
			name:   "a value predicate on a status panel can never match",
			schema: SchemaStatus, when: `value > 1`,
			wantErr: "could never match",
		},
		{
			name: "a state outside the closed set", schema: SchemaStatus, when: `any(state == "crticial")`,
			wantErr: "is not a status.v1 state",
		},
		{
			name: "an unknown field", schema: SchemaStatus, when: `any(label == "x")`,
			wantErr: `reads no field called "label"`,
		},
		{
			name: "ordering a word", schema: SchemaStatus, when: `any(state > "ok")`,
			wantErr: "compares with == or !=",
		},
		{
			name: "no comparison at all", schema: SchemaStatus, when: `any(state)`,
			wantErr: "there is no comparison in it",
		},
		{
			name: "a general expression is refused", schema: SchemaMetric,
			when:    `value > 90 and value < 100`,
			wantErr: "is not one",
		},
		{
			name: "an unknown bare field", schema: SchemaMetric, when: `errors > 3`,
			wantErr: `there is no field called "errors"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseWakePredicate(tc.schema, tc.when)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseWakePredicate(%q) = %v, want a predicate", tc.when, err)
				}
				if p.String() != strings.TrimSpace(tc.when) {
					t.Errorf("String() = %q, want the sentence as authored %q", p.String(), strings.TrimSpace(tc.when))
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseWakePredicate(%q) accepted it; want %q", tc.when, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestWakePredicateEval(t *testing.T) {
	tests := []struct {
		name    string
		schema  PanelSchema
		when    string
		payload Payload
		want    bool
	}{
		{name: "any critical, one is", schema: SchemaStatus, when: `any(state == "critical")`,
			payload: statusPayload(StatusOK, StatusCritical), want: true},
		{name: "any critical, none is", schema: SchemaStatus, when: `any(state == "critical")`,
			payload: statusPayload(StatusOK, StatusWarning), want: false},
		{name: "any critical over no items", schema: SchemaStatus, when: `any(state == "critical")`,
			payload: statusPayload(), want: false},
		{name: "all ok, all are", schema: SchemaStatus, when: `all(state == "ok")`,
			payload: statusPayload(StatusOK, StatusOK), want: true},
		{name: "all ok, one is not", schema: SchemaStatus, when: `all(state == "ok")`,
			payload: statusPayload(StatusOK, StatusWarning), want: false},
		{name: "all ok over no items is not vacuously true", schema: SchemaStatus, when: `all(state == "ok")`,
			payload: statusPayload(), want: false},
		{name: "any not-ok", schema: SchemaStatus, when: `any(state != "ok")`,
			payload: statusPayload(StatusOK, StatusWarning), want: true},

		{name: "metric over the bound", schema: SchemaMetric, when: `value > 90`,
			payload: metricPayload(wakeNum(91)), want: true},
		{name: "metric exactly at the bound", schema: SchemaMetric, when: `value > 90`,
			payload: metricPayload(wakeNum(90)), want: false},
		{name: "metric at an inclusive bound", schema: SchemaMetric, when: `value >= 90`,
			payload: metricPayload(wakeNum(90)), want: true},
		{name: "a measured zero is data", schema: SchemaMetric, when: `value <= 0`,
			payload: metricPayload(wakeNum(0)), want: true},
		{name: "a null metric wakes nobody", schema: SchemaMetric, when: `value <= 0`,
			payload: metricPayload(nil), want: false},
		{name: "the wrong payload shape wakes nobody", schema: SchemaMetric, when: `value > 1`,
			payload: statusPayload(StatusCritical), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseWakePredicate(tc.schema, tc.when)
			if err != nil {
				t.Fatalf("ParseWakePredicate: %v", err)
			}
			if got := p.Eval(tc.payload); got != tc.want {
				t.Errorf("Eval(%s) = %v, want %v", tc.when, got, tc.want)
			}
		})
	}
}

func TestCompileWakeGates(t *testing.T) {
	base := PanelSpec{ID: "sluzby", Schema: SchemaStatus, Owner: "crew/ops", Producer: "script/scrape", SLA: "60s"}

	t.Run("a panel with no gates compiles to nothing", func(t *testing.T) {
		gates, err := CompileWakeGates(base)
		if err != nil || gates != nil {
			t.Fatalf("CompileWakeGates = %v, %v; want no gates and no error", gates, err)
		}
	})

	t.Run("the PRD's gate", func(t *testing.T) {
		p := base
		p.Wake = []PanelWake{{When: `any(state == "critical")`, For: "5m", Agent: "crew/devops", Writes: "incident"}}
		gates, err := CompileWakeGates(p)
		if err != nil {
			t.Fatalf("CompileWakeGates: %v", err)
		}
		if len(gates) != 1 {
			t.Fatalf("got %d gates, want 1", len(gates))
		}
		g := gates[0]
		switch {
		case g.Index != 1:
			t.Errorf("Index = %d, want 1", g.Index)
		case g.For != 5*time.Minute:
			t.Errorf("For = %s, want 5m", g.For)
		case g.CrewSlug != "devops":
			t.Errorf("CrewSlug = %q, want devops", g.CrewSlug)
		case g.Writes != "incident":
			t.Errorf("Writes = %q, want incident", g.Writes)
		case g.PayloadKey() != "wake_1":
			t.Errorf("PayloadKey = %q, want wake_1", g.PayloadKey())
		}
	})

	t.Run("two gates get two identities", func(t *testing.T) {
		p := base
		p.Wake = []PanelWake{
			{When: `any(state == "critical")`, Agent: "crew/devops"},
			{When: `all(state == "ok")`, Agent: "crew/ops"},
		}
		gates, err := CompileWakeGates(p)
		if err != nil {
			t.Fatalf("CompileWakeGates: %v", err)
		}
		if gates[0].PayloadKey() == gates[1].PayloadKey() {
			t.Fatalf("both gates claim the payload key %q; two gates on one panel must be two rules",
				gates[0].PayloadKey())
		}
	})

	errCases := []struct {
		name    string
		wake    []PanelWake
		wantErr string
	}{
		{
			name:    "no agent",
			wake:    []PanelWake{{When: `any(state == "critical")`}},
			wantErr: "must be crew/<slug>",
		},
		{
			name:    "an agent that is not a crew",
			wake:    []PanelWake{{When: `any(state == "critical")`, Agent: "agent/scout"}},
			wantErr: "must be crew/<slug>",
		},
		{
			name:    "a for that is not a duration",
			wake:    []PanelWake{{When: `any(state == "critical")`, For: "soon", Agent: "crew/ops"}},
			wantErr: "is not a duration",
		},
		{
			name:    "a for past the ceiling",
			wake:    []PanelWake{{When: `any(state == "critical")`, For: "48h", Agent: "crew/ops"}},
			wantErr: "on_failure's job",
		},
		{
			name: "more gates than the cap",
			wake: []PanelWake{
				{When: `any(state == "critical")`, Agent: "crew/ops"},
				{When: `any(state == "warning")`, Agent: "crew/ops"},
				{When: `all(state == "ok")`, Agent: "crew/ops"},
				{When: `any(state != "ok")`, Agent: "crew/ops"},
				{When: `all(state != "critical")`, Agent: "crew/ops"},
			},
			wantErr: "the cap is 4",
		},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Wake = tc.wake
			if _, err := CompileWakeGates(p); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("CompileWakeGates = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestOnFailureCrewSlug(t *testing.T) {
	tests := []struct {
		name    string
		in      *PanelOnFailure
		want    string
		wantErr string
	}{
		{name: "absent", in: nil},
		{name: "a crew", in: &PanelOnFailure{Issue: "crew/ops"}, want: "ops"},
		{name: "empty block", in: &PanelOnFailure{}, wantErr: "declares nothing"},
		{name: "a user", in: &PanelOnFailure{Issue: "user/pavel"}, wantErr: "must be crew/<slug>"},
		{name: "a bare slug", in: &PanelOnFailure{Issue: "ops"}, wantErr: "must be crew/<slug>"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OnFailureCrewSlug(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("OnFailureCrewSlug = %v, want an error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("OnFailureCrewSlug: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateGates(t *testing.T) {
	doc := func(panels ...PanelSpec) *Document {
		return &Document{
			APIVersion: DocumentAPIVersion,
			Kind:       DocumentKind,
			Metadata:   Metadata{Name: "Ops", Slug: "ops"},
			Spec:       Spec{Panels: panels},
		}
	}
	sensor := PanelSpec{ID: "sluzby", Schema: SchemaStatus, Owner: "crew/ops", Producer: "script/scrape", SLA: "60s"}
	target := PanelSpec{ID: "incident", Schema: SchemaNarrative, Owner: "crew/ops", Producer: "agent/scout", SLA: "1h"}

	t.Run("a page with no gates passes", func(t *testing.T) {
		if err := ValidateGates(doc(sensor, target)); err != nil {
			t.Fatalf("ValidateGates: %v", err)
		}
	})

	t.Run("writes must name a panel on this page", func(t *testing.T) {
		s := sensor
		s.Wake = []PanelWake{{When: `any(state == "critical")`, Agent: "crew/devops", Writes: "incidnet"}}
		err := ValidateGates(doc(s, target))
		if err == nil || !strings.Contains(err.Error(), "not a panel on this page") {
			t.Fatalf("ValidateGates = %v, want a refusal naming the typo'd panel", err)
		}
		var ve *ValidationError
		if !asValidationError(err, &ve) || ve.Code != CodeInvalidSpec {
			t.Fatalf("error = %#v, want a CodeInvalidSpec ValidationError", err)
		}
	})

	t.Run("writes naming a real panel passes", func(t *testing.T) {
		s := sensor
		s.Wake = []PanelWake{{When: `any(state == "critical")`, For: "5m", Agent: "crew/devops", Writes: "incident"}}
		if err := ValidateGates(doc(s, target)); err != nil {
			t.Fatalf("ValidateGates: %v", err)
		}
	})

	t.Run("an on_failure that names no crew is refused", func(t *testing.T) {
		s := sensor
		s.OnFailure = &PanelOnFailure{Issue: "ops"}
		if err := ValidateGates(doc(s)); err == nil {
			t.Fatal("ValidateGates accepted on_failure: {issue: ops}")
		}
	})
}

// asValidationError is errors.As without the import churn in a table test.
func asValidationError(err error, target **ValidationError) bool {
	ve, ok := err.(*ValidationError)
	if ok {
		*target = ve
	}
	return ok
}

func TestWakeHeldFor(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	tests := []struct {
		name    string
		samples []WakeSample
		hold    time.Duration
		want    bool
	}{
		{
			name:    "no history at all",
			samples: nil, hold: 0, want: false,
		},
		{
			name:    "the newest push does not match",
			samples: []WakeSample{{ProducedAt: now, Matched: false}, {ProducedAt: ago(time.Hour), Matched: true}},
			hold:    0, want: false,
		},
		{
			name:    "no hold window fires on the first match",
			samples: []WakeSample{{ProducedAt: now, Matched: true}},
			hold:    0, want: true,
		},
		{
			name: "one bad scrape wakes nobody",
			samples: []WakeSample{
				{ProducedAt: now, Matched: true},
				{ProducedAt: ago(30 * time.Second), Matched: false},
				{ProducedAt: ago(time.Hour), Matched: true},
			},
			hold: 5 * time.Minute, want: false,
		},
		{
			name: "one nanosecond short of the window",
			samples: []WakeSample{
				{ProducedAt: now, Matched: true},
				{ProducedAt: ago(5*time.Minute - time.Nanosecond), Matched: true},
				{ProducedAt: ago(6 * time.Minute), Matched: false},
			},
			hold: 5 * time.Minute, want: false,
		},
		{
			name: "4m59s does not fire a 5m gate",
			samples: []WakeSample{
				{ProducedAt: now, Matched: true},
				{ProducedAt: ago(4*time.Minute + 59*time.Second), Matched: true},
				{ProducedAt: ago(10 * time.Minute), Matched: false},
			},
			hold: 5 * time.Minute, want: false,
		},
		{
			name: "exactly at the window fires, as the SLA boundary does",
			samples: []WakeSample{
				{ProducedAt: now, Matched: true},
				{ProducedAt: ago(5 * time.Minute), Matched: true},
				{ProducedAt: ago(10 * time.Minute), Matched: false},
			},
			hold: 5 * time.Minute, want: true,
		},
		{
			name: "a ring that matches all the way back proves only as far as it reaches",
			samples: []WakeSample{
				{ProducedAt: now, Matched: true},
				{ProducedAt: ago(2 * time.Minute), Matched: true},
			},
			hold: time.Hour, want: false,
		},
		{
			name: "a single push cannot satisfy a hold window",
			samples: []WakeSample{
				{ProducedAt: now, Matched: true},
			},
			hold: 5 * time.Minute, want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WakeHeldFor(tc.samples, tc.hold, now); got != tc.want {
				t.Errorf("WakeHeldFor(hold=%s) = %v, want %v", tc.hold, got, tc.want)
			}
		})
	}
}
