#!/usr/bin/env bash
#
# fleet-watch.sh — a real producer for the `flotila` page.
#
# This is what docs/prd/pages.md §0 means by "the page has no datasource".
# The script runs next to the thing it measures, computes whatever it likes
# with whatever it has, and pushes a typed payload. The page holds no query,
# no connection string and no credential; the only way data gets in is a
# producer calling:
#
#     crewship page set <page>/<panel> --data -
#
# Everything the page shows about provenance — who produced it, under which
# run, and when — is attached by the server. This script cannot claim any of
# it, and a `produced_at` in the payload would be refused: the freshness clock
# is the server's (§4 rule 2).
#
# Auth: a CLI token in CREWSHIP_TOKEN. A producer inside a crew container will
# eventually reach the sidecar instead, so the agent process never holds a
# credential at all — that path is issue #1946 and is not built yet.
#
#   CREWSHIP_SERVER=http://localhost:8083 \
#   CREWSHIP_TOKEN=... CREWSHIP_WORKSPACE=... \
#   ./fleet-watch.sh            # one pass
#   ./fleet-watch.sh --loop 30  # every 30s
#
set -euo pipefail

PAGE=${PAGE:-flotila}
CS=${CS:-/tmp/crewship-3-dev}
CLONES=(1 2 3)

push() {
  local panel=$1 payload=$2
  # The push is the only thing that can fail in a way worth reporting: a 4xx
  # here means the payload broke the panel's schema, and printing the server's
  # own sentence is more useful than anything this script could invent.
  if ! printf '%s' "$payload" | "$CS" page set "$PAGE/$panel" --data - >/dev/null 2>"/tmp/fleet-watch-$panel.err"; then
    echo "  $panel: $(head -c 200 "/tmp/fleet-watch-$panel.err")" >&2
    return 1
  fi
  echo "  $panel: ok"
}

# ── status.v1 ──────────────────────────────────────────────────────────────
# state is a closed set: ok | warning | critical. The renderer draws a glyph
# AND a word for each, never colour alone, so a colour-blind reader loses
# nothing (§3).
collect_status() {
  local items="[]"
  for n in "${CLONES[@]}"; do
    local state label
    if systemctl is-active --quiet "crewship-ws@$n"; then
      state=ok;       label="běží"
    else
      state=critical; label="stojí"
    fi
    items=$(jq -c --arg n "crewship-ws@$n" --arg s "$state" --arg l "$label" \
      '. + [{name:$n, state:$s, label:$l}]' <<<"$items")
  done

  # Disk: a threshold turns a number into a state, which is the whole job of
  # this panel type. 90% is critical because that is where SQLite starts
  # failing writes, not because it is a round number.
  local used; used=$(df --output=pcent / | tail -1 | tr -dc '0-9')
  local dstate=ok dlabel="${used}% využito"
  if   [ "$used" -ge 90 ]; then dstate=critical; dlabel="${used}% — dochází místo"
  elif [ "$used" -ge 75 ]; then dstate=warning;  dlabel="${used}% využito"
  fi
  items=$(jq -c --arg s "$dstate" --arg l "$dlabel" '. + [{name:"disk /", state:$s, label:$l}]' <<<"$items")

  jq -nc --argjson i "$items" '{items: $i}'
}

# ── metric.v1 ──────────────────────────────────────────────────────────────
# The sparkline is kept in a file because §11b.16 says points are evenly
# spaced BY CONTRACT — so the producer, not the panel, is responsible for
# only sending points it took at an even cadence.
collect_memory() {
  local free_pct hist=/tmp/fleet-watch-mem.hist
  free_pct=$(free | awk '/Mem:/ {printf "%d", $7/$2*100}')

  echo "$free_pct" >>"$hist"
  tail -30 "$hist" >"$hist.tmp" && mv "$hist.tmp" "$hist"
  local spark; spark=$(jq -sc '.' <"$hist")

  # A delta needs something to compare with. On the first pass there is
  # nothing, and the field is then OMITTED rather than sent as null — the
  # schema refuses a null delta on purpose, because "no change" (0) and
  # "nothing to compare with" (absent) are different claims and the panel
  # draws them differently. The server said so the first time this script ran:
  #
  #   payload does not satisfy metric.v1: '/delta' expected number, but got null
  #
  # delta_good: up is good for free memory. Without it the arrow renders
  # muted, because a green arrow pointing up is a lie on an error rate and the
  # payload is the only thing that knows which metric this is (§11b.9).
  local base='{value: $v, unit: "%", delta_good: "up", target: 40, sparkline: $s}'
  if [ "$(wc -l <"$hist")" -gt 1 ]; then
    local prev delta
    prev=$(tail -2 "$hist" | head -1)
    delta=$((free_pct - prev))
    jq -nc --argjson v "$free_pct" --argjson d "$delta" --argjson s "$spark" \
      "$base + {delta: \$d}"
  else
    jq -nc --argjson v "$free_pct" --argjson s "$spark" "$base"
  fi
}

