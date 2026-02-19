#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
PID_DIR="/tmp"
LOG_DIR="/tmp"

NEXT_PID_FILE="$PID_DIR/crewship-next.pid"
GO_PID_FILE="$PID_DIR/crewship-go.pid"
NEXT_LOG="$LOG_DIR/crewship-next.log"
GO_LOG="$LOG_DIR/crewship-go.log"

NEXT_PORT=3001
GO_PORT=8080

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

log()  { echo -e "${CYAN}[crewship]${NC} $*"; }
ok()   { echo -e "${GREEN}[crewship]${NC} $*"; }
warn() { echo -e "${YELLOW}[crewship]${NC} $*"; }
err()  { echo -e "${RED}[crewship]${NC} $*" >&2; }

is_running() {
  local pid_file="$1"
  if [[ -f "$pid_file" ]]; then
    local pid
    pid=$(cat "$pid_file")
    if kill -0 "$pid" 2>/dev/null; then
      echo "$pid"
      return 0
    fi
    rm -f "$pid_file"
  fi
  return 1
}

port_in_use() {
  lsof -ti:"$1" >/dev/null 2>&1
}

detect_db_mode() {
  if [[ -f "$PROJECT_DIR/.env.local" ]]; then
    local db_url
    db_url=$(grep -E '^DATABASE_URL=' "$PROJECT_DIR/.env.local" | head -1 | cut -d'=' -f2- | tr -d '"' || true)
    if [[ "$db_url" == postgresql://* ]] || [[ "$db_url" == postgres://* ]]; then
      echo "postgresql"
      return
    fi
  fi
  echo "sqlite"
}

check_prerequisites() {
  local missing=0

  if [[ ! -f "$PROJECT_DIR/.env.local" ]]; then
    err ".env.local not found -- copy from .env.example and fill in values"
    missing=1
  fi

  for cmd in node pnpm go; do
    if ! command -v "$cmd" &>/dev/null; then
      err "$cmd is not installed"
      missing=1
    fi
  done

  # Docker is only required if agents will be run
  if ! command -v docker &>/dev/null; then
    warn "docker is not installed -- agent containers will not work"
  fi

  if [[ $missing -ne 0 ]]; then
    err "Prerequisites check failed"
    exit 1
  fi
}

start_postgres() {
  local db_mode
  db_mode=$(detect_db_mode)

  if [[ "$db_mode" != "postgresql" ]]; then
    ok "Using SQLite (no PostgreSQL needed)"
    return
  fi

  log "Starting PostgreSQL (DATABASE_URL points to PostgreSQL)..."
  if docker compose -f "$PROJECT_DIR/docker/docker-compose.yml" ps --format '{{.Status}}' 2>/dev/null | grep -qi "up"; then
    ok "PostgreSQL already running"
  else
    docker compose -f "$PROJECT_DIR/docker/docker-compose.yml" up -d
    ok "PostgreSQL started"
  fi
}

start_go() {
  if pid=$(is_running "$GO_PID_FILE"); then
    ok "crewship already running (pid $pid)"
    return
  fi

  if port_in_use "$GO_PORT"; then
    err "Port $GO_PORT already in use -- stop the process first"
    return 1
  fi

  log "Starting crewship on :$GO_PORT..."
  mkdir -p /tmp/crewship-data /tmp/crewship-logs /tmp/crewship-state

  (
    cd "$PROJECT_DIR"
    set -a && . ./.env.local && set +a
    export CREWSHIP_NEXTJS_URL="http://localhost:$NEXT_PORT"
    # CREWSHIP_INTERNAL_TOKEN auto-generated at startup if not set
    export CREWSHIP_STORAGE_BASE_PATH=/tmp/crewship-data
    export CREWSHIP_LOG_PATH=/tmp/crewship-logs
    export CREWSHIP_BOLT_PATH=/tmp/crewship-state/state.db
    export CREWSHIP_LOG_LEVEL=debug
    # Auto-detect container runtime; fall back to --no-docker if none found
    if docker info &>/dev/null || podman info &>/dev/null; then
      exec go run ./cmd/crewship start
    else
      exec go run ./cmd/crewship start --no-docker
    fi
  ) > "$GO_LOG" 2>&1 &

  echo $! > "$GO_PID_FILE"

  local attempts=0
  while [[ $attempts -lt 15 ]]; do
    if curl -sf http://localhost:$GO_PORT/api/health >/dev/null 2>&1; then
      ok "crewship started (pid $(cat "$GO_PID_FILE"))"
      return
    fi
    sleep 1
    attempts=$((attempts + 1))
  done

  warn "crewship started but health check timed out -- check $GO_LOG"
}

start_next() {
  if pid=$(is_running "$NEXT_PID_FILE"); then
    ok "Next.js already running (pid $pid)"
    return
  fi

  if port_in_use "$NEXT_PORT"; then
    err "Port $NEXT_PORT already in use -- stop the process first"
    return 1
  fi

  log "Starting Next.js on :$NEXT_PORT..."

  (
    cd "$PROJECT_DIR"
    exec pnpm dev --port "$NEXT_PORT"
  ) > "$NEXT_LOG" 2>&1 &

  echo $! > "$NEXT_PID_FILE"

  local attempts=0
  while [[ $attempts -lt 20 ]]; do
    if curl -sf -o /dev/null http://localhost:$NEXT_PORT 2>/dev/null; then
      ok "Next.js started (pid $(cat "$NEXT_PID_FILE"))"
      return
    fi
    sleep 1
    attempts=$((attempts + 1))
  done

  warn "Next.js started but readiness check timed out -- check $NEXT_LOG"
}

kill_tree() {
  local pid="$1"
  local children
  children=$(pgrep -P "$pid" 2>/dev/null || true)
  for child in $children; do
    kill_tree "$child"
  done
  kill "$pid" 2>/dev/null || true
}

stop_service() {
  local name="$1" pid_file="$2" port="$3"

  if pid=$(is_running "$pid_file"); then
    log "Stopping $name (pid $pid)..."
    kill_tree "$pid"
    local attempts=0
    while kill -0 "$pid" 2>/dev/null && [[ $attempts -lt 10 ]]; do
      sleep 0.5
      attempts=$((attempts + 1))
    done
    if kill -0 "$pid" 2>/dev/null; then
      kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$pid_file"
  fi

  if port_in_use "$port"; then
    local orphan_pids
    orphan_pids=$(lsof -ti:"$port" 2>/dev/null || true)
    for orphan_pid in $orphan_pids; do
      kill "$orphan_pid" 2>/dev/null || true
    done
    sleep 1
    orphan_pids=$(lsof -ti:"$port" 2>/dev/null || true)
    for orphan_pid in $orphan_pids; do
      kill -9 "$orphan_pid" 2>/dev/null || true
    done
  fi

  ok "$name stopped"
}

cmd_start() {
  check_prerequisites
  echo -e "${BOLD}Crewship Dev Environment${NC}"
  echo "========================"
  echo ""
  local db_mode
  db_mode=$(detect_db_mode)
  echo -e "  Database:  ${CYAN}$db_mode${NC}"
  echo ""
  start_postgres
  start_go
  start_next
  echo ""
  ok "All services started"
  echo -e "  Frontend:  ${CYAN}http://localhost:$NEXT_PORT${NC}"
  echo -e "  Backend:   ${CYAN}http://localhost:$GO_PORT${NC}"
  echo -e "  WebSocket: ${CYAN}ws://localhost:$GO_PORT/ws${NC}"
  echo -e "  Logs:      ${CYAN}./dev.sh logs${NC}"
}

cmd_stop() {
  echo -e "${BOLD}Stopping Crewship...${NC}"
  stop_service "Next.js" "$NEXT_PID_FILE" "$NEXT_PORT"
  stop_service "crewship" "$GO_PID_FILE" "$GO_PORT"
  local db_mode
  db_mode=$(detect_db_mode)
  if [[ "$db_mode" == "postgresql" ]]; then
    ok "Stopped (PostgreSQL left running)"
  else
    ok "Stopped"
  fi
}

cmd_restart() {
  cmd_stop
  echo ""
  cmd_start
}

cmd_status() {
  echo -e "${BOLD}Crewship Dev Environment${NC}"
  echo "========================"

  local db_mode
  db_mode=$(detect_db_mode)

  if [[ "$db_mode" == "postgresql" ]]; then
    if docker compose -f "$PROJECT_DIR/docker/docker-compose.yml" ps --format '{{.Status}}' 2>/dev/null | grep -qi "up"; then
      echo -e "  PostgreSQL:  ${GREEN}running${NC}"
    else
      echo -e "  PostgreSQL:  ${RED}stopped${NC}"
    fi
  else
    echo -e "  SQLite:      ${GREEN}file:./crewship.db${NC}"
  fi

  if pid=$(is_running "$GO_PID_FILE"); then
    if curl -sf http://localhost:$GO_PORT/api/health >/dev/null 2>&1; then
      echo -e "  crewship:    ${GREEN}running${NC} (pid $pid)"
    else
      echo -e "  crewship:    ${YELLOW}starting${NC} (pid $pid)"
    fi
  elif port_in_use "$GO_PORT"; then
    echo -e "  crewship:    ${YELLOW}running (orphan on :$GO_PORT)${NC}"
  else
    echo -e "  crewship:    ${RED}stopped${NC}"
  fi

  if pid=$(is_running "$NEXT_PID_FILE"); then
    echo -e "  Next.js:     ${GREEN}running${NC} (pid $pid, port $NEXT_PORT)"
  elif port_in_use "$NEXT_PORT"; then
    echo -e "  Next.js:     ${YELLOW}running (orphan on :$NEXT_PORT)${NC}"
  else
    echo -e "  Next.js:     ${RED}stopped${NC}"
  fi

  echo ""
  echo -e "  Frontend:    http://localhost:$NEXT_PORT"
  echo -e "  Backend:     http://localhost:$GO_PORT"
  echo -e "  WebSocket:   ws://localhost:$GO_PORT/ws"
  echo -e "  IPC Socket:  /tmp/crewship.sock"
}

cmd_logs() {
  tail -f "$GO_LOG" "$NEXT_LOG" 2>/dev/null
}

cmd_logs_go() {
  tail -f "$GO_LOG" 2>/dev/null
}

cmd_logs_next() {
  tail -f "$NEXT_LOG" 2>/dev/null
}

case "${1:-help}" in
  start)     cmd_start ;;
  stop)      cmd_stop ;;
  restart)   cmd_restart ;;
  status)    cmd_status ;;
  logs)      cmd_logs ;;
  logs:go)   cmd_logs_go ;;
  logs:next) cmd_logs_next ;;
  *)
    echo "Usage: ./dev.sh {start|stop|restart|status|logs|logs:go|logs:next}"
    echo ""
    echo "  start     Start crewshipd + Next.js (+ PostgreSQL if configured)"
    echo "  stop      Stop crewshipd + Next.js"
    echo "  restart   Stop then start all services"
    echo "  status    Show status of all services"
    echo "  logs      Tail combined logs"
    echo "  logs:go   Tail crewshipd logs only"
    echo "  logs:next Tail Next.js logs only"
    exit 1
    ;;
esac
