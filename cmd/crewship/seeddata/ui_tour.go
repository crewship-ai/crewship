package seeddata

// The UI tour (`crewship seed --ui-tour`) layers a lived-in week on top of the
// default demo workspace, for visual acceptance of the product screens. It
// stays inside the same fictional world as the default seed — Harbor Goods,
// the small storage-box retailer the business stories run, staffed by the
// Sales & Shipping, Finance & Marketing and Operations crews — and adds what
// the default seed deliberately leaves out: scheduled routines (so Calendar
// has upcoming fires), run history (completed, waiting for a person, failed),
// a longer issue board, and the inbox and journal rows those writes produce.
//
// Every routine here is agentless: transform, code (expr/cel), query, wait
// and notify steps only. The tour therefore needs no provider credentials,
// and the schedules it creates never spend a token when they fire.

// TourFolder groups tour routines. Name is the routine folder (the
// /pipeline-folders resource the Routines panel groups by); Tag is the same
// grouping as a routine tag, so `routine list --tag` finds it too.
type TourFolder struct {
	Tag, Name, Color string
}

var TourFolders = []TourFolder{
	{Tag: "daily-operations", Name: "Daily operations", Color: "green"},
	{Tag: "customer-care", Name: "Customer care", Color: "blue"},
	{Tag: "finance", Name: "Finance", Color: "amber"},
	{Tag: "reports", Name: "Reports", Color: "violet"},
}

// TourSchedule is the cron plan a tour routine is saved with.
type TourSchedule struct {
	Name     string
	Cron     string
	Timezone string
	Inputs   map[string]interface{}
}

// TourRun is one entry of a routine's history, created in order. ViaSchedule
// force-fires the routine's plan (the run is attributed to the schedule and
// uses the plan's stored inputs); otherwise it is a manual run with Inputs.
// Expect is the state the run must end in: completed, failed or waiting.
// Approve answers a waiting run's approval so it completes, which leaves a
// decided approval in the inbox history.
type TourRun struct {
	ViaSchedule bool
	Inputs      map[string]interface{}
	Expect      string
	Approve     string // decision action id; empty leaves the run waiting
}

type TourRoutine struct {
	Slug, Name, Description string
	CrewSlug                string
	AuthorAgentSlug         string
	Folder                  string // TourFolder.Tag
	Schedule                *TourSchedule
	Definition              map[string]interface{}
	Runs                    []TourRun
}

const tourTimezone = "Europe/Prague"

func tourInput(name, typ, label string, def interface{}) map[string]interface{} {
	return map[string]interface{}{"name": name, "type": typ, "label": label, "default": def}
}

func tourTransform(id, input, expression string, needs ...string) map[string]interface{} {
	step := map[string]interface{}{"id": id, "type": "transform", "transform": map[string]interface{}{"input": input, "expression": expression}}
	if len(needs) > 0 {
		step["needs"] = needs
	}
	return step
}

func tourNotify(id, title, body, priority, category string, needs ...string) map[string]interface{} {
	step := map[string]interface{}{"id": id, "type": "notify", "notify": map[string]interface{}{
		"to": "workspace", "title": title, "body": body, "priority": priority, "category": category,
	}}
	if len(needs) > 0 {
		step["needs"] = needs
	}
	return step
}

func tourCode(id, runtime, code string, needs ...string) map[string]interface{} {
	step := map[string]interface{}{"id": id, "type": "code", "code": map[string]interface{}{"runtime": runtime, "code": code}}
	if len(needs) > 0 {
		step["needs"] = needs
	}
	return step
}

func tourApproval(id, title, prompt, risk string, needs ...string) map[string]interface{} {
	wait := map[string]interface{}{
		"kind": "approval", "approval_title": title, "approval_prompt": prompt,
		// A week: the tour is looked at over several days, and an approval
		// that times out overnight turns the "waiting" example into a failure.
		"timeout_sec": 7 * 24 * 3600,
		"decision_form": map[string]interface{}{"fields": []interface{}{}, "actions": []map[string]interface{}{
			{"id": "approve", "label": "Approve", "approved": true},
			{"id": "hold", "label": "Not now", "approved": false},
		}},
	}
	if risk != "" {
		wait["risk_level"] = risk
	}
	step := map[string]interface{}{"id": id, "type": "wait", "wait": wait}
	if len(needs) > 0 {
		step["needs"] = needs
	}
	return step
}

