package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// RoutineCalendarICS answers the window of RoutineCalendar as an iCalendar
// file, for the Calendar's .ics button and for calendar apps. Same window
// rules (RFC3339 from/to, at most 32 days), same role (member read), same
// events; only the format differs.
// GET /api/v1/workspaces/{workspaceId}/pipelines/calendar.ics
func (h *PipelineHandler) RoutineCalendarICS(w http.ResponseWriter, r *http.Request) {
	start, end, ok := calendarWindow(w, r)
	if !ok {
		return
	}
	resp, status, msg := h.routineCalendarEvents(r.Context(), WorkspaceIDFromContext(r.Context()), start, end)
	if status != http.StatusOK {
		replyError(w, status, msg)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="crewship-calendar.ics"`)
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write([]byte(renderICS(resp.Events, time.Now().UTC())))
}

const icsStamp = "20060102T150405Z"

// renderICS writes the events as one VCALENDAR (RFC 5545): CRLF line ends,
// text escaped, lines folded at 75 octets.
func renderICS(events []routineCalendarEvent, now time.Time) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(foldICSLine(s)); b.WriteString("\r\n") }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Crewship//Routine calendar//EN")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	for _, e := range events {
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			continue // a row whose time cannot be read has no place on a calendar
		}
		at = at.UTC()
		desc := icsDescription(e)
		line("BEGIN:VEVENT")
		line("UID:" + icsEscape(e.ID) + "@crewship")
		line("DTSTAMP:" + now.Format(icsStamp))
		line("DTSTART:" + at.Format(icsStamp))
		line("DTEND:" + at.Add(15*time.Minute).Format(icsStamp))
		line("SUMMARY:" + icsEscape(e.Name))
		line("DESCRIPTION:" + icsEscape(desc))
		line("CATEGORIES:" + icsEscape(e.Kind))
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}

func icsDescription(e routineCalendarEvent) string {
	switch e.Kind {
	case "planned":
		return fmt.Sprintf("Planned start of routine %s", e.Slug)
	case "pending":
		return fmt.Sprintf("One-time start of routine %s", e.Slug)
	default:
		res := e.Status
		if e.Outcome != "" {
			res += "/" + e.Outcome
		}
		return fmt.Sprintf("Run of routine %s: %s", e.Slug, res)
	}
}

// icsEscape escapes TEXT values: backslash, semicolon, comma and newlines.
func icsEscape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\r\n", "\\n")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return strings.ReplaceAll(s, "\r", "\\n")
}

// foldICSLine splits a content line into pieces of at most 75 octets, each
// continuation starting with one space, without cutting a UTF-8 character.
func foldICSLine(s string) string {
	if len(s) <= 75 {
		return s
	}
	var b strings.Builder
	limit := 75
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		limit = 74 // the leading space counts toward the 75
	}
	b.WriteString(s)
	return b.String()
}
