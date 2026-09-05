package docker

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// The crew HostConfig asks every runtime for the same hardening, and the
// runtimes do not all deliver it. A drop that nobody is told about is the
// failure mode this exists to close: on podman 4.9.3 the agent silently loses
// gid 1002, crew-shared memory reads start failing with EACCES, and the agent
// presents as having forgotten things rather than as lacking a group.
//
// Every entry is measured against a real daemon by the conformance harness,
// never read out of release notes.
func TestKnownRuntimeGaps(t *testing.T) {
	t.Parallel()

	t.Run("podman below 5 loses supplementary groups", func(t *testing.T) {
		t.Parallel()
		for _, v := range []string{"4.9.3", "4.0.0", "3.4.4"} {
			gaps := KnownRuntimeGaps(DetectResult{Runtime: "podman", Version: v})
			if len(gaps) == 0 {
				t.Fatalf("podman %s: no gap reported, but GroupAdd is measurably dropped there", v)
			}
			// The detail has to name the CONSEQUENCE, not just the field. An
			// operator reading "GroupAdd not honoured" has no way to connect it
			// to the memory failures they are actually seeing.
			if !strings.Contains(gaps[0].Detail, "crew-shared memory") {
				t.Errorf("podman %s gap detail does not say what breaks: %q", v, gaps[0].Detail)
			}
		}
	})

	// 6.0.2 was verified green end to end — id reports groups=1001,1002 — so
	// warning there would be a false alarm, and false alarms are how real ones
	// stop being read.
	t.Run("podman 5 and later reports nothing", func(t *testing.T) {
		t.Parallel()
		for _, v := range []string{"5.0.0", "6.0.2", "10.1.0"} {
			if gaps := KnownRuntimeGaps(DetectResult{Runtime: "podman", Version: v}); len(gaps) != 0 {
				t.Errorf("podman %s: reported %d gap(s), want none — it honours GroupAdd", v, len(gaps))
			}
		}
	})

	t.Run("docker and friends report nothing", func(t *testing.T) {
		t.Parallel()
		for _, rt := range []string{"docker", "colima", "orbstack", "rancher", "nerdctl", ""} {
			if gaps := KnownRuntimeGaps(DetectResult{Runtime: rt, Version: "28.0.4"}); len(gaps) != 0 {
				t.Errorf("runtime %q reported %d gap(s), want none", rt, len(gaps))
			}
		}
	})

	// An unparseable version must produce NO warning rather than a guessed one.
	// A wrong warning about a runtime that is in fact fine costs more than a
	// missing one: it sends an operator chasing a group problem they do not have.
	t.Run("an unreadable version yields no guess", func(t *testing.T) {
		t.Parallel()
		for _, v := range []string{"", "unknown", "v4.9.3", "podman-4"} {
			if gaps := KnownRuntimeGaps(DetectResult{Runtime: "podman", Version: v}); len(gaps) != 0 {
				t.Errorf("podman version %q: reported a gap from a version it cannot read", v)
			}
		}
	})
}

// The report is worth nothing if it is not emitted where somebody sees it.
func TestLogRuntimeGaps(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logRuntimeGaps(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
		DetectResult{Runtime: "podman", Version: "4.9.3"})
	out := buf.String()
	if !strings.Contains(out, "WARN") {
		t.Errorf("gap logged below WARN; an operator filtering to warnings would never see it: %s", out)
	}
	for _, want := range []string{"podman", "4.9.3", "GroupAdd", "crew-shared memory"} {
		if !strings.Contains(out, want) {
			t.Errorf("log line omits %q, so it cannot be acted on: %s", want, out)
		}
	}

	buf.Reset()
	logRuntimeGaps(slog.New(slog.NewTextHandler(&buf, nil)), DetectResult{Runtime: "docker", Version: "28.0.4"})
	if buf.Len() != 0 {
		t.Errorf("docker produced a gap warning: %s", buf.String())
	}
}

// Gap is serialised onto GET /api/v1/system/runtime, so its field names are a
// published wire contract rather than an internal detail. Renaming a field is
// now an API break, and the compiler cannot say so — this can.
func TestGapJSONFieldNames(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(KnownRuntimeGaps(DetectResult{Runtime: "podman", Version: "4.9.3"})[0])
	if err != nil {
		t.Fatalf("marshal gap: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal gap: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("gap serialises %d field(s) (%v); the wire shape is {control, detail}", len(got), got)
	}
	if got["control"] != "GroupAdd" {
		t.Errorf(`gap["control"] = %v, want "GroupAdd"`, got["control"])
	}
	if detail, _ := got["detail"].(string); !strings.Contains(detail, "crew-shared memory") {
		t.Errorf(`gap["detail"] = %q, want the consequence spelled out`, detail)
	}
}

// The registry and the conformance harness used to contradict each other. The
// registry recorded that podman below 5 cannot carry a bare numeric GID and
// that upgrading is the only remedy; the harness failed the nightly build over
// that same GID on a runner where it could not be otherwise. "Runtime
// Conformance" was red for days saying something the repo already knew and had
// written down. ClassifyConformance is the join that was missing.
func TestClassifyConformance(t *testing.T) {
	t.Parallel()

	podman4 := KnownRuntimeGaps(DetectResult{Runtime: "podman", Version: "4.9.3"})
	if len(podman4) == 0 {
		t.Fatal("fixture precondition: podman 4.9.3 must report a gap")
	}

	for _, tc := range []struct {
		name     string
		control  string
		honoured bool
		gaps     []Gap
		want     ConformanceVerdict
		wantGap  bool
	}{
		{
			name: "documented gap is expected, not a failure",
			// The whole point: this is the exact podman 4.9.3 GroupAdd case
			// that reddened the nightly job.
			control: "GroupAdd", honoured: false, gaps: podman4,
			want: ConformanceKnownGap, wantGap: true,
		},
		{
			name:    "an undocumented drop is still a regression",
			control: "PidsLimit", honoured: false, gaps: podman4,
			want: ConformanceRegression,
		},
		{
			name:    "a control nobody tracks is judged on the measurement alone",
			control: "", honoured: false, gaps: podman4,
			want: ConformanceRegression,
		},
		{
			name:    "honoured and untracked is simply honoured",
			control: "GroupAdd", honoured: true, gaps: nil,
			want: ConformanceHonoured,
		},
		{
			// A registry entry that outlived the defect is not harmless: it
			// feeds doctor and /system/runtime, so it tells operators their
			// agents cannot read memory when they can.
			name:    "honoured despite a recorded gap means the registry is stale",
			control: "GroupAdd", honoured: true, gaps: podman4,
			want: ConformanceStaleGap, wantGap: true,
		},
		{
			name:    "control matching ignores case and surrounding space",
			control: " groupadd ", honoured: false, gaps: podman4,
			want: ConformanceKnownGap, wantGap: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, gap := ClassifyConformance(tc.control, tc.honoured, tc.gaps)
			if got != tc.want {
				t.Errorf("ClassifyConformance(%q, %v) = %q, want %q", tc.control, tc.honoured, got, tc.want)
			}
			if tc.wantGap && gap.Control == "" {
				t.Error("no Gap returned; the caller cannot print the operator-facing detail")
			}
			if !tc.wantGap && gap.Control != "" {
				t.Errorf("unexpected Gap returned: %+v", gap)
			}
		})
	}
}
