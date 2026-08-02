package api

import (
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/keeper/health"
)

// AdminKeeperHealthHandler answers "is the judge behaving?" on demand.
//
//	GET /api/v1/admin/keeper/health → the rolling decision window
//
// The metric itself has been on the credential path since the decision monitor
// landed: every verdict updates a window, and an alarm fires into the inbox when
// the picture collapses. What was missing was the other half. An operator could
// be PAGED and could not LOOK — which is a strange shape for a feature whose
// whole purpose is catching degradation that is invisible from the outside. The
// #1624 outage ran for milestones precisely because a fail-closed DENY and a
// considered DENY are the same response.
//
// So this returns the same numbers the alarm decides on, and the alarm itself
// when one is standing. Read-only and in-memory: it touches no database and is
// safe on a live server.
type AdminKeeperHealthHandler struct {
	logger *slog.Logger
}

func NewAdminKeeperHealthHandler(logger *slog.Logger) *AdminKeeperHealthHandler {
	return &AdminKeeperHealthHandler{logger: logger}
}

type keeperHealthAlarm struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	At      string `json:"at,omitempty"`
}

// keeperHealthResponse is the window in the form an operator reads it.
//
// Both the counts and the rates are sent. The rates are what the thresholds
// compare, and the counts are what make a rate honest: "0% allowed" over four
// decisions is not the same claim as over four hundred, and a caller given only
// the percentage cannot tell those apart.
type keeperHealthResponse struct {
	WorkspaceID string `json:"workspace_id"`

	Samples       int `json:"samples"`
	Allow         int `json:"allow"`
	Deny          int `json:"deny"`
	Escalate      int `json:"escalate"`
	JudgeFailures int `json:"judge_failures"`

	AllowRate    float64 `json:"allow_rate"`
	DenyRate     float64 `json:"deny_rate"`
	EscalateRate float64 `json:"escalate_rate"`
	// ProgressedRate is granted OR escalated — the share the collapse alarm
	// actually reads. Deliberately not the ALLOW share: the tier policy converts
	// every ALLOW into an ESCALATE at a human-approval tier, so an all-L4
	// workspace runs at an allow rate of exactly zero while working perfectly.
	ProgressedRate   float64 `json:"progressed_rate"`
	JudgeFailureRate float64 `json:"judge_failure_rate"`

	P95LatencyMS int64 `json:"p95_latency_ms"`

	// Thresholds travel with the numbers so a reader can see how close they are
	// without knowing the constants by heart, and so a report cannot drift from
	// the code that raises on it.
	MinSamples            int     `json:"min_samples"`
	AlarmProgressedRate   float64 `json:"alarm_progressed_rate"`
	AlarmJudgeFailureRate float64 `json:"alarm_judge_failure_rate"`

	// Alarm is nil when nothing is standing. It is NOT "healthy" — a window with
	// too few samples to judge also has no alarm, and Samples is what tells the
	// two apart.
	Alarm *keeperHealthAlarm `json:"alarm,omitempty"`

	Oldest string `json:"oldest,omitempty"`
	Newest string `json:"newest,omitempty"`
}

func (h *AdminKeeperHealthHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		// RFC 7807, matching the rest of the admin surface. An operator scripting
		// against this endpoint should not have to branch on which shape a given
		// route happens to answer with.
		writeProblem(w, r, http.StatusBadRequest, "workspace is required")
		return
	}

	// A workspace nobody has decided anything for yet is an empty window, not an
	// error and not a healthy one. Zero samples is its own answer, and the
	// response says so by carrying the count rather than an "ok" flag.
	s, _ := health.Default.Snapshot(wsID)

	out := keeperHealthResponse{
		WorkspaceID:           wsID,
		Samples:               s.Samples,
		Allow:                 s.Allow,
		Deny:                  s.Deny,
		Escalate:              s.Escalate,
		JudgeFailures:         s.JudgeFailures,
		AllowRate:             s.AllowRate(),
		DenyRate:              s.DenyRate(),
		EscalateRate:          s.EscalateRate(),
		ProgressedRate:        s.ProgressedRate(),
		JudgeFailureRate:      s.JudgeFailureRate(),
		P95LatencyMS:          s.P95Latency.Milliseconds(),
		MinSamples:            health.MinSamples,
		AlarmProgressedRate:   health.AlarmAllowRate,
		AlarmJudgeFailureRate: health.AlarmJudgeFailureRate,
	}
	if !s.Oldest.IsZero() {
		out.Oldest = s.Oldest.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !s.Newest.IsZero() {
		out.Newest = s.Newest.UTC().Format("2006-01-02T15:04:05Z")
	}
	if a, ok := s.Alarm(); ok {
		out.Alarm = &keeperHealthAlarm{Kind: string(a.Kind), Summary: a.Summary}
		if !a.At.IsZero() {
			out.Alarm.At = a.At.UTC().Format("2006-01-02T15:04:05Z")
		}
	}

	writeJSON(w, http.StatusOK, out)
}
