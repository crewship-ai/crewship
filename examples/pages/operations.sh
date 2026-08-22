#!/usr/bin/env bash
#
# operations.sh — the producer for the seeded `operations` page.
#
# `crewship seed` creates that page and pushes one demo payload per panel, so a
# fresh workspace opens on something rather than on five panels reading "never
# produced". Those payloads are a snapshot and nothing refreshes them. This is
# what refreshes them: point it at your server and the same five panels start
# reporting the machine this script runs on.
#
# It is deliberately unremarkable. docs/prd/pages.md §1 argues that adding a
# data source to Pages is a scripting job rather than connector engineering, and
# this is what that claim looks like when it is true: `free`, `uptime`, `df`,
# one HTTP probe, and five pushes through the ordinary CLI. Nothing here imports
# Crewship, and the page holds no query, no credential and no connection string.
#
# WHY IT MAY WRITE THESE PANELS. Every panel declares `producer: script/operations.sh`,
# and `script` is the one producer kind where the page's OWNER may also push
# (internal/api/pages_authz.go mayProduce) — the reasoning there is that
# "script/operations.sh is a name, not a principal", so the real principal is
# whichever human's CLI token invoked it. The seeded page is owned by crew/ops,
# and ownership counts every member of that crew, so any of them can run this,
# and any of them can also correct a panel by hand without stopping it.
#
# What that ALSO means: this script must run somewhere you control, with a CLI
# token. It is not the container path. A producer inside a crew container talks
# to the sidecar on localhost instead and arrives as an AGENT, so the panel it
# writes has to declare `producer: agent/<slug>` — see docs/guides/pages.mdx,
# "From inside a crew container".
#
#   export CREWSHIP_SERVER=http://localhost:8082
#   export CREWSHIP_TOKEN=...            # crewship login, or a CLI token
#   export CREWSHIP_WORKSPACE=...
#
#   ./operations.sh                  # one pass
#   ./operations.sh --loop 60        # every 60 s until interrupted
#
# Needs: crewship, jq, and the coreutils any Linux box already has.

set -euo pipefail

PAGE=${PAGE:-operations}
CS=${CS:-crewship}
STATE=${STATE:-${TMPDIR:-/tmp}/provoz-$PAGE.history}

# HISTORY is how many samples the sparkline and the series carry. Nine is what
# the seeded payloads use, and it is about what a narrow panel can draw without
# the points touching.
HISTORY=9

# SAMPLE_MINUTES is only used to LABEL the x axis, and it has to match the
# interval you actually run at or the labels lie. Pass INTERVAL and it is
# derived; the default assumes the documented --loop 60.
INTERVAL=${INTERVAL:-60}

die() { echo "operations.sh: $*" >&2; exit 1; }

# Checked from main(), AFTER the arguments are parsed: --help has to work on a
# machine that has neither, which is exactly the machine somebody is reading the
# help on to find out what it needs.
require_tools() {
  command -v jq >/dev/null 2>&1 || die "jq is required"
  command -v "$CS" >/dev/null 2>&1 || die "the crewship CLI is required (set CS to its path)"
}

# ── pushing ────────────────────────────────────────────────────────────────
# The push is the only thing here that can fail in a way worth reporting: a 4xx
# means the payload broke the panel's schema or this token is not allowed to
# write that panel, and the server names which. Printing its own sentence beats
# anything this script could invent, so the stderr is passed through.
push() {
  local panel=$1 payload=$2
  if ! printf '%s' "$payload" | "$CS" page set "$PAGE/$panel" --data - >/dev/null; then
    echo "  $panel: push refused (see above)" >&2
    return 1
  fi
  echo "  $panel: ok"
}

