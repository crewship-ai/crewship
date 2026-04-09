package seeddata

import "fmt"

// SkillDef defines a skill to seed. Content is the system prompt extension
// that gets injected into the agent's prompt during task execution.
type SkillDef struct {
	Name        string
	Slug        string
	DisplayName string
	Category    string
	Description string
	Icon        string
	Content     string
}

// SkillMD returns the SKILL.md format (YAML frontmatter + content) required
// by the POST /api/v1/workspaces/{id}/skills/import endpoint.
func (s SkillDef) SkillMD() string {
	return fmt.Sprintf(`---
name: "%s"
display_name: "%s"
version: "1.0.0"
description: "%s"
category: "%s"
icon: "%s"
---

%s`, s.Name, s.DisplayName, s.Description, s.Category, s.Icon, s.Content)
}

// Skills with real content — these are system prompt extensions injected
// as <skill> blocks into the agent's prompt during execution.
var Skills = []SkillDef{
	{
		Name: "Network Probe", Slug: "network-probe", DisplayName: "Network Probe",
		Category: "DEVOPS", Icon: "wifi",
		Description: "Network diagnostics: ping, HTTP checks, DNS resolution, speed tests",
		Content: `# Network Probe

## When to Activate
- Task involves ping, HTTP requests, DNS resolution, or network diagnostics
- Keywords: "ping", "curl", "HTTP status", "DNS", "download", "speed"

## Instructions
1. Always use -c flag with ping to limit packet count (never unlimited)
2. For HTTP status checks use: curl -s -o /dev/null -w "%{http_code} %{time_total}s" <url>
3. For DNS resolution use: nslookup <domain> or dig +short <domain> (whichever is available)
4. Save all results to /tmp/ with descriptive filenames including a timestamp
5. Include timing information in all network measurements
6. Report summary statistics: average, min, max for timing data

## Output Format
- Save raw results to /tmp/{task-name}.txt
- Print a human-readable summary to stdout with key findings
- Use CSV format for tabular data (url,status,time)

## Guardrails
- Never ping more than 10 times per host (avoid flood)
- Always use timeouts: curl --connect-timeout 5 --max-time 10
- Only probe public endpoints — never scan internal IPs or port ranges
- If a host is unreachable, report it and continue (don't fail the whole task)`,
	},
	{
		Name: "File Crafter", Slug: "file-crafter", DisplayName: "File Crafter",
		Category: "CODING", Icon: "file-text",
		Description: "File and directory creation, CSV generation, data formatting",
		Content: `# File Crafter

## When to Activate
- Task involves creating files, directories, or structured data
- Keywords: "create", "file", "directory", "CSV", "JSON", "tree", "generate"

## Instructions
1. Create parent directories with mkdir -p before writing files
2. Use heredoc (cat << 'EOF') for multi-line file content
3. For CSV files: always include a header row with column names
4. For JSON files: ensure valid JSON (use python3 -m json.tool to validate)
5. After creating files, verify they exist with ls -la and show first few lines
6. Use chmod appropriately (755 for scripts, 644 for data files)

## Output Format
- Always show the created file tree with: find <dir> -type f | sort
- For generated data, show a preview: head -5 <file>
- Report file sizes and line counts: wc -l <file>

## Guardrails
- Write only to /tmp/ — never to system directories
- Don't create files larger than 10MB
- Always use descriptive filenames (not temp1.txt, file.dat)
- Clean up any temporary intermediate files`,
	},
	{
		Name: "Web Scraper", Slug: "web-scraper", DisplayName: "Web Scraper",
		Category: "RESEARCH", Icon: "globe",
		Description: "Web content fetching, HTML/JSON parsing, API data extraction",
		Content: `# Web Scraper

## When to Activate
- Task involves downloading web pages, parsing HTML, or calling APIs
- Keywords: "fetch", "scrape", "download", "parse", "API", "JSON", "HTML", "curl"

## Instructions
1. Always use curl -s (silent) with --max-time 15 for timeouts
2. For HTML parsing: use grep, sed, awk pipeline — no external libraries needed
3. For JSON parsing: prefer python3 -c "import json,sys; ..." or jq if available
4. Save raw responses to /tmp/ for debugging, processed output separately
5. Handle HTTP errors gracefully — check status code before parsing
6. Extract meaningful data, not raw HTML dumps

## Output Format
- Save structured results to /tmp/{descriptive-name}.txt
- For lists: number items (1. First item, 2. Second item)
- For JSON data: pretty-print with indentation
- Always include a summary line at the end: "Extracted N items from <source>"

## Guardrails
- Only fetch from public, well-known URLs (no random IPs)
- Respect rate limits — add 1s sleep between multiple requests to same host
- Never POST data or authenticate — read-only operations only
- If a URL is unreachable, report it and continue with remaining URLs`,
	},
	{
		Name: "Script Runner", Slug: "script-runner", DisplayName: "Script Runner",
		Category: "CODING", Icon: "terminal",
		Description: "Python and Bash script creation, execution, and output capture",
		Content: `# Script Runner

## When to Activate
- Task involves writing and executing Python or Bash scripts
- Keywords: "script", "Python", "Bash", "run", "execute", "generate", "calculate"

## Instructions
1. Write scripts to /tmp/ with descriptive names (not script.sh, test.py)
2. Add a shebang line: #!/usr/bin/env python3 or #!/bin/bash
3. Make scripts executable with chmod +x before running
4. For Python: use only stdlib modules (no pip install needed)
5. For Bash: use set -euo pipefail at the top for error safety
6. Capture output to both stdout and a file with: ./script.sh | tee /tmp/output.txt
7. Include comments explaining what each section does

## Output Format
- Show the script content before running it
- Run the script and show its output
- Save output to /tmp/{script-name}-output.txt
- Report: script location, exit code, output file location

## Guardrails
- Scripts must be idempotent — safe to run multiple times
- No infinite loops — always have exit conditions
- Don't install packages or modify system configuration
- Keep scripts under 100 lines — simple and readable
- If a script fails, show the error and explain what went wrong`,
	},
	{
		Name: "System Inspector", Slug: "system-inspector", DisplayName: "System Inspector",
		Category: "DEVOPS", Icon: "cpu",
		Description: "Container environment inspection, tool inventory, resource mapping",
		Content: `# System Inspector

## When to Activate
- Task involves checking container environment, installed tools, or system resources
- Keywords: "inventory", "container", "system info", "resource", "environment", "inspect"

## Instructions
1. Check tool availability with: command -v <tool> or which <tool>
2. Get versions with: <tool> --version 2>/dev/null || echo "version unknown"
3. Read system info from /proc/ when available (meminfo, cpuinfo)
4. Use standard tools: uname, df, free, nproc, id, hostname, ip/ifconfig
5. Handle missing tools gracefully — report as MISSING, don't fail

## Output Format
- Use aligned columns for readability:
  curl          /usr/bin/curl      7.88.1    AVAILABLE
  wget          -                  -         MISSING
- Group results by category (Network tools, Dev tools, System tools)
- Include a summary count: "Found N/M tools available"
- Save to /tmp/ with clear filename

## Guardrails
- Read-only inspection — never modify system configuration
- Don't access /etc/shadow or other sensitive files
- Stick to standard Linux inspection commands
- If /proc is not available, use alternative commands and note the limitation`,
	},
}

// SkillAssignments maps agent slug → list of skill slugs to assign.
// Assignments match agents to the demo issues they'll handle.
var SkillAssignments = map[string][]string{
	// Engineering — network probes, file ops, scripting
	"tomas":  {"network-probe", "script-runner", "file-crafter"},
	"viktor": {"script-runner", "file-crafter"},
	"nela":   {"file-crafter", "web-scraper"},
	"martin": {"network-probe", "system-inspector"},
	// Quality — testing, log parsing, validation
	"eva":    {"script-runner", "file-crafter"},
	"daniel": {"script-runner"},
	"petra":  {"file-crafter", "script-runner"},
	"jakub":  {"system-inspector"},
	// DevOps — network, system inspection
	"ondrej": {"network-probe", "system-inspector"},
	"radek":  {"network-probe", "system-inspector"},
	// Research — web scraping, data analysis
	"lucie": {"web-scraper", "script-runner"},
	"filip": {"web-scraper", "script-runner"},
}
