// Package chain reconstructs the causal graph around one anchor — an issue,
// a run, a routine or an assignment — in a single walk.
//
// Why it exists: "what caused what" is spread across two execution substrates
// that share no table. `pipeline_runs` is the routine substrate; `assignments`
// is the delegation substrate; `missions` (issues) sits above both and points
// at each with an untyped string. A UI that wants to draw the causal graph
// therefore has to stitch five calls together and hard-code the join rules,
// which means every client re-implements — and re-gets-wrong — the same
// pointer semantics.
//
// The design rule here is that a link is walked only if a column actually
// carries it. Where a link is missing from the schema, this package says so
// (Graph.Gaps, Node.Partial) rather than inferring one. An invented edge is
// worse than an admitted gap: a gap is a bug report, an invented edge is a
// wrong answer that looks authoritative.
package chain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// NodeKind enumerates the row types a chain node can stand for. Each maps to
// exactly one table, so a node's identity is (kind, primary key).
type NodeKind string

const (
	KindIssue      NodeKind = "issue"      // missions (rows carrying an identifier)
	KindRoutine    NodeKind = "routine"    // pipelines
	KindRun        NodeKind = "run"        // pipeline_runs
	KindAssignment NodeKind = "assignment" // assignments
	KindAgent      NodeKind = "agent"      // agents
	KindInbox      NodeKind = "inbox"      // inbox_items
	KindAutomation NodeKind = "automation" // automations
)

// EdgeKind is the relationship an edge asserts. Every kind below is backed by
// a real column; see the table in docs/api-reference/chains.mdx for the
// column behind each (from-kind, to-kind, edge-kind) triple.
type EdgeKind string

const (
	// EdgeTriggers: `from` caused `to` to come into existence or to start.
	EdgeTriggers EdgeKind = "triggers"
	// EdgeRuns: `from` is the definition, `to` is one execution of it.
	EdgeRuns EdgeKind = "runs"
	// EdgeExecutes: `from` is the actor carrying `to` out.
	EdgeExecutes EdgeKind = "executes"
	// EdgeProduces: `from` emitted `to` as a consequence.
	EdgeProduces EdgeKind = "produces"
	// EdgeRelates: an author-declared association with no causal direction
	// (mission_relations). Kept distinct so a client can style or drop it.
	EdgeRelates EdgeKind = "relates"
)

// Node is one row in the graph, flattened. Ref is the row's primary key; ID is
// the graph-wide key ("kind:ref") that Edge.From/Edge.To reference.
type Node struct {
	ID     string   `json:"id"`
	Kind   NodeKind `json:"kind"`
	Ref    string   `json:"ref"`
	Key    string   `json:"key,omitempty"` // human handle: issue identifier, routine slug, agent slug, inbox kind
	Label  string   `json:"label"`
	Status string   `json:"status,omitempty"`
	Depth  int      `json:"depth"`
	Anchor bool     `json:"anchor,omitempty"`

	// Partial marks a node whose outward expansion is known to be incomplete,
	// with PartialReason naming why. Two causes, deliberately not separated:
	// a link the schema does not carry, and a boundary this walk declines to
	// cross. Both mean "there is more here and this response does not have
	// it", which is the only thing a caller can act on.
	Partial       bool   `json:"partial,omitempty"`
	PartialReason string `json:"partial_reason,omitempty"`
}

// Edge is a directed link between two materialised nodes. An edge is never
// emitted for an endpoint that was dropped by a cap — a dangling edge would
// read as a node the client failed to render.
type Edge struct {
	From string   `json:"from"`
	To   string   `json:"to"`
	Kind EdgeKind `json:"kind"`
}