# push_failed reports that a MEASUREMENT failed, which is not the same as the
# push failing. §4 rule 2 makes "ok" or "failed" the only part of its own state a
# producer may assert; fresh and stale are the server's arithmetic. A panel whose
# producer ran and could not measure says so, rather than silently keeping the
# last good number until the SLA expires.
push_failed() {
  local panel=$1 payload=$2
  printf '%s' "$payload" | "$CS" page set "$PAGE/$panel" --data - --state failed >/dev/null || true
  echo "  $panel: reported failed"
}

# ── sampling ───────────────────────────────────────────────────────────────
# One line per pass: "<free MB> <load 1m> <load 5m>". Newest last, capped at
# HISTORY. A plain file rather than anything cleverer because the whole point of
# the producer story is that it is a shell script.
sample_and_remember() {
  local free_mb load1 load5
  free_mb=$(free -m | awk '/^Mem:/ {print $7 != "" ? $7 : $4}')
  read -r load1 load5 _ < /proc/loadavg
  printf '%s %s %s\n' "$free_mb" "$load1" "$load5" >> "$STATE"
  # Trim in place. tail into a temp file and move, so an interrupted trim
  # cannot leave the history empty.
  if [ "$(wc -l < "$STATE")" -gt "$HISTORY" ]; then
    tail -n "$HISTORY" "$STATE" > "$STATE.tmp" && mv "$STATE.tmp" "$STATE"
  fi
}

col() { awk -v c="$1" '{print $c}' "$STATE"; }

# json_numbers turns a column of the history into a JSON array of numbers.
json_numbers() { col "$1" | jq -Rsc 'split("\n") | map(select(length > 0) | tonumber)'; }

# ── labels ─────────────────────────────────────────────────────────────────
# Name the first tick, the last, and one in the middle; leave the rest null.
# `labels` accepts null for a tick the producer declines to name, which is what
# lets a nine-point window exist at all — this script cannot know how wide the
# panel is, so it names the few that carry meaning and lets the renderer decide
# how many of those it can fit.
build_labels() {
  local n=$1 mid=$(( $1 / 2 ))
  local i out="[]"
  for (( i = 0; i < n; i++ )); do
    local label=null
    if (( i == n - 1 )); then
      label='"now"'
    elif (( i == 0 )); then
      label="\"-$(( (n - 1) * INTERVAL / 60 ))m\""
    elif (( i == mid )); then
      label="\"-$(( (n - 1 - i) * INTERVAL / 60 ))m\""
    fi
    out=$(jq -c --argjson l "$label" '. + [$l]' <<<"$out")
  done
  printf '%s' "$out"
}

# ── panels ─────────────────────────────────────────────────────────────────

# status.v1 — `state` is a closed set of three, and the renderer draws a glyph
# AND a word for each, so nothing here is carried by colour alone.
panel_services() {
  local items='[]' state label

  # The API this script is about to push to. A producer that cannot reach it
  # will fail on the push anyway; measuring it first is what turns that into a
  # readable panel instead of a stack trace.
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    "${CREWSHIP_SERVER:-http://localhost:8080}/api/health" 2>/dev/null || echo 000)
  if [ "$code" = "200" ]; then state=ok; label="200 OK"
  else state=critical; label="not answering (HTTP $code)"; fi
  items=$(jq -c --arg s "$state" --arg l "$label" \
    '. + [{name:"api", state:$s, label:$l}]' <<<"$items")

  local used
  used=$(df -P / | awk 'NR==2 {gsub(/%/,"",$5); print $5}')
  if   [ "$used" -ge 90 ]; then state=critical
  elif [ "$used" -ge 75 ]; then state=warning
  else state=ok; fi
  items=$(jq -c --arg s "$state" --arg l "${used} % used" \
    '. + [{name:"disk /", state:$s, label:$l}]' <<<"$items")

  local pct
  pct=$(free -m | awk '/^Mem:/ {avail = ($7 != "" ? $7 : $4); printf "%d", avail * 100 / $2}')
  if   [ "$pct" -le 5 ];  then state=critical
  elif [ "$pct" -le 15 ]; then state=warning
  else state=ok; fi
  items=$(jq -c --arg s "$state" --arg l "${pct} % free" \
    '. + [{name:"memory", state:$s, label:$l}]' <<<"$items")

  local load1 cores
  read -r load1 _ < /proc/loadavg
  cores=$(nproc)
  # Load per core, in integer percent, so bash can compare it without bc.
  local per
  per=$(awk -v l="$load1" -v c="$cores" 'BEGIN {printf "%d", l * 100 / c}')
  if   [ "$per" -ge 200 ]; then state=critical
  elif [ "$per" -ge 100 ]; then state=warning
  else state=ok; fi
  items=$(jq -c --arg s "$state" --arg l "load $load1 across $cores cores" \
    '. + [{name:"load", state:$s, label:$l}]' <<<"$items")

  jq -nc --argjson items "$items" '{items: $items}'
}

