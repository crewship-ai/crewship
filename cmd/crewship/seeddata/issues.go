package seeddata

// LabelDef defines a label to seed.
type LabelDef struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

var Labels = []LabelDef{
	{Name: "Network", Color: "#3B82F6"},
	{Name: "Filesystem", Color: "#22C55E"},
	{Name: "Scripting", Color: "#A855F7"},
	{Name: "Data", Color: "#F97316"},
	{Name: "Monitoring", Color: "#EAB308"},
	{Name: "Web", Color: "#06B6D4"},
	{Name: "Automation", Color: "#EC4899"},
	{Name: "Quick", Color: "#6B7280"},
}

// ProjectDef defines a project to seed.
type ProjectDef struct {
	Name     string
	Color    string
	Icon     string
	Status   string
	Priority string
}

var Projects = []ProjectDef{
	{Name: "Network Probes", Color: "#3B82F6", Icon: "wifi", Status: "in_progress", Priority: "high"},
	{Name: "File Operations", Color: "#22C55E", Icon: "file-text", Status: "in_progress", Priority: "high"},
	{Name: "Web Scraping", Color: "#06B6D4", Icon: "globe", Status: "in_progress", Priority: "medium"},
	{Name: "Script Factory", Color: "#A855F7", Icon: "terminal", Status: "in_progress", Priority: "medium"},
	{Name: "System Info", Color: "#F97316", Icon: "cpu", Status: "planned", Priority: "low"},
}

// IssueDef defines an issue to seed.
// These are REAL executable tasks that agents run inside containers.
// Keep them simple, idempotent, and safe to run 100x.
type IssueDef struct {
	CrewSlug    string
	Assignee    string // agent slug — resolved to ID during seed
	Title       string
	Description string
	Priority    string
	Project     string // project name (resolved to ID during seed)
	TargetState string // final status after creation (empty = BACKLOG)
	Comment     string
}

