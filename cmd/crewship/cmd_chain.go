package main

// CLI parity for GET /api/v1/chains/{anchor} (internal/api/chain_handler.go).
//
//	crewship chain ENG-4          # what an issue set off
//	crewship chain prn_abc123     # why a run happened, and what it started
//	crewship chain nightly-deploy # every run of a routine
//	crewship chain aut_abc123     # what a rule is wired to, and what it has done
//
// The API returns a graph. This renders it as a tree, because a terminal has
// no second dimension to spend on a graph and a reader following causality
// reads downward — but every edge in the response gets exactly one line, so
// the tree and the JSON describe the same thing.
//
// Two consequences worth knowing before reading the code. The layout walks the
// graph UNDIRECTED, because the anchor is usually in the middle of its chain:
// `chain <run-id>` has to show the routine and the issue ABOVE it as well as
// the nested runs below, and a children-only walk would strand both. Direction
// is therefore printed on the edge (`[triggers ->]` vs `[<- triggers]`) rather
// than implied by nesting. And an edge reaching a node that is already on the
// page prints as a one-line cross-link instead of re-expanding its subtree,
// which is what stops a cycle from printing forever while still showing the
// edge that closes it.

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// chainNode / chainEdge / chainGap mirror internal/chain's wire types. Only
// the fields this command renders are declared; --format json prints the
// decoded struct, so a field added server-side needs a field here too.
type chainNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Depth  int    `json:"depth"`
	// ChainDepth is the run's own composition depth, not its distance from the
	// anchor (that is Depth). Rendered only when non-zero, so an ordinary run
	// reads exactly as it did before.
	ChainDepth    int    `json:"chain_depth"`
	Anchor        bool   `json:"anchor"`
	Partial       bool   `json:"partial"`
	PartialReason string `json:"partial_reason"`
}

type chainEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type chainGap struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type chainGraph struct {
	Anchor      string      `json:"anchor"`
	AnchorNode  string      `json:"anchor_node"`
	MaxDepth    int         `json:"max_depth"`
	MaxNodes    int         `json:"max_nodes"`
	Nodes       []chainNode `json:"nodes"`
	Edges       []chainEdge `json:"edges"`
	Truncated   bool        `json:"truncated"`
	TruncatedBy string      `json:"truncated_by"`
	Gaps        []chainGap  `json:"gaps"`
}

