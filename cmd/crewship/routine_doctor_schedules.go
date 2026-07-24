package main

import (
	"encoding/json"
	"fmt"
)

// checkScheduleCircuitBreaker surfaces schedule circuit-breaker state
// (#1405) in `routine doctor` — the same tripped/near-tripped signal
// `schedules list` shows in its FAILS/ENABLED columns, but folded into
// the preflight checklist so an operator running doctor before a
// deploy or an incident review sees "this routine's cron trigger is
// dark" without a separate `schedules list --slug` round-trip.
//
// One check row per schedule targeting this routine:
//   - disabled_reason == "circuit_breaker" → FAIL (the routine is not
//     running on its cron at all right now)
//   - consecutive_failures > 0 but not yet tripped → WARN (heading
//     toward a trip)
//   - otherwise → OK
//
// A workspace with zero schedules targeting the routine reports a
// single informational OK row rather than silently omitting the
// check — consistent with the other doctor checks always emitting at
// least one row.
func checkScheduleCircuitBreaker(client doctorHTTPGetter, ws, slug string) []doctorCheck {
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipeline-schedules", ws))
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil || resp == nil || resp.StatusCode != 200 {
		return []doctorCheck{{
			Name:    "schedule_circuit_breaker",
			Level:   doctorWarn,
			Message: "could not list schedules to check circuit-breaker state",
		}}
	}
	var rows []scheduleRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return []doctorCheck{{
			Name:    "schedule_circuit_breaker",
			Level:   doctorWarn,
			Message: "could not decode schedules response",
		}}
	}

	var matched []scheduleRow
	for _, s := range rows {
		if s.TargetPipelineSlug == slug {
			matched = append(matched, s)
		}
	}
	if len(matched) == 0 {
		return []doctorCheck{{
			Name:    "schedule_circuit_breaker",
			Level:   doctorOK,
			Message: "no cron schedules target this routine",
		}}
	}

	out := make([]doctorCheck, 0, len(matched))
	for _, s := range matched {
		maxFailures := s.MaxConsecutiveFailures
		if maxFailures <= 0 {
			maxFailures = 5
		}
		switch {
		case s.DisabledReason == "circuit_breaker":
			out = append(out, doctorCheck{
				Name:  "schedule_circuit_breaker",
				Level: doctorFail,
				Message: fmt.Sprintf("schedule %q disabled after %d straight failures (cron %s)",
					s.Name, s.ConsecutiveFailures, s.CronExpr),
				Hint: fmt.Sprintf("inspect the recent failed runs, fix the cause, then `crewship routine schedules enable %s`", s.ID),
			})
		case s.ConsecutiveFailures > 0:
			out = append(out, doctorCheck{
				Name:  "schedule_circuit_breaker",
				Level: doctorWarn,
				Message: fmt.Sprintf("schedule %q has %d/%d consecutive failures — trips and auto-disables at %d",
					s.Name, s.ConsecutiveFailures, maxFailures, maxFailures),
			})
		default:
			out = append(out, doctorCheck{
				Name:    "schedule_circuit_breaker",
				Level:   doctorOK,
				Message: fmt.Sprintf("schedule %q healthy (0 consecutive failures)", s.Name),
			})
		}
	}
	return out
}