func tourDefinition(slug, name, description string, inputs []map[string]interface{}, steps ...map[string]interface{}) map[string]interface{} {
	def := map[string]interface{}{
		"dsl_version": "1.0", "name": slug, "display_name": name, "description": description,
		"agentless": true, "estimated_cost_usd": 0.0, "egress_targets": []string{},
		"steps": steps,
	}
	if len(inputs) > 0 {
		def["inputs"] = inputs
	}
	return def
}

func tourIn(kv ...interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

// TourRoutines is the routine catalogue of the UI tour, in creation order.
// The history runs are what Routines, Calendar's past days, Inbox and the
// journal show; the schedules are what Calendar shows ahead.
var TourRoutines = []TourRoutine{
	{
		Slug: "morning-order-digest", Name: "Morning order digest", CrewSlug: "engineering", AuthorAgentSlug: "sam", Folder: "daily-operations",
		Description: "Counts the orders that came in overnight and posts a short digest before the shop opens.",
		Schedule:    &TourSchedule{Name: "Weekdays 07:30", Cron: "30 7 * * 1-5", Timezone: tourTimezone, Inputs: tourIn("new_orders", 12, "needs_reply", 2)},
		Definition: tourDefinition("morning-order-digest", "Morning order digest", "Counts the orders that came in overnight and posts a short digest before the shop opens.",
			[]map[string]interface{}{tourInput("new_orders", "integer", "New orders", 12), tourInput("needs_reply", "integer", "Waiting for a reply", 2)},
			tourTransform("digest", `{"summary":"{{ inputs.new_orders }} new orders overnight, {{ inputs.needs_reply }} waiting for a reply."}`, ".summary"),
			tourNotify("post", "Orders overnight: {{ inputs.new_orders }} new", "{{ steps.digest.output }} Open Sales to answer the customers who wrote in.", "low", "routines.completed", "digest"),
		),
		Runs: []TourRun{
			{ViaSchedule: true, Expect: "completed"},
			{Inputs: tourIn("new_orders", 9, "needs_reply", 0), Expect: "completed"},
			{Inputs: tourIn("new_orders", 17, "needs_reply", 3), Expect: "completed"},
		},
	},
	{
		Slug: "carrier-delay-check", Name: "Carrier delay check", CrewSlug: "engineering", AuthorAgentSlug: "robin", Folder: "customer-care",
		Description: "Checks parcels that missed their delivery date and flags the day when more than three are late.",
		Schedule:    &TourSchedule{Name: "Three times a day", Cron: "0 8,12,16 * * 1-5", Timezone: tourTimezone, Inputs: tourIn("late_parcels", 1)},
		Definition: tourDefinition("carrier-delay-check", "Carrier delay check", "Checks parcels that missed their delivery date and flags the day when more than three are late.",
			[]map[string]interface{}{tourInput("late_parcels", "integer", "Late parcels", 1)},
			tourCode("over_limit", "expr", "{{ inputs.late_parcels }} > 3"),
			tourTransform("status", `{"line":"{{ inputs.late_parcels }} parcels late; follow-up needed: {{ steps.over_limit.output }}"}`, ".line", "over_limit"),
		),
		Runs: []TourRun{
			{ViaSchedule: true, Expect: "completed"},
			{Inputs: tourIn("late_parcels", 4), Expect: "completed"},
			{Inputs: tourIn("late_parcels", 2), Expect: "completed"},
		},
	},
	{
		Slug: "late-delivery-refund", Name: "Late-delivery refund", CrewSlug: "engineering", AuthorAgentSlug: "robin", Folder: "customer-care",
		Description: "Prepares a refund for a delivery that arrived late and waits for a person to approve the payout.",
		Definition: tourDefinition("late-delivery-refund", "Late-delivery refund", "Prepares a refund for a delivery that arrived late and waits for a person to approve the payout.",
			[]map[string]interface{}{tourInput("shipment", "string", "Shipment", "SHIP-105"), tourInput("customer", "string", "Customer", "Ava Novak"), tourInput("amount_eur", "string", "Refund (EUR)", "48.00")},
			tourTransform("claim", `{"line":"Refund EUR {{ inputs.amount_eur }} to {{ inputs.customer }} for {{ inputs.shipment }}"}`, ".line"),
			tourApproval("approve", "Refund EUR {{ inputs.amount_eur }} to {{ inputs.customer }}",
				"{{ inputs.shipment }} arrived four days after the promised date. The carrier accepted the claim.\n\nApprove to pay EUR {{ inputs.amount_eur }} back to {{ inputs.customer }}. A refund cannot be taken back.", "destructive", "claim"),
			tourTransform("done", `{"line":"Refund recorded for {{ inputs.shipment }}"}`, ".line", "approve"),
		),
		Runs: []TourRun{
			{Inputs: tourIn("shipment", "SHIP-098", "customer", "Lukas Berg", "amount_eur", "22.50"), Expect: "completed", Approve: "approve"},
			{Expect: "waiting"},
		},
	},
	{
		Slug: "overdue-invoice-reminders", Name: "Overdue invoice reminders", CrewSlug: "quality", AuthorAgentSlug: "jordan", Folder: "finance",
		Description: "Lists invoices past their due date and asks before payment reminders go out.",
		Schedule:    &TourSchedule{Name: "Mondays 09:00", Cron: "0 9 * * 1", Timezone: tourTimezone, Inputs: tourIn("overdue", 3, "total_eur", "1,840")},
		Definition: tourDefinition("overdue-invoice-reminders", "Overdue invoice reminders", "Lists invoices past their due date and asks before payment reminders go out.",
			[]map[string]interface{}{tourInput("overdue", "integer", "Overdue invoices", 3), tourInput("total_eur", "string", "Total (EUR)", "1,840")},
			tourTransform("list", `{"line":"{{ inputs.overdue }} invoices overdue, EUR {{ inputs.total_eur }} in total"}`, ".line"),
			tourApproval("approve", "Send {{ inputs.overdue }} payment reminders",
				"{{ steps.list.output }}. The oldest is INV-2041 from Marlow & Finch, 21 days late.\n\nApprove to send a friendly reminder to each customer.", "", "list"),
			tourNotify("sent", "Payment reminders sent", "{{ inputs.overdue }} reminders went out (EUR {{ inputs.total_eur }} outstanding).", "medium", "routines.completed", "approve"),
		),
		Runs: []TourRun{
			{Inputs: tourIn("overdue", 2, "total_eur", "960"), Expect: "completed", Approve: "approve"},
			{ViaSchedule: true, Expect: "waiting"},
		},
	},
	{
		Slug: "bank-feed-reconciliation", Name: "Bank feed reconciliation", CrewSlug: "quality", AuthorAgentSlug: "jordan", Folder: "finance",
		Description: "Matches yesterday's bank statement against recorded payments.",
		Schedule:    &TourSchedule{Name: "Weekdays 06:15", Cron: "15 6 * * 1-5", Timezone: tourTimezone, Inputs: tourIn("statement_total", "4210", "matched_total", "4210")},
		Definition: tourDefinition("bank-feed-reconciliation", "Bank feed reconciliation", "Matches yesterday's bank statement against recorded payments.",
			[]map[string]interface{}{tourInput("statement_total", "string", "Statement total (EUR)", "4210"), tourInput("matched_total", "string", "Matched payments (EUR)", "4210")},
			tourCode("balanced", "cel", `int("{{ inputs.statement_total }}") == int("{{ inputs.matched_total }}")`),
			tourTransform("result", `{"line":"Statement EUR {{ inputs.statement_total }}, matched EUR {{ inputs.matched_total }}, balanced: {{ steps.balanced.output }}"}`, ".line", "balanced"),
		),
		Runs: []TourRun{
			{ViaSchedule: true, Expect: "completed"},
			{Inputs: tourIn("statement_total", "3875", "matched_total", "3875"), Expect: "completed"},
			// The bank sent an empty statement: the total is not a number,
			// so the comparison step fails and the run records the error.
			{Inputs: tourIn("statement_total", "not provided", "matched_total", "3990"), Expect: "failed"},
		},
	},
	{
		Slug: "month-end-close", Name: "Month-end close checklist", CrewSlug: "quality", AuthorAgentSlug: "jordan", Folder: "finance",
		Description: "Opens the month-end checklist for the accountant on the first morning of each month.",
		Schedule:    &TourSchedule{Name: "First of the month", Cron: "0 7 1 * *", Timezone: tourTimezone},
		Definition: tourDefinition("month-end-close", "Month-end close checklist", "Opens the month-end checklist for the accountant on the first morning of each month.", nil,
			tourNotify("open", "Month-end close is open", "Reconcile the bank, export VAT and archive paid invoices before the 5th.", "medium", "routines.completed"),
		),
	},
	{
		Slug: "newsletter-signup-digest", Name: "Newsletter sign-up digest", CrewSlug: "quality", AuthorAgentSlug: "casey", Folder: "reports",
		Description: "Sums up new newsletter sign-ups and where they came from.",
		Schedule:    &TourSchedule{Name: "Tuesdays and Thursdays", Cron: "0 10 * * 2,4", Timezone: tourTimezone, Inputs: tourIn("signups", 26, "top_source", "blog footer")},
		Definition: tourDefinition("newsletter-signup-digest", "Newsletter sign-up digest", "Sums up new newsletter sign-ups and where they came from.",
			[]map[string]interface{}{tourInput("signups", "integer", "New sign-ups", 26), tourInput("top_source", "string", "Top source", "blog footer")},
			tourTransform("digest", `{"line":"{{ inputs.signups }} new sign-ups, most from the {{ inputs.top_source }}."}`, ".line"),
			tourNotify("post", "{{ inputs.signups }} new newsletter sign-ups", "{{ steps.digest.output }}", "low", "routines.completed", "digest"),
		),
		Runs: []TourRun{
			{Inputs: tourIn("signups", 31, "top_source", "checkout page"), Expect: "completed"},
			{ViaSchedule: true, Expect: "completed"},
		},
	},
	{
		Slug: "weekly-sales-summary", Name: "Weekly sales summary", CrewSlug: "engineering", AuthorAgentSlug: "alex", Folder: "reports",
		Description: "Friday summary of revenue, orders and the best-selling product.",
		Schedule:    &TourSchedule{Name: "Fridays 16:00", Cron: "0 16 * * 5", Timezone: tourTimezone, Inputs: tourIn("revenue_eur", "12,480", "orders", 143, "best_seller", "Oak storage box, large")},
		Definition: tourDefinition("weekly-sales-summary", "Weekly sales summary", "Friday summary of revenue, orders and the best-selling product.",
			[]map[string]interface{}{tourInput("revenue_eur", "string", "Revenue (EUR)", "12,480"), tourInput("orders", "integer", "Orders", 143), tourInput("best_seller", "string", "Best seller", "Oak storage box, large")},
			tourTransform("summary", `{"line":"EUR {{ inputs.revenue_eur }} from {{ inputs.orders }} orders. Best seller: {{ inputs.best_seller }}."}`, ".line"),
			tourNotify("post", "Week in sales: EUR {{ inputs.revenue_eur }}", "{{ steps.summary.output }}", "medium", "routines.completed", "summary"),
		),
		Runs: []TourRun{{ViaSchedule: true, Expect: "completed"}},
	},
	{
		Slug: "low-stock-watch", Name: "Low stock watch", CrewSlug: "ops", AuthorAgentSlug: "morgan", Folder: "daily-operations",
		Description: "Warns when a best-selling product drops below its reorder level.",
		Schedule:    &TourSchedule{Name: "Weekdays 07:00 and 14:00", Cron: "0 7,14 * * 1-5", Timezone: tourTimezone, Inputs: tourIn("product", "Linen basket, medium", "on_hand", 40, "reorder_at", 25)},
		Definition: tourDefinition("low-stock-watch", "Low stock watch", "Warns when a best-selling product drops below its reorder level.",
			[]map[string]interface{}{tourInput("product", "string", "Product", "Linen basket, medium"), tourInput("on_hand", "integer", "On hand", 40), tourInput("reorder_at", "integer", "Reorder level", 25)},
			tourCode("low", "expr", "{{ inputs.on_hand }} < {{ inputs.reorder_at }}"),
			map[string]interface{}{"id": "warn", "type": "notify", "needs": []string{"low"}, "if": "{{ steps.low.output }}", "notify": map[string]interface{}{
				"to": "workspace", "title": "Low stock: {{ inputs.product }}",
				"body":     "Only {{ inputs.on_hand }} left (reorder level {{ inputs.reorder_at }}). Place a supplier order today.",
				"priority": "high", "category": "routines.completed",
			}},
		),
		Runs: []TourRun{
			{ViaSchedule: true, Expect: "completed"},
			{Inputs: tourIn("product", "Oak storage box, large", "on_hand", 12, "reorder_at", 20), Expect: "completed"},
			{Inputs: tourIn("product", "Cotton drawer liner", "on_hand", 64, "reorder_at", 30), Expect: "completed"},
		},
	},
	{
		Slug: "operations-health-digest", Name: "Operations health digest", CrewSlug: "ops", AuthorAgentSlug: "morgan", Folder: "reports",
		Description: "Evening roll-up of the day's routine runs, failures and cost.",
		Schedule:    &TourSchedule{Name: "Weekdays 18:00", Cron: "0 18 * * 1-5", Timezone: tourTimezone},
		Definition: tourDefinition("operations-health-digest", "Operations health digest", "Evening roll-up of the day's routine runs, failures and cost.", nil,
			map[string]interface{}{"id": "stats", "type": "query", "query": map[string]interface{}{"source": "pipeline_runs", "window_hours": 24}},
			tourTransform("summary", "{{ steps.stats.output }}", ".summary_md", "stats"),
			tourNotify("post", "Operations health digest", "{{ steps.summary.output }}", "low", "routines.completed", "summary"),
		),
		// Last on purpose: its digest counts the runs created above it.
		Runs: []TourRun{{ViaSchedule: true, Expect: "completed"}},
	},
}

// TourProjects are created when missing; the four business projects already
// exist after a default seed and are reused by name.
var TourProjects = []ProjectDef{
	{Name: "Sales", Color: "cyan", Icon: "handshake", Status: "in_progress", Priority: "medium"},
	{Name: "Finance", Color: "amber", Icon: "wallet", Status: "in_progress", Priority: "medium"},
	{Name: "Marketing", Color: "violet", Icon: "megaphone", Status: "in_progress", Priority: "medium"},
	{Name: "Shipping", Color: "blue", Icon: "truck", Status: "in_progress", Priority: "medium"},
	{Name: "Warehouse move", Color: "green", Icon: "package", Status: "planned", Priority: "high"},
}

var TourLabels = []LabelDef{
	{Name: "Customer request", Color: "#0EA5E9"},
	{Name: "Supplier", Color: "#F59E0B"},
	{Name: "Recurring", Color: "#8B5CF6"},
}

// TourIssue is one card of the tour board. Comments are posted in order by
// the seeding owner; none mentions an agent, so no comment wakes one.
type TourIssue struct {
	CrewSlug, Project, Title, Description, Priority, State, Assignee string
	Labels                                                           []string
	Comments                                                         []string
}

var TourIssues = []TourIssue{
	// Sales
	{CrewSlug: "engineering", Project: "Sales", Title: "Quote 200 gift boxes for Lindqvist Café", Priority: "high", State: "TODO", Assignee: "sam", Labels: []string{"Customer request"},
		Description: "Lindqvist Café wants 200 small gift boxes with their logo for the December market. They asked for a price and a delivery date by Friday.",
		Comments:    []string{"Printing the logo adds about a week. Let's quote delivery for 2 December to be safe."}},
	{CrewSlug: "engineering", Project: "Sales", Title: "Follow up on the Brightwell Hotels trial order", Priority: "medium", State: "IN_PROGRESS", Assignee: "sam", Labels: []string{"Customer request"},
		Description: "Brightwell Hotels received 40 storage boxes as a trial for two of their hotels. Ask how the housekeeping teams like them before the 30-day window ends."},
	{CrewSlug: "engineering", Project: "Sales", Title: "Update the wholesale price list for Q4", Priority: "medium", State: "REVIEW", Assignee: "alex",
		Description: "Oak and linen prices went up 6 % from the supplier. Update the wholesale sheet and check the margins on the three best sellers.",
		Comments:    []string{"Draft is in the shared folder. Margins stay above 30 % on everything except the small linen basket."}},
	{CrewSlug: "engineering", Project: "Sales", Title: "Answer the bulk-discount question from Oakline Interiors", Priority: "low", State: "DONE", Assignee: "sam", Labels: []string{"Customer request"},
		Description: "Oakline asked whether orders above 100 boxes get a discount. Offer the standard 8 % wholesale tier."},
	{CrewSlug: "engineering", Project: "Sales", Title: "Draft a welcome email for new business customers", Priority: "low", State: "BACKLOG",
		Description: "A short welcome with the wholesale price list, delivery times and who to contact."},
	// Shipping
	{CrewSlug: "engineering", Project: "Shipping", Title: "Rebook the delayed pallet to Rotterdam", Priority: "urgent", State: "IN_PROGRESS", Assignee: "robin", Labels: []string{"Supplier"},
		Description: "The carrier missed Tuesday's pickup. The customer needs the pallet by Friday; rebook with the express service if the regular one cannot make it.",
		Comments:    []string{"Express pickup is confirmed for tomorrow 10:00. Extra cost is EUR 64, the carrier will credit half."}},
	{CrewSlug: "engineering", Project: "Shipping", Title: "Compare courier rates for parcels over 5 kg", Priority: "medium", State: "TODO", Assignee: "robin",
		Description: "Heavy parcels are 18 % of orders but a third of shipping cost. Get quotes from two other couriers."},
	{CrewSlug: "engineering", Project: "Shipping", Title: "Claim compensation for the damaged SHIP-112 delivery", Priority: "high", State: "REVIEW", Assignee: "robin", Labels: []string{"Customer request"},
		Description: "Two boxes in SHIP-112 arrived crushed. Photos are attached to the customer's email; file the claim with the carrier."},
	{CrewSlug: "engineering", Project: "Shipping", Title: "Print return labels for the October batch", Priority: "low", State: "DONE", Assignee: "robin", Labels: []string{"Recurring"},
		Description: "Eleven returns were approved this month. Print the labels and email them to the customers."},
	{CrewSlug: "engineering", Project: "Shipping", Title: "Move the Friday pickup to 15:00", Priority: "low", State: "CANCELLED",
		Description: "The carrier cannot offer a later slot on Fridays, so the pickup stays at 13:00."},
	// Finance
	{CrewSlug: "quality", Project: "Finance", Title: "Reconcile September card payments", Priority: "high", State: "IN_PROGRESS", Assignee: "jordan", Labels: []string{"Recurring"},
		Description: "The card processor's payout is EUR 112 lower than the orders it covers. Find the refunds or fees that explain the gap."},
	{CrewSlug: "quality", Project: "Finance", Title: "Chase unpaid invoice INV-2041 from Marlow & Finch", Priority: "high", State: "TODO", Assignee: "jordan", Labels: []string{"Customer request"},
		Description: "INV-2041 (EUR 640) is 21 days past due. The weekly reminder routine is waiting for approval to send the reminder."},
	{CrewSlug: "quality", Project: "Finance", Title: "Prepare the Q3 VAT summary for the accountant", Priority: "medium", State: "REVIEW", Assignee: "jordan",
		Description: "Export sales and purchases for July to September and send the summary to the accountant before the 25th."},
	{CrewSlug: "quality", Project: "Finance", Title: "Fix the bank feed import for the business account", Priority: "urgent", State: "TODO",
		Description: "This morning's bank feed reconciliation failed: the statement arrived without a total. Ask the bank to resend it, then run the routine again.",
		Comments:    []string{"The bank says the export was cut off by their maintenance window. A new file is promised for this afternoon."}},
	{CrewSlug: "quality", Project: "Finance", Title: "Archive paid supplier invoices for August", Priority: "low", State: "DONE", Assignee: "jordan", Labels: []string{"Recurring", "Supplier"},
		Description: "Move the August supplier invoices to the archive folder once each one is matched to a payment."},
	// Marketing
	{CrewSlug: "quality", Project: "Marketing", Title: "Plan the autumn newsletter", Priority: "medium", State: "IN_PROGRESS", Assignee: "casey",
		Description: "Theme: getting the house ready for winter. Feature the linen baskets and the new oak drawer set."},
	{CrewSlug: "quality", Project: "Marketing", Title: "Refresh product photos on the storage boxes page", Priority: "low", State: "BACKLOG", Assignee: "casey",
		Description: "Three of the photos still show the old packaging."},
	{CrewSlug: "quality", Project: "Marketing", Title: "Review results of the September discount campaign", Priority: "medium", State: "DONE", Assignee: "casey",
		Description: "The 10 % code ran for two weeks. Compare orders and average basket against August.",
		Comments:    []string{"Orders up 22 %, average basket down EUR 4. Worth repeating before Christmas with a minimum order value."}},
	{CrewSlug: "quality", Project: "Marketing", Title: "Add the sign-up form to the blog footer", Priority: "low", State: "TODO", Assignee: "casey",
		Description: "Most new subscribers come from the blog, but the form is only on the home page."},
	// Warehouse move
	{CrewSlug: "ops", Project: "Warehouse move", Title: "Count stock before the warehouse move", Priority: "high", State: "TODO", Assignee: "morgan",
		Description: "Full count on the 10th so the new warehouse starts with correct numbers."},
	{CrewSlug: "ops", Project: "Warehouse move", Title: "Book a moving company for 14 November", Priority: "medium", State: "IN_PROGRESS", Assignee: "morgan", Labels: []string{"Supplier"},
		Description: "Two quotes are in. The move needs a truck with a tail lift and three people for one day."},
	{CrewSlug: "ops", Project: "Warehouse move", Title: "Label the shelves in the new warehouse", Priority: "low", State: "BACKLOG",
		Description: "Use the same aisle and shelf codes as now so the pick lists stay valid."},
}