var Issues = []IssueDef{
	// ── Network Probes: test container networking ──
	{
		CrewSlug: "engineering", Assignee: "martin", Project: "Network Probes", Priority: "high",
		Title: "Ping google.com 5 times and save results",
		Description: `Run ping -c 5 google.com inside the container.
Save the full output to /tmp/ping-google.txt.
Report the average round-trip time in a summary.`,
	},
	{
		CrewSlug: "engineering", Assignee: "martin", Project: "Network Probes", Priority: "medium",
		Title: "Check HTTP status of 5 popular websites",
		Description: `Use curl to check the HTTP status code of these URLs:
- https://google.com
- https://github.com
- https://api.anthropic.com
- https://httpbin.org/status/200
- https://httpbin.org/status/404

Save results as a CSV file: /tmp/http-status.csv with columns: url,status_code,response_time_ms`,
	},
	{
		CrewSlug: "devops", Assignee: "radek", Project: "Network Probes", Priority: "medium",
		Title: "Trace DNS resolution for 3 domains",
		Description: `Resolve these domains and save the results:
- api.anthropic.com
- github.com
- registry.npmjs.org

For each domain, record the IP addresses returned.
Save output to /tmp/dns-results.txt with format: domain -> ip1, ip2, ...`,
	},
	{
		CrewSlug: "devops", Assignee: "radek", Project: "Network Probes", Priority: "low",
		Title: "Measure download speed with a 1MB test file",
		Description: `Download a 1MB test file from https://httpbin.org/bytes/1048576 using curl.
Measure and report the download time and speed.
Save the timing info to /tmp/speed-test.txt.
Delete the downloaded file after measuring.`,
	},

	// ── File Operations: test container filesystem ──
	{
		CrewSlug: "engineering", Assignee: "viktor", Project: "File Operations", Priority: "high",
		Title: "Create a directory tree with sample files",
		Description: `Create the following directory structure under /tmp/demo-project/:
  /tmp/demo-project/
    README.md        (with project name and date)
    src/
      main.py        (simple hello world)
      utils.py       (a helper function)
    tests/
      test_main.py   (a basic test)
    data/
      config.json    (sample config with 3 keys)

Verify all files exist and list the tree.`,
	},
	{
		CrewSlug: "engineering", Assignee: "nela", Project: "File Operations", Priority: "medium",
		Title: "Generate a CSV report with random data",
		Description: `Create a Python script that generates a CSV file with 50 rows of sample data.
Columns: id, name, email, score (random 0-100), status (active/inactive).
Save the script as /tmp/generate-data.py and run it.
Output CSV should be at /tmp/sample-data.csv.
Print the first 5 rows and a summary (total rows, average score).`,
	},
	{
		CrewSlug: "quality", Assignee: "petra", Project: "File Operations", Priority: "medium",
		Title: "Write a log parser that extracts errors",
		Description: `Create a bash script /tmp/log-parser.sh that:
1. Generates a sample log file /tmp/app.log with 100 lines (mix of INFO, WARN, ERROR)
2. Extracts all ERROR lines into /tmp/errors.txt
3. Counts occurrences of each log level
4. Prints a summary: total lines, errors, warnings, info

Run the script and verify the output.`,
	},

	// ── Web Scraping: test network + data processing ──
	{
		CrewSlug: "research", Assignee: "filip", Project: "Web Scraping", Priority: "high",
		Title: "Fetch and parse the Hacker News front page",
		Description: `Use curl to download https://news.ycombinator.com/ (HTML).
Parse the response to extract the top 10 story titles.
Save to /tmp/hackernews-top10.txt with format:
  1. Story title here
  2. Another story
  ...

Note: Use grep/sed/awk for parsing, no external libraries needed.`,
	},
	{
		CrewSlug: "research", Assignee: "filip", Project: "Web Scraping", Priority: "medium",
		Title: "Fetch current weather data from wttr.in",
		Description: `Use curl to fetch weather for Prague from wttr.in:
  curl -s "wttr.in/Prague?format=j1"

Parse the JSON response and extract:
- Current temperature
- Weather description
- Wind speed
- Humidity

Save a human-readable summary to /tmp/weather-prague.txt`,
	},
	{
		CrewSlug: "research", Assignee: "lucie", Project: "Web Scraping", Priority: "low",
		Title: "Download and analyze a public JSON API",
		Description: `Fetch a list of users from https://jsonplaceholder.typicode.com/users
Parse the JSON and create a report /tmp/users-report.txt with:
- Total number of users
- List of all usernames and cities
- Users grouped by company name

Use curl + python3 or jq for parsing.`,
	},

	// ── Script Factory: test code execution in container ──
	{
		CrewSlug: "engineering", Assignee: "viktor", Project: "Script Factory", Priority: "high",
		Title: "Write a Python script that generates Fibonacci",
		Description: `Create /tmp/fibonacci.py that:
1. Takes an argument N (default 20)
2. Prints the first N Fibonacci numbers
3. Calculates the golden ratio approximation from the last two numbers

Run it with N=20 and save output to /tmp/fibonacci-output.txt`,
	},
	{
		CrewSlug: "engineering", Assignee: "nela", Project: "Script Factory", Priority: "medium",
		Title: "Create a bash system info collector",
		Description: `Write /tmp/sysinfo.sh that collects and saves to /tmp/sysinfo.txt:
- Hostname and OS info (uname -a)
- Current date and uptime
- Disk usage (df -h)
- Memory usage (free -m or /proc/meminfo)
- Running processes count
- Network interfaces and IPs
- Environment variables count

Make it executable and run it.`,
	},
	{
		CrewSlug: "quality", Assignee: "daniel", Project: "Script Factory", Priority: "medium",
		Title: "Write and run a simple test suite in bash",
		Description: `Create /tmp/test-suite.sh — a minimal test framework in bash.
Define at least 5 test cases:
1. Test that /tmp exists and is writable
2. Test that curl is available
3. Test that python3 is available
4. Test that DNS resolution works (nslookup google.com)
5. Test that /etc/hostname is readable

Output format: [PASS] or [FAIL] for each test, with a summary at the end.
Run the test suite and save results to /tmp/test-results.txt`,
	},

	// ── System Info: test container environment ──
	{
		CrewSlug: "devops", Assignee: "ondrej", Project: "System Info", Priority: "low",
		Title: "Inventory all installed tools in the container",
		Description: `Check which common tools are available in the container:
curl, wget, python3, node, git, jq, sed, awk, grep, tar, gzip, ssh, ping, nslookup, dig

Save a report to /tmp/tool-inventory.txt with:
- Tool name, path, version (if available)
- Mark as AVAILABLE or MISSING

This helps us know what agents can use.`,
	},
	{
		CrewSlug: "devops", Assignee: "jakub", Project: "System Info", Priority: "low",
		Title: "Map container resource limits and environment",
		Description: `Collect container environment info and save to /tmp/container-info.txt:
- CPU count (nproc)
- Memory total and available (/proc/meminfo)
- Disk space (df -h /)
- Container ID (hostname or /proc/1/cgroup)
- User ID and groups (id)
- Network interfaces (ip addr or ifconfig)
- Mounted filesystems (mount)
- Key environment variables (HOME, PATH, USER)`,
	},
}

