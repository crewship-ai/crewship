package pages

import "strings"

// The panel icon vocabulary (PRD §3, §9b.2).
//
// A panel's icon is derived from its schema, which is right until a page
// carries three `status.v1` panels: "is it running", "who is on call" and
// "what deployed today" then wear one face, and the header stops telling the
// reader which panel they are looking at. `icon:` lets the author say what the
// panel is ABOUT, in the one place the rest of the panel is declared.
//
// WHY THE SET IS CLOSED.
// The same reason PanelSchema is (schema.go). An open string means the server
// accepts a name the client has no glyph for, and the failure is quiet: the
// page saves, the panel renders, and the header is blank or wrong. Worse than
// an unknown schema, in fact — an unknown schema at least reaches a fallback
// that says so, while a missing glyph looks like a design decision. So the
// vocabulary is a release decision, refused at save time by name, and mirrored
// once on the client (components/features/pages/panels/panel-icon.tsx), where
// a parity test reads THIS file and fails if the two lists disagree.
//
// WHY THESE THIRTEEN.
// They are what a producer watches, not what the icon library happens to have.
// The machine (memory, cpu, disk, network, container), the stores it talks to
// (database, queue), time (clock, calendar), and the business the machine
// exists for (money, people, deploy, alert). A list a human reads in one go
// and picks from without opening the docs.
//
// WHY THERE IS NO `check` AND NO `warning`.
// The panel already renders a verdict: status.v1 draws ✓ / ! / ✕ per item, the
// frame draws the freshness word, and colour is spoken for by ok/warning/
// critical (§3: "Status colours are reserved"). A tick or a warning triangle in
// the header is a second verdict on the same card, and the reader cannot tell
// whether it describes the subject or the state. `alert` is admitted because
// "the incident board" is a SUBJECT a producer watches; it renders as a siren
// and never as the warning triangle the broken-panel chrome uses.
//
// WHY THERE IS NO PER-ICON COLOUR, EVER.
// Same rule, one step further out. Colour on this surface means state. A second
// colour axis on the same glyph would collide with the first, and the collision
// is unreadable rather than merely ugly. The icon carries identity; the state
// carries colour.

// PanelIcon is the glyph a panel wears in its header. Optional: a panel that
// declares none keeps the icon its schema implies, which is what every panel
// did before this field existed.
type PanelIcon string

const (
	// IconMemory — RAM. Not the agent's memory: that concept has its own icon
	// elsewhere in the product and this one is a stick of DDR.
	IconMemory PanelIcon = "memory"
	// IconCPU — processor load.
	IconCPU PanelIcon = "cpu"
	// IconDisk — a volume's free space, IO, an array's health.
	IconDisk PanelIcon = "disk"
	// IconNetwork — throughput, reachability, a link.
	IconNetwork PanelIcon = "network"
	// IconContainer — what is running where.
	IconContainer PanelIcon = "container"
	// IconDatabase — a store: rows, replication lag, connections.
	IconDatabase PanelIcon = "database"
	// IconQueue — work waiting to be done. Depth, backlog, age of the oldest.
	IconQueue PanelIcon = "queue"
	// IconClock — elapsed time: uptime, latency, how long since.
	IconClock PanelIcon = "clock"
	// IconCalendar — dated things: a schedule, a deadline, a period.
	IconCalendar PanelIcon = "calendar"
	// IconMoney — revenue, cost, a balance.
	IconMoney PanelIcon = "money"
	// IconPeople — headcount, who is on call, who is in.
	IconPeople PanelIcon = "people"
	// IconDeploy — releases and what shipped.
	IconDeploy PanelIcon = "deploy"
	// IconAlert — incidents as a SUBJECT (how many are open, who is paging),
	// never as this panel's own verdict. See the note above.
	IconAlert PanelIcon = "alert"
)

// PanelIcons is the vocabulary in the order the documentation lists it, which
// is also the order a refusal names them: machine, stores, time, business. The
// slice is the single source of the set — Known and the error text both read
// it, so a name added to the constants above and forgotten here is a name
// nothing accepts.
var PanelIcons = []PanelIcon{
	IconMemory, IconCPU, IconDisk, IconNetwork, IconContainer,
	IconDatabase, IconQueue,
	IconClock, IconCalendar,
	IconMoney, IconPeople, IconDeploy, IconAlert,
}

var knownPanelIcons = func() map[PanelIcon]bool {
	m := make(map[PanelIcon]bool, len(PanelIcons))
	for _, i := range PanelIcons {
		m[i] = true
	}
	return m
}()

// Known reports whether i is a member of the closed set. The empty string is
// NOT a member: "no icon declared" is checked for separately, because it is
// the default rather than a value.
func (i PanelIcon) Known() bool { return knownPanelIcons[i] }

func (i PanelIcon) String() string { return string(i) }

// PanelIconList renders the vocabulary for a refusal. A closed set whose error
// does not name its members is a set the author has to go and look up, which
// is how `icon: ram` gets tried three times.
func PanelIconList() string {
	names := make([]string, 0, len(PanelIcons))
	for _, i := range PanelIcons {
		names = append(names, string(i))
	}
	return strings.Join(names, ", ")
}