# ── series.v1 ──────────────────────────────────────────────────────────────
# One unit for the panel. Every series is MB, so they can share an axis;
# two units would have to be two panels (§3).
collect_disk_series() {
  local labels="[]" values="[]"
  for n in "${CLONES[@]}"; do
    local mb; mb=$(du -sm "/srv/crewship/crewship_$n" 2>/dev/null | cut -f1 || echo 0)
    labels=$(jq -c --arg l "crewship_$n" '. + [$l]' <<<"$labels")
    values=$(jq -c --argjson v "${mb:-0}" '. + [$v]' <<<"$values")
  done
  jq -nc --argjson l "$labels" --argjson v "$values" \
    '{unit: "MB", labels: $l, series: [{name: "na disku", values: $v}]}'
}

# ── table.v1 ───────────────────────────────────────────────────────────────
# Rows are objects keyed by the declared column keys. A cell we genuinely
# could not read is null — NOT an empty string, which would claim we measured
# emptiness (§9b.4).
collect_clones() {
  local rows="[]"
  for n in "${CLONES[@]}"; do
    local dir="/srv/crewship/crewship_$n" head branch up
    head=$(git -C "$dir" rev-parse --short HEAD 2>/dev/null || echo "")
    branch=$(git -C "$dir" rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
    up=$(systemctl show "crewship-ws@$n" -p ActiveEnterTimestamp --value 2>/dev/null || echo "")
    rows=$(jq -c \
      --arg c "crewship_$n" \
      --arg p "808${n}" \
      --argjson h "$([ -n "$head" ]   && jq -Rn --arg x "$head"   '$x' || echo null)" \
      --argjson b "$([ -n "$branch" ] && jq -Rn --arg x "$branch" '$x' || echo null)" \
      --argjson u "$([ -n "$up" ]     && jq -Rn --arg x "$up"     '$x' || echo null)" \
      '. + [{klon:$c, port:$p, head:$h, vetev:$b, od:$u}]' <<<"$rows")
  done
  jq -nc --argjson r "$rows" '{
    columns: [
      {key:"klon",  label:"Klon"},
      {key:"port",  label:"Port", align:"right"},
      {key:"vetev", label:"Větev"},
      {key:"head",  label:"HEAD"},
      {key:"od",    label:"Běží od"}
    ],
    rows: $r
  }'
}

# ── narrative.v1 ───────────────────────────────────────────────────────────
# Paragraphs and lists. There is no heading kind, no bold, no image field and
# no URL field — §8 rules 1-3 keep them out of the SCHEMA rather than
# sanitising them out of a payload, because CamoLeak exfiltrated through a
# trusted first-party image proxy and CSP did not stop it.
collect_narrative() {
  local down=0 names=""
  for n in "${CLONES[@]}"; do
    systemctl is-active --quiet "crewship-ws@$n" || { down=$((down+1)); names="$names crewship-ws@$n"; }
  done
  local used; used=$(df --output=pcent / | tail -1 | tr -dc '0-9')

  local verdict text
  if [ "$down" -eq 0 ] && [ "$used" -lt 75 ]; then
    verdict="Nic nehoří"
    text="Všechny tři workstationy běží a na kořenovém svazku je ${used} % využito. Nic nevyžaduje pozornost."
  elif [ "$down" -gt 0 ]; then
    verdict="$down workstation(ů) stojí"
    text="Neběží:${names}. Klon je to, co je v něm checknuté — restart ho vrátí na tentýž commit, takže restartovat je bezpečné, ale neřekne to, proč spadl."
  else
    verdict="Dochází místo"
    text="Workstationy běží, ale kořenový svazek je na ${used} %. SQLite začne selhávat na zápisech dřív, než se disk zaplní úplně."
  fi

  jq -nc --arg v "$verdict" --arg t "$text" --arg u "$used" '{
    verdict: $v,
    blocks: [
      {kind: "paragraph", text: $t},
      {kind: "list", text: "Co skript měří: stav tří systemd unit, využití kořenového svazku, volnou paměť, velikost každého klonu a commit, který v něm běží."},
      {kind: "paragraph", text: "Tento text píše skript, ne agent. Kdyby ho psal agent, platila by pro něj stejná schémata — a proto v nich není pole pro obrázek ani pro odkaz."}
    ]
  }'
}

one_pass() {
  echo "$(date -u +%H:%M:%S) push → $PAGE"
  push sluzby "$(collect_status)"       || true
  push pamet  "$(collect_memory)"       || true
  push disk   "$(collect_disk_series)"  || true
  push klony  "$(collect_clones)"       || true
  push rozbor "$(collect_narrative)"    || true
}

if [ "${1:-}" = "--loop" ]; then
  interval=${2:-30}
  # The server floors pushes at one per 2s per panel and refuses faster ones
  # with 429 (§10b.3), so a loop tighter than that is not a way to get more
  # data — it is a way to get errors.
  while :; do one_pass; sleep "$interval"; done
else
  one_pass
fi
