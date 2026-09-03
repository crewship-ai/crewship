package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/crewship-ai/crewship/internal/cli"
)

// List paging on the CLI — the client half of the S1 convention.
//
// Every list that pages takes `--limit` and `--offset`, sends them as
// `?limit=&offset=`, and reads the totals the server publishes in
// `X-Total-Count`, `X-Limit` and `X-Offset`. The footer says what the table
// did not show, because a page that fills the terminal looks complete —
// exactly the trap the web board fell into when it printed "100 issues" at
// 1 015.

// addListPagingFlags registers --limit and --offset on a list command.
//
//nolint:unused // adopted by the list commands landing in #2302 (issues, missions) and #2303 (crews, agents, credentials)
func addListPagingFlags(flags interface {
	Int(name string, value int, usage string) *int
}, defaultLimit int) {
	flags.Int("limit", defaultLimit, "Maximum number of rows to return (the server caps it)")
	flags.Int("offset", 0, "Skip this many rows — page with --offset <limit>, 2×<limit>, …")
}

// setListPaging copies --limit/--offset into the query string when they are
// set. A zero limit means "server default", so it is not sent.
func setListPaging(params url.Values, limit, offset int) {
	if limit > 0 {
		params.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
}

// listMeta is what the server said about the page: the total the filter
// matched and the limit/offset it actually applied (after clamping).
type listMeta struct {
	Total, Limit, Offset int
	// Known is false when the server sent no X-Total-Count — an older
	// server, or a list that does not page — and the footer stays silent.
	Known bool
}

// readListMeta reads the paging headers off a list response.
func readListMeta(resp *http.Response) listMeta {
	if resp == nil {
		return listMeta{}
	}
	total, err := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	if err != nil {
		return listMeta{}
	}
	limit, _ := strconv.Atoi(resp.Header.Get("X-Limit"))
	offset, _ := strconv.Atoi(resp.Header.Get("X-Offset"))
	return listMeta{Total: total, Limit: limit, Offset: offset, Known: true}
}

// printListFooter tells a person how much of the list the table showed, and
// how to reach the rest. Human formats only: json/yaml/ndjson consumers read
// the array, and quiet is for scripts that count lines.
func printListFooter(f *cli.Formatter, meta listMeta, shown int) {
	if !meta.Known || !f.RoutesToHuman() || f.Format == "quiet" {
		return
	}
	if shown >= meta.Total && meta.Offset == 0 {
		return
	}
	next := meta.Offset + shown
	if next < meta.Total && meta.Limit > 0 {
		fmt.Fprintf(f.Writer, "showing %d–%d of %d · next page: --offset %d\n",
			meta.Offset+1, next, meta.Total, next)
		return
	}
	fmt.Fprintf(f.Writer, "showing %d–%d of %d\n", meta.Offset+1, next, meta.Total)
}