var chainCmd = &cobra.Command{
	Use:     "chain <anchor>",
	Aliases: []string{"why"},
	Short:   "Show what caused what around an issue, run, routine, assignment or automation",
	Long: `Reconstruct the causal chain around one anchor and print it as a tree.

The anchor is whatever you have to hand — an issue identifier, an issue id, a
run id, a routine id or slug, an assignment id, an inbox item id, or an
automation id. The server resolves it and walks outward across both execution
substrates (routine runs and agent delegation), so you do not need to know
which one your anchor lives in.

Edge kinds:
  triggers   the parent caused the child to start
  runs       the parent is the routine definition, the child is one run of it
  executes   the parent is the agent carrying the child out
  produces   the parent emitted the child (an inbox approval, a failure alert)
  relates    an author-declared issue<->issue link, not a causal one

Automations are the origin of a composed chain. A run started by a rule is
linked back to it exactly (pipeline_runs.triggered_via='automation'), so
"chain <run-id>" names the rule that began it rather than stopping at the
routine. Anchoring on a rule shows what it is wired to AND the runs it has
caused.

A rule is only ever drawn where it actually fired. Walking a routine does not
list the rules that merely point at it: they would be drawn with the same
edge as the one that really fired, and a chain that offers four candidate
causes for a run you started by hand is worse than one that offers none. The
rules that did fire stay reachable through the runs they caused.

Runs that a rule started off another run are marked [composed depth N].

Two links do not exist in the schema and are reported rather than guessed:
inbox_items carries no issue pointer on any kind, and escalations has neither
a run nor a mission column. Nodes at those boundaries are marked (partial) and
the reasons are listed under "Not walkable" at the end. Pass --gaps to see
them in full.

Both bounds are capped server-side. When a cap bites, the footer says so and
names which one — a short tree is never silently presented as a whole chain.

Examples:
  crewship chain ENG-4
  crewship chain ENG-4 --depth 6 --limit 400
  crewship chain aut_01hx...
  crewship chain prn_01hx... --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		depth, _ := cmd.Flags().GetInt("depth")
		limit, _ := cmd.Flags().GetInt("limit")
		showGaps, _ := cmd.Flags().GetBool("gaps")

		q := url.Values{}
		if depth > 0 {
			q.Set("depth", strconv.Itoa(depth))
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		qs := ""
		if len(q) > 0 {
			qs = "?" + q.Encode()
		}
		// The fmt.Sprintf is inlined into the Get call, not assigned to a
		// local first, because cli_route_contract_test.go reads the path
		// argument statically: a literal or a Sprintf format string is checked
		// against the router's table, while a local variable renders as an
		// opaque "{}" and the call is dropped from the gate SILENTLY. The
		// mild awkwardness buys real verification that this route exists.
		resp, err := client.Get(fmt.Sprintf("/api/v1/chains/%s%s", url.PathEscape(args[0]), qs))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var g chainGraph
		if err := cli.ReadJSON(resp, &g); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(g, func() {
			for _, line := range renderChainTree(g, showGaps) {
				fmt.Println(line)
			}
		})
	},
}

// renderChainTree returns the human view as lines. A pure function of the
// decoded payload so the layout is unit-testable without an HTTP round trip.
func renderChainTree(g chainGraph, showGaps bool) []string {
	byID := make(map[string]chainNode, len(g.Nodes))
	for _, n := range g.Nodes {
		byID[n.ID] = n
	}

	// Undirected adjacency: every edge is reachable from both ends, tagged so
	// the printed arrow still tells the truth about which way it points.
	// Edges whose endpoints are not both present are skipped defensively —
	// the server already drops those, and printing one would name a node the
	// reader cannot find.
	adj := map[string][]chainLink{}
	for _, e := range g.Edges {
		if _, ok := byID[e.From]; !ok {
			continue
		}
		if _, ok := byID[e.To]; !ok {
			continue
		}
		adj[e.From] = append(adj[e.From], chainLink{edge: e, to: e.To})
		adj[e.To] = append(adj[e.To], chainLink{edge: e, to: e.From, reverse: true})
	}

	var out []string
	anchor, ok := byID[g.AnchorNode]
	if !ok {
		return []string{"chain: response carried no anchor node"}
	}
	out = append(out, fmt.Sprintf("%s%s%s", cli.Bold, chainNodeLine(anchor, ""), cli.Reset))

	st := &chainTreeState{
		byID:        byID,
		adj:         adj,
		printed:     map[string]bool{g.AnchorNode: true},
		printedEdge: map[string]bool{},
	}
	out = append(out, st.subtree(g.AnchorNode, "")...)
	printed := st.printed

	// Any node the walk found but no edge reached from the anchor's component
	// (possible when a cap dropped the connecting node). Listing them beats
	// dropping them.
	var orphans []string
	for _, n := range g.Nodes {
		if !printed[n.ID] {
			orphans = append(orphans, n.ID)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		out = append(out, "", "Not reachable from the anchor in this response:")
		for _, id := range orphans {
			out = append(out, "  "+chainNodeLine(byID[id], ""))
		}
	}

	out = append(out, "")
	out = append(out, fmt.Sprintf("%d nodes, %d edges (depth<=%d, limit %d)",
		len(g.Nodes), len(g.Edges), g.MaxDepth, g.MaxNodes))
	if g.Truncated {
		out = append(out, fmt.Sprintf("%sTruncated: the %s cap was reached, so this is NOT the whole chain. Raise --%s.%s",
			cli.Yellow, g.TruncatedBy, chainCapFlag(g.TruncatedBy), cli.Reset))
	}

	if partials := chainPartialLines(g); len(partials) > 0 {
		out = append(out, "", "Not walkable (the schema carries no link here):")
		out = append(out, partials...)
	}
	if showGaps && len(g.Gaps) > 0 {
		out = append(out, "", "Known gaps in the data model:")
		for _, gap := range g.Gaps {
			out = append(out, fmt.Sprintf("  %s -> %s", gap.From, gap.To))
			out = append(out, "    "+sanitizeTerminal(gap.Reason))
		}
	}
	return out
}

func chainCapFlag(by string) string {
	if by == "depth" {
		return "depth"
	}
	return "limit"
}

// chainLink is one traversable end of an edge. reverse means the edge points
// from the node being reached back at the node we came from.
type chainLink struct {
	edge    chainEdge
	to      string
	reverse bool
}

type chainTreeState struct {
	byID map[string]chainNode
	adj  map[string][]chainLink
	// printed: nodes already on the page, so a diamond does not duplicate a
	// subtree. printedEdge: edges already rendered, so the undirected
	// adjacency does not print each edge twice (once per end).
	printed     map[string]bool
	printedEdge map[string]bool
}

func (s *chainTreeState) subtree(id, indent string) []string {
	// Collect the links that still need a line before laying any out, so the
	// last one gets the └─ corner.
	var pending []chainLink
	for _, l := range s.adj[id] {
		key := l.edge.From + "\x00" + l.edge.To + "\x00" + l.edge.Kind
		if s.printedEdge[key] {
			continue
		}
		s.printedEdge[key] = true
		pending = append(pending, l)
	}

	var out []string
	for i, l := range pending {
		branch, next := "├─ ", indent+"│  "
		if i == len(pending)-1 {
			branch, next = "└─ ", indent+"   "
		}
		child := s.byID[l.to]
		label := chainEdgeLabel(l)
		if s.printed[l.to] {
			// The node is already on the page. Render the edge as a
			// cross-link rather than re-expanding — this is the line that
			// shows a cycle closing, and the reason it does not loop.
			out = append(out, fmt.Sprintf("%s%s%s %s %s(shown above)%s",
				indent, branch, label, chainShortRef(child), cli.Dim, cli.Reset))
			continue
		}
		s.printed[l.to] = true
		out = append(out, indent+branch+chainNodeLine(child, label))
		out = append(out, s.subtree(l.to, next)...)
	}
	return out
}

// chainEdgeLabel prints the edge kind with the direction it actually points,
// because the tree walks undirected and nesting therefore cannot carry it.
func chainEdgeLabel(l chainLink) string {
	if l.reverse {
		return fmt.Sprintf("%s[<- %s]%s", cli.Dim, l.edge.Kind, cli.Reset)
	}
	return fmt.Sprintf("%s[%s ->]%s", cli.Dim, l.edge.Kind, cli.Reset)
}

func chainNodeLine(n chainNode, edgeLabel string) string {
	var b strings.Builder
	if edgeLabel != "" {
		b.WriteString(edgeLabel + " ")
	}
	fmt.Fprintf(&b, "%s%s%s %s", cli.Cyan, chainKindLabel(n), cli.Reset, chainShortRef(n))
	// Labels come out of user- and agent-written rows (issue titles,
	// assignment prompts, inbox subjects) and are printed straight to a
	// terminal, so control bytes are stripped before they can repaint it.
	if label := sanitizeTerminal(strings.ReplaceAll(n.Label, "\n", " ")); label != "" {
		fmt.Fprintf(&b, "  %s", truncateStr(label, 60))
	}
	if n.Status != "" {
		fmt.Fprintf(&b, "  %s(%s)%s", cli.Dim, sanitizeTerminal(n.Status), cli.Reset)
	}
	// A composed run says so. Printed only above zero: every hand-started run
	// carries 0, and a "composed 0" on each of them would bury the handful that
	// are actually composed.
	if n.ChainDepth > 0 {
		fmt.Fprintf(&b, " %s[composed depth %d]%s", cli.Dim, n.ChainDepth, cli.Reset)
	}
	if n.Partial {
		fmt.Fprintf(&b, " %s(partial)%s", cli.Yellow, cli.Reset)
	}
	return b.String()
}

// chainShortRef is the handle a reader uses to identify a node — and, more
// importantly, to tell it apart from its siblings.
//
// Only three kinds have a `key` that is unique inside a workspace: an issue's
// identifier, a routine's slug, and an agent's slug. For a run the key is the
// slug of the routine it ran, which is shared by every run of that routine —
// printing it would render two different runs as the same line. For an inbox
// item the key is its kind, which is shared by every item of that kind. Those
// fall back to the row id, which is the only thing that distinguishes them.
func chainShortRef(n chainNode) string {
	switch n.Kind {
	case "issue", "routine", "agent":
		if n.Key != "" {
			return sanitizeTerminal(n.Key)
		}
	}
	return truncateID(sanitizeTerminal(n.Ref), 20)
}

// chainKindLabel qualifies the kind with the sub-kind when there is one, so an
// inbox item reads "inbox/waitpoint" rather than an undifferentiated "inbox".
//
// An automation is qualified by its event_type for the same reason: "what arms
// this rule" is the first thing a reader wants from a node whose whole job is
// to explain why a chain began, and it is the only kind whose key is otherwise
// unprinted (event_type is not unique per workspace, so chainShortRef falls
// back to the id).
func chainKindLabel(n chainNode) string {
	if (n.Kind == "inbox" || n.Kind == "automation") && n.Key != "" {
		return sanitizeTerminal(n.Kind) + "/" + sanitizeTerminal(n.Key)
	}
	return sanitizeTerminal(n.Kind)
}

// chainPartialLines lists each distinct reason once, with the nodes it applies
// to. Repeating the same sentence under every agent node would bury the two
// reasons that are actually about missing schema.
func chainPartialLines(g chainGraph) []string {
	byReason := map[string][]string{}
	var order []string
	for _, n := range g.Nodes {
		if !n.Partial || n.PartialReason == "" {
			continue
		}
		if _, seen := byReason[n.PartialReason]; !seen {
			order = append(order, n.PartialReason)
		}
		byReason[n.PartialReason] = append(byReason[n.PartialReason], chainShortRef(n))
	}
	var out []string
	for _, reason := range order {
		refs := byReason[reason]
		sort.Strings(refs)
		out = append(out, fmt.Sprintf("  %s: %s", strings.Join(refs, ", "), sanitizeTerminal(reason)))
	}
	return out
}

func init() {
	chainCmd.Flags().Int("depth", 0, "How many hops from the anchor to walk (server default 4, max 10)")
	chainCmd.Flags().Int("limit", 0, "Maximum nodes to return (server default 200, max 1000)")
	chainCmd.Flags().Bool("gaps", false, "Print the full text of the links the data model cannot carry")
	rootCmd.AddCommand(chainCmd)
}