// StatusPath returns the sequence of status transitions needed to reach
// target from BACKLOG.
func StatusPath(target string) []string {
	switch target {
	case "TODO":
		return []string{"TODO"}
	case "IN_PROGRESS":
		return []string{"TODO", "IN_PROGRESS"}
	case "REVIEW":
		return []string{"TODO", "IN_PROGRESS", "REVIEW"}
	case "DONE":
		return []string{"TODO", "IN_PROGRESS", "REVIEW", "DONE"}
	case "FAILED":
		return []string{"TODO", "IN_PROGRESS", "FAILED"}
	case "CANCELLED":
		return []string{"TODO", "CANCELLED"}
	case "DUPLICATE":
		return []string{"TODO", "DUPLICATE"}
	default:
		return nil
	}
}

// validIssueTransitions mirrors the server-side issue status DAG defined in
// internal/api/issue_handler.go. Keep in sync.
var validIssueTransitions = map[string][]string{
	"BACKLOG":     {"TODO", "IN_PROGRESS", "CANCELLED"},
	"TODO":        {"IN_PROGRESS", "BACKLOG", "CANCELLED"},
	"IN_PROGRESS": {"REVIEW", "DONE", "FAILED", "CANCELLED", "TODO"},
	"REVIEW":      {"DONE", "TODO", "IN_PROGRESS", "FAILED", "CANCELLED"},
	"DONE":        {"BACKLOG"},
	"FAILED":      {"BACKLOG", "TODO", "IN_PROGRESS"},
	"CANCELLED":   {"BACKLOG", "TODO"},
	"DUPLICATE":   {},
}

// StatusPathFrom returns the shortest sequence of status transitions needed
// to move an issue from current to target, using the server-side issue
// status DAG. The returned slice does NOT include current and DOES include
// target as its final element. If current == target, returns an empty
// non-nil slice. Returns nil when no valid path exists.
func StatusPathFrom(current, target string) []string {
	if current == target {
		return []string{}
	}
	if _, ok := validIssueTransitions[current]; !ok {
		return nil
	}
	if _, ok := validIssueTransitions[target]; !ok {
		return nil
	}
	// BFS over the transition graph.
	type node struct {
		status string
		path   []string
	}
	visited := map[string]bool{current: true}
	queue := []node{{status: current, path: nil}}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, next := range validIssueTransitions[n.status] {
			if visited[next] {
				continue
			}
			visited[next] = true
			nextPath := make([]string, len(n.path)+1)
			copy(nextPath, n.path)
			nextPath[len(n.path)] = next
			if next == target {
				return nextPath
			}
			queue = append(queue, node{status: next, path: nextPath})
		}
	}
	return nil
}