# metric.v1 — one number with a delta and a sparkline. delta_good says which
# direction is the good one: free memory going UP is good, and without it the
# arrow renders muted rather than guessing.
panel_memory() {
  local spark now prev delta
  spark=$(json_numbers 1)
  now=$(jq -r '.[-1]' <<<"$spark")
  prev=$(jq -r 'if length > 1 then .[-2] else .[-1] end' <<<"$spark")
  delta=$(( now - prev ))
  jq -nc --argjson v "$now" --argjson d "$delta" --argjson s "$spark" \
    '{value: $v, unit: "MB", delta: $d, delta_good: "up", sparkline: $s}'
}

# series.v1 — ONE unit for the whole panel. Both series are load averages, so
# they share an axis; a second unit would have to be a second panel, because a
# dual axis is the most common way a chart lies.
panel_load() {
  local one five labels n
  one=$(json_numbers 2)
  five=$(json_numbers 3)
  n=$(jq 'length' <<<"$one")
  labels=$(build_labels "$n")
  jq -nc --argjson l "$labels" --argjson a "$one" --argjson b "$five" \
    '{unit: "load", labels: $l, series: [{name:"1m", values:$a}, {name:"5m", values:$b}]}'
}

# table.v1 — the columns are declared once and every row is keyed by them. A
# cell nobody could measure is null, which renders as an em dash; an empty
# string would claim we measured emptiness, which is a different statement.
panel_disks() {
  local raw rows
  # `df -P -T` columns are: Filesystem Type 1024-blocks Used Available Capacity
  # Mounted-on. Reordered here to mount, size, available, capacity, type so the
  # jq below reads positionally without re-deriving that layout.
  #
  # timeout, because df blocks indefinitely on a wedged network mount and a
  # producer that hangs is worse than one that reports a failure: the panel
  # would sit on its last good payload until the SLA expired, saying nothing.
  raw=$(timeout 10 df -P -T 2>/dev/null) || return 1
  rows=$(printf '%s\n' "$raw" \
    | awk 'NR > 1 && $2 !~ /^(tmpfs|devtmpfs|squashfs|overlay|autofs)$/ {print $7, $3, $5, $6, $2}' \
    | jq -R -s -c '
        def gib: (. | tonumber) / 1048576 * 10 | round / 10 | tostring | . + "G";
        split("\n")
        | map(select(length > 0) | split(" "))
        | map({
            mount:    .[0],
            size: (.[1] | gib),
            free:    (.[2] | gib),
            used:  (.[3] | rtrimstr("%") | tonumber),
            fs:       .[4]
          })')
  jq -nc --argjson rows "$rows" '{
    columns: [
      {key:"mount",    label:"Mount point"},
      {key:"size", label:"Size", align:"right"},
      {key:"free",    label:"Free",    align:"right"},
      {key:"used",  label:"Used",  align:"right"},
      {key:"fs",       label:"Filesystem"}
    ],
    rows: $rows
  }'
}

