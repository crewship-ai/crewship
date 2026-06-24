#!/usr/bin/env bash
# Release-demo runtime walkthrough.
#
# `crewship seed` builds the whole workspace declaratively (crews, agents,
# 18 routines, schedules, memory, credentials, integrations). But some
# subsystems only come alive at RUNTIME — they can't be pre-seeded:
#   - lead hiring an ephemeral agent (+ autonomy approval gate)
#   - lead → peer delegation (/assign)
#   - an agent escalating for a credential, a human granting it
#   - memory write → recall across sessions
#   - a routine finishing → a notification landing
#   - cross-tier consistency (Haiku vs Opus) on the recipe library
#
# This script exercises exactly those, against a running dev server.
# It's a guided checklist: run it top-to-bottom, or copy individual blocks.
#
# Prereqs: ./dev.sh start is up; `crewship seed --nuke --with-memory
# --with-users --wait-provision` has completed; SEED_ANTHROPIC_API_KEY (and
# optionally SEED_GITHUB_TOKEN) were set during seed.
set -euo pipefail

CREWSHIP="${CREWSHIP:-./crewship}"
SERVER="${SERVER:-http://localhost:8081}"
cs() { "$CREWSHIP" --server "$SERVER" "$@"; }

echo "== 0. Sanity: workspace, crews, agents, routines =="
cs config workspace
cs crew list
cs agent list
cs routine list

echo "== 1. Memory: write → recall across sessions → FTS =="
cs --agent alex -q -p "Remember this fact: the release ship-date is 2026-07-15."
# A fresh ask is a new session — recall must come from persisted memory.
cs --agent alex -q -p "What is the release ship-date? Answer with just the date."
cs memory search "ship-date"
cs memory status

echo "== 2. Lead → peer delegation (/assign) =="
# Alex (Engineering lead) decomposes and delegates to a peer (Sam).
cs --agent alex -q -p "Delegate to Sam: write a one-line healthcheck for our API, then report Sam's answer back to me."

echo "== 3. Ephemeral hire → (autonomy gate) → it expires =="
# --template is a CREW-template slug (see `crewship template list`), e.g.
# devops-sre / api-integrations. If the crew autonomy is 'guided', the hire
# lands as PENDING_REVIEW with a blocking inbox "waitpoint" item.
# NOTE: approving a hire waitpoint currently has no working CLI path
# (`approvals approve <inbox-id>` 404s; the item isn't in `approvals list`) —
# approve it from the UI inbox. Verified gap on dev1, 2026-06-24.
cs hire --crew ops --template devops-sre --reason "spike: prototype a rate-limiter" --ttl 30 --yes || \
  echo "  (hire may be gated by crew autonomy_level — check: cs inbox list)"
cs agent list
cs inbox list

echo "== 4. Escalation for a credential → human grants it =="
# Drive an agent to hit a wall that needs access it doesn't have. The agent
# raises an escalation (type=CREDENTIAL); a human then creates + assigns it.
cs --agent morgan -q -p "You need a PagerDuty API token to page on-call but you don't have one. Raise a credential escalation describing exactly what you need."
cs escalation list --crew ops
# Human grants it (the part an autonomous agent intentionally CANNOT do itself):
cs credential create --name PAGERDUTY_TOKEN --type API_KEY --provider PAGERDUTY --value "demo-token-rotate-me" || true
cs credential assign PAGERDUTY_TOKEN morgan --env-var-name PAGERDUTY_TOKEN || true
cs credential list

echo "== 5. GitHub credential injection (only if SEED_GITHUB_TOKEN was set) =="
# The seed assigns a CLI_TOKEN named GH_TOKEN; inside the crew container it is
# mounted as a file + env GH_TOKEN, which the in-container `gh` reads.
cs --agent sam -q -p "Run 'gh auth status' in your container and report whether GitHub auth is configured."

echo "== 6. Run a recipe → watch for the completion notification =="
cs routine run extract-contacts
cs routine run classify-ticket
# Block until the next completion event arrives (Ctrl-C to stop):
cs routine watch classify-ticket --once || true
cs inbox list

echo "== 7. Cross-tier consistency: the recipe library on Haiku vs Opus =="
# The core of the 'Opus authors, Haiku runs, identical output' guarantee.
cs eval scenarios --runs 3 --tiers fast,smart -f markdown

echo "== 8. Scheduler: the seeded cron schedules =="
cs routine schedules list
# Force-fire one out of cycle to see it on the activity rail:
cs routine schedules now "$(cs routine schedules list --json | jq -r '.[0].id // empty')" || true

echo "== walkthrough complete =="