// Gap records a link the product implies but the schema does not carry. It is
// returned unconditionally: a client drawing this graph needs to know which
// absences are "nothing happened" and which are "we cannot see it".
type Gap struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// Graph is the whole response. Nodes and Edges are in breadth-first discovery
// order, which is deterministic for a given database.
type Graph struct {
	Anchor      string `json:"anchor"`
	AnchorNode  string `json:"anchor_node"`
	MaxDepth    int    `json:"max_depth"`
	MaxNodes    int    `json:"max_nodes"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
	Truncated   bool   `json:"truncated"`
	TruncatedBy string `json:"truncated_by,omitempty"` // "depth" | "nodes"
	Gaps        []Gap  `json:"gaps"`
}

// Options bound the walk. Zero values fall back to the defaults below.
type Options struct {
	MaxDepth int
	MaxNodes int
}

// Walk bounds. A chain is a debugging aid, not an export: the caps exist so a
// pathological fan-out (a routine with 10k runs) cannot turn one request into
// a table scan, and so the response stays something a UI can lay out.
const (
	DefaultMaxDepth = 4
	MaxMaxDepth     = 10
	DefaultMaxNodes = 200
	MaxMaxNodes     = 1000
)

// ErrAnchorNotFound means no row in this workspace matched the anchor. It is
// deliberately indistinguishable from "the row exists in another workspace":
// the caller gets one 404 either way, so chain lookup cannot be used to probe
// for identifiers in a tenant the caller cannot read.
var ErrAnchorNotFound = errors.New("chain: anchor not found in this workspace")

// KnownGaps are the links this walker cannot make because no column carries
// them. Verified against the schema, not assumed.
//
// Both entries are real holes in the data model rather than omissions here.
// They are surfaced instead of guessed because the plausible guesses are all
// wrong: an inbox item's source_id is polymorphic over waitpoint token /
// escalation id / run id and never a mission id, and matching an escalation to
// a run by crew and timestamp would produce confident nonsense the moment two
// runs overlap.
var KnownGaps = []Gap{
	{
		From:   "inbox",
		To:     "issue",
		Reason: "inbox_items has no mission/issue column on any kind — source_id is polymorphic over waitpoint token, escalation id and run id only — so an issue cannot reach the inbox items raised while it was worked, and an inbox item cannot name the issue it belongs to.",
	},
	{
		From:   "escalation",
		To:     "run",
		Reason: "escalations has neither a run nor a mission column (only crew_id, chat_id, from_agent_id), so an escalation cannot be attached to the run or issue that provoked it. Inbox items of kind 'escalation' are therefore chain leaves.",
	},
}

// clampOptions applies the defaults and the hard ceilings. Out-of-range values
// are clamped rather than rejected so a client that asks for depth=999 gets
// the deepest walk the server will do, with truncated=true telling it the
// answer is partial — which is more useful than a 400 it has to special-case.
func clampOptions(o Options) Options {
	if o.MaxDepth <= 0 {
		o.MaxDepth = DefaultMaxDepth
	}
	if o.MaxDepth > MaxMaxDepth {
		o.MaxDepth = MaxMaxDepth
	}
	if o.MaxNodes <= 0 {
		o.MaxNodes = DefaultMaxNodes
	}
	if o.MaxNodes > MaxMaxNodes {
		o.MaxNodes = MaxMaxNodes
	}
	return o
}

func nodeID(kind NodeKind, ref string) string { return string(kind) + ":" + ref }

// neighbour is one discovered node plus the edge that reaches it. The edge
// carries its own From/To because direction is a property of the relationship,
// not of which end the walk happened to start from: a run discovered from its
// routine and a routine discovered from its run must both yield routine→run.
type neighbour struct {
	node Node
	edge Edge
}

// walker holds the mutable state of one Walk. It is not safe for concurrent
// use and is never shared.
type walker struct {
	db          *sql.DB
	workspaceID string
	opt         Options

	nodes []Node
	edges []Edge
	// seen is the cycle guard. Keyed on node ID, so a row is materialised and
	// expanded at most once no matter how many edges reach it. Every cycle in
	// the data (a → b → a delegation loop, two issues that relate to each
	// other) terminates here rather than in a depth counter.
	seen     map[string]bool
	seenEdge map[string]bool

	truncated   bool
	truncatedBy string
}

// Walk resolves anchor to a row in workspaceID and returns the connected chain
// around it, bounded by opt.
//
// Every query is workspace-scoped. That is not defence in depth layered over a
// scoped entry point — it is the only fence there is, because the walk hops
// between tables on untyped string columns (pipeline_runs.triggered_by_id is a
// schedule id, a webhook id, a parent run id, or an issue identifier depending
// on triggered_via) and a value that collides across tenants would otherwise
// pull a foreign row into the graph.
func Walk(ctx context.Context, db *sql.DB, workspaceID, anchor string, opt Options) (*Graph, error) {
	if db == nil {
		return nil, errors.New("chain: nil database")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	anchor = strings.TrimSpace(anchor)
	if workspaceID == "" {
		return nil, errors.New("chain: workspace required")
	}
	if anchor == "" {
		return nil, ErrAnchorNotFound
	}
	opt = clampOptions(opt)

	w := &walker{
		db:          db,
		workspaceID: workspaceID,
		opt:         opt,
		seen:        map[string]bool{},
		seenEdge:    map[string]bool{},
	}

	root, err := w.resolveAnchor(ctx, anchor)
	if err != nil {
		return nil, err
	}
	root.Anchor = true
	root.Depth = 0
	w.materialise(root)

	// Breadth-first so the depth recorded on a node is its true shortest
	// distance from the anchor, which is what a caller trims on.
	queue := []Node{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		neighbours, err := w.expand(ctx, cur)
		if err != nil {
			return nil, err
		}
		for _, nb := range neighbours {
			if w.seen[nb.node.ID] {
				// Already in the graph: the edge is still real and still
				// worth returning (this is how cycles and diamonds render),
				// but the node is not re-expanded.
				w.addEdge(nb.edge)
				continue
			}
			if cur.Depth+1 > w.opt.MaxDepth {
				w.markTruncated("depth")
				continue
			}
			if len(w.nodes) >= w.opt.MaxNodes {
				w.markTruncated("nodes")
				continue
			}
			nb.node.Depth = cur.Depth + 1
			w.materialise(nb.node)
			w.addEdge(nb.edge)
			queue = append(queue, nb.node)
		}
	}

	return &Graph{
		Anchor:      anchor,
		AnchorNode:  root.ID,
		MaxDepth:    w.opt.MaxDepth,
		MaxNodes:    w.opt.MaxNodes,
		Nodes:       w.nodes,
		Edges:       w.edges,
		Truncated:   w.truncated,
		TruncatedBy: w.truncatedBy,
		Gaps:        append([]Gap(nil), KnownGaps...),
	}, nil
}

func (w *walker) materialise(n Node) {
	w.seen[n.ID] = true
	w.nodes = append(w.nodes, n)
}

func (w *walker) addEdge(e Edge) {
	// An edge whose endpoint was dropped by a cap must be dropped too —
	// otherwise the client sees a reference to a node that is not in the
	// response and has no way to tell that from a rendering bug of its own.
	if !w.seen[e.From] || !w.seen[e.To] {
		return
	}
	key := e.From + "\x00" + e.To + "\x00" + string(e.Kind)
	if w.seenEdge[key] {
		return
	}
	w.seenEdge[key] = true
	w.edges = append(w.edges, e)
}

// markTruncated records the FIRST cap that bit. First rather than last because
// it is the one that shaped the result: once the node cap has stopped the walk
// the depth cap is never reached, and reporting the later one would send the
// caller to raise the wrong limit.
func (w *walker) markTruncated(by string) {
	w.truncated = true
	if w.truncatedBy == "" {
		w.truncatedBy = by
	}
}

// fanOutLimit bounds a single neighbour query. The global node cap is what
// makes the response small; this is what keeps one routine with 50k runs from
// materialising 50k rows in Go before the cap discards them.
func (w *walker) fanOutLimit() int { return w.opt.MaxNodes + 1 }

func (w *walker) expand(ctx context.Context, n Node) ([]neighbour, error) {
	switch n.Kind {
	case KindIssue:
		return w.expandIssue(ctx, n)
	case KindRoutine:
		return w.expandRoutine(ctx, n)
	case KindRun:
		return w.expandRun(ctx, n)
	case KindAssignment:
		return w.expandAssignment(ctx, n)
	case KindInbox:
		return w.expandInbox(ctx, n)
	case KindAutomation:
		return w.expandAutomation(ctx, n)
	case KindAgent:
		// Deliberate leaf — see issueNode/agentNode for the reason carried to
		// the client.
		return nil, nil
	default:
		return nil, fmt.Errorf("chain: unknown node kind %q", n.Kind)
	}
}