# narrative.v1 — typed blocks, plain text, and no field anywhere that could
# carry a URL or an image. That is not this script being careful; the schema has
# no such field, and a payload containing a URL is refused at the boundary. So
# do not interpolate $CREWSHIP_SERVER into any of this.
panel_review() {
  local first last trend verdict n
  n=$(wc -l < "$STATE")
  first=$(head -n1 "$STATE" | awk '{print $1}')
  last=$(tail -n1 "$STATE" | awk '{print $1}')
  local used pct
  used=$(df -P / | awk 'NR==2 {gsub(/%/,"",$5); print $5}')
  pct=$(free -m | awk '/^Mem:/ {avail = ($7 != "" ? $7 : $4); printf "%d", avail * 100 / $2}')

  # The first pass has nothing to compare against, and saying "unchanged" then
  # would be a claim rather than a measurement — the history is one point long.
  if [ "$n" -le 1 ]; then
    trend="First measurement since this producer started; ${last} MB free. A trend needs more passes."
    verdict="First measurement"
  elif [ "$last" -lt "$first" ]; then
    trend="Free memory fell from ${first} to ${last} MB over the last ${n} measurements."
    verdict="Memory is draining"
  elif [ "$last" -gt "$first" ]; then
    trend="Free memory rose from ${first} to ${last} MB over the last ${n} measurements."
    verdict="Memory is recovering"
  else
    trend="Free memory has not moved over the last ${n} measurements; it is holding at ${last} MB."
    verdict="No change"
  fi
  if [ "$pct" -le 15 ] || [ "$used" -ge 90 ]; then
    verdict="Running out of room"
  fi

  jq -nc --arg verdict "$verdict" --arg trend "$trend" \
    --arg rest "Disk / is ${used} % used, and ${pct} % of memory is free." '{
    verdict: $verdict,
    blocks: [
      {kind: "paragraph", text: $trend},
      {kind: "paragraph", text: $rest},
      {kind: "list", text: "What this page knows: the state of four things, free memory, the load average and the filesystems."},
      {kind: "list", text: "What it does not know: which process is holding that memory. Somebody has to look."}
    ]
  }'
}

# ── one pass ───────────────────────────────────────────────────────────────
pass() {
  sample_and_remember
  echo "$PAGE:"
  local failed=0

  # Each panel is pushed independently and a failure does not stop the rest. A
  # page with four panels reporting and one gone stale is a useful page, and an
  # honest one: it is exactly what a reader would see if one producer had died.
  push services "$(panel_services)" || failed=1
  push memory  "$(panel_memory)"  || failed=1
  push load  "$(panel_load)"  || failed=1

  # df can hang on a wedged network mount, which is a measurement failure and
  # not a push failure — so the panel is told, rather than left to go stale
  # while the other four look fine.
  local disks
  if disks=$(panel_disks); then
    push disks "$disks" || failed=1
  else
    # An empty `rows` is a measured "nothing matched" and renders the panel's
    # own empty-state sentence; --state failed is what says the producer ran and
    # could not measure. The two together are the honest report.
    push_failed disks '{"columns":[{"key":"mount","label":"Mount point"}],"rows":[]}'
    failed=1
  fi

  push review "$(panel_review)" || failed=1
  return $failed
}

main() {
  local loop=0
  while [ $# -gt 0 ]; do
    case $1 in
      --loop) loop=${2:?--loop needs an interval in seconds}; INTERVAL=$loop; shift 2 ;;
      # The header IS the help, read up to the first line that is not a comment
      # rather than to a line number — a hard-coded range prints `set -euo
      # pipefail` the moment somebody adds a paragraph above it.
      -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
      *) die "unknown argument: $1" ;;
    esac
  done

  require_tools

  # touch, not `: >` — the latter TRUNCATES, which would throw away the
  # sparkline and the series every time the script started, so every restart
  # would draw a chart with one point on it.
  touch "$STATE" 2>/dev/null || die "cannot write history at $STATE"

  if [ "$loop" -eq 0 ]; then
    pass
    exit $?
  fi
  while true; do
    pass || true
    sleep "$loop"
  done
}

main "$@"
