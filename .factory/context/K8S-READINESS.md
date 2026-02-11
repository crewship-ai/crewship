# Crewship -- Kubernetes Readiness Strategy

**Datum:** 2026-02-11
**Princip:** Architektura je od dne 1 navrzena pro K8s. MVP bezi na Docker,
Enterprise na K8s. Prechod = swap implementace provideru, NE prepis.

---

## 1. PROVIDER PATTERN (klicove rozhodnuti)

Kazda externi zavislost je skryta za interface. crewshipd NIKDY nepristupuje
k Docker, filesystemu nebo bboltu primo — vzdy pres provider.

```
MVP (single-node Docker):
  ContainerProvider  → DockerProvider (Docker SDK)
  StorageProvider    → LocalFSProvider (lokalni filesystem)
  StateProvider      → BboltProvider (embedded bbolt)
  IPC                → HTTP over Unix socket

Enterprise (K8s):
  ContainerProvider  → K8sProvider (client-go, Pods)
  StorageProvider    → S3Provider (MinIO/S3 + event notifications)
  StateProvider      → PgStateProvider (PostgreSQL tabulka)
  IPC                → HTTP over TCP (K8s Service discovery)
```

---

## 2. CONTAINER PROVIDER

Abstrahovani kontejneroveho runtime. Docker exec v MVP, K8s Pods v Enterprise.

```go
// internal/provider/container.go

type ContainerProvider interface {
    // Team container lifecycle
    EnsureTeamRuntime(ctx context.Context, team TeamConfig) error
    DestroyTeamRuntime(ctx context.Context, teamID string) error
    TeamRuntimeStatus(ctx context.Context, teamID string) (RuntimeStatus, error)

    // Agent execution (the core operation)
    ExecAgent(ctx context.Context, req ExecRequest) (ExecHandle, error)

    // Health
    HealthCheck(ctx context.Context, teamID string) error
}

type ExecRequest struct {
    TeamID     string
    AgentSlug  string
    Command    []string            // ["claude", "--print", "..."]
    Env        map[string]string   // credentials as ENV vars
    WorkingDir string              // "/workspace/{agent-slug}"
    Timeout    time.Duration
}

type ExecHandle interface {
    Stdout() io.Reader
    Stderr() io.Reader
    Wait() (exitCode int, err error)
    Cancel() error
}

type RuntimeStatus struct {
    State      string    // "running", "stopped", "creating", "error"
    Since      time.Time
    MemoryUsed int64
    CPUPercent float64
}
```

### Docker implementace (MVP)

```go
// internal/provider/docker/docker.go

type DockerProvider struct {
    client *client.Client
}

func (d *DockerProvider) EnsureTeamRuntime(ctx context.Context, team TeamConfig) error {
    // Docker: ContainerCreate + ContainerStart
    // 1 container = 1 team (shared by agents)
}

func (d *DockerProvider) ExecAgent(ctx context.Context, req ExecRequest) (ExecHandle, error) {
    // Docker: ContainerExecCreate + ContainerExecAttach
    // Exec runs INSIDE existing team container
}
```

### K8s implementace (Enterprise)

```go
// internal/provider/k8s/k8s.go

type K8sProvider struct {
    clientset *kubernetes.Clientset
    namespace string
}

func (k *K8sProvider) EnsureTeamRuntime(ctx context.Context, team TeamConfig) error {
    // K8s: Create/ensure Deployment + Service pro team
    // Pod template: agent-runtime image, resource limits, network policy
    // PVC pro /workspace/ (ReadWriteOnce)
    // PVC pro /output/ (ReadWriteMany — NFS/EFS)
}

func (k *K8sProvider) ExecAgent(ctx context.Context, req ExecRequest) (ExecHandle, error) {
    // K8s: PodExec (remotecommand.NewSPDYExecutor)
    // Alternativa: spustit novy Job per agent run (silnejsi izolace)
}
```

### Klic: Co se meni, co zustava

| Aspekt | Docker (MVP) | K8s (Enterprise) | Interface |
|---|---|---|---|
| Team runtime | Container | Deployment + Pod | EnsureTeamRuntime() |
| Agent exec | docker exec | kubectl exec / Job | ExecAgent() |
| Network izolace | --internal network | NetworkPolicy | (infra config) |
| Resource limity | --memory, --cpus | resources.limits | TeamConfig |
| Health check | Docker healthcheck | livenessProbe | HealthCheck() |
| Image | ghcr.io/crewship-ai/agent-runtime | Stejny image | TeamConfig.Image |

---

## 3. STORAGE PROVIDER

Abstrahovani ukladani logu, konverzaci a output souboru.

```go
// internal/provider/storage.go

type StorageProvider interface {
    // Logs (append-only JSONL)
    AppendLog(ctx context.Context, path string, entry []byte) error
    ReadLog(ctx context.Context, path string, offset, limit int) ([][]byte, error)

    // Conversations (append-only JSONL per session)
    AppendMessage(ctx context.Context, sessionID string, msg []byte) error
    ReadMessages(ctx context.Context, sessionID string, offset, limit int) ([][]byte, error)

    // Output files (agent deliverables)
    WriteFile(ctx context.Context, path string, r io.Reader) error
    ReadFile(ctx context.Context, path string) (io.ReadCloser, error)
    ListFiles(ctx context.Context, prefix string) ([]FileInfo, error)
    DeleteFile(ctx context.Context, path string) error

    // File watching (real-time notifications)
    Watch(ctx context.Context, prefix string) (<-chan FileEvent, error)
}

type FileEvent struct {
    Op   string // "create", "write", "remove"
    Path string
    Size int64
    Time time.Time
}

type FileInfo struct {
    Path    string
    Size    int64
    ModTime time.Time
    IsDir   bool
}
```

### LocalFS implementace (MVP)

```go
// internal/provider/localfs/localfs.go

type LocalFSProvider struct {
    basePath string // "/var/lib/crewship"
    watcher  *fsnotify.Watcher
}

func (l *LocalFSProvider) AppendLog(ctx context.Context, path string, entry []byte) error {
    // os.OpenFile(path, O_APPEND|O_CREATE|O_WRONLY) + Write
}

func (l *LocalFSProvider) Watch(ctx context.Context, prefix string) (<-chan FileEvent, error) {
    // fsnotify.NewWatcher() + watcher.Add(prefix)
}
```

### S3 implementace (Enterprise)

```go
// internal/provider/s3/s3.go

type S3Provider struct {
    client   *s3.Client
    bucket   string
    eventsCh chan FileEvent
}

func (s *S3Provider) AppendLog(ctx context.Context, path string, entry []byte) error {
    // S3: AppendObject nebo buffer + periodic flush
    // Alternativa: lokalni buffer + periodic S3 upload
}

func (s *S3Provider) Watch(ctx context.Context, prefix string) (<-chan FileEvent, error) {
    // S3 Event Notifications → SQS → poll
    // Nebo MinIO notifications (webhook)
}
```

### Klic: Co se meni

| Aspekt | LocalFS (MVP) | S3/MinIO (Enterprise) | Interface |
|---|---|---|---|
| Log append | os.OpenFile O_APPEND | S3 PutObject (buffered) | AppendLog() |
| File read | os.Open | S3 GetObject | ReadFile() |
| File watch | fsnotify (inotify) | S3 Event Notifications | Watch() |
| Conversation | JSONL file per session | S3 object per session | AppendMessage() |
| Output files | bind mount | S3 bucket | WriteFile() |
| Retention | logrotate | S3 lifecycle policy | (infra config) |

---

## 4. STATE PROVIDER

Abstrahovani job state (WAL). bbolt v MVP, PostgreSQL v Enterprise.

```go
// internal/provider/state.go

type StateProvider interface {
    SaveRun(ctx context.Context, run RunState) error
    GetRun(ctx context.Context, runID string) (RunState, error)
    UpdateRunStatus(ctx context.Context, runID, status string) error
    GetRunsByStatus(ctx context.Context, status string) ([]RunState, error)
    DeleteRun(ctx context.Context, runID string) error

    // Credential cooldowns
    SetCooldown(ctx context.Context, credID string, until time.Time) error
    GetCooldown(ctx context.Context, credID string) (time.Time, error)
    ClearExpiredCooldowns(ctx context.Context) error
}

type RunState struct {
    ID           string
    AgentID      string
    SessionID    string
    Status       string    // PENDING, RUNNING, COMPLETED, FAILED
    StartedAt    time.Time
    ContainerRef string    // container ID (Docker) nebo pod name (K8s)
    ExecRef      string    // exec ID (Docker) nebo exec session (K8s)
    LastActivity time.Time
    Metadata     map[string]string
}
```

### Bbolt implementace (MVP)

```go
// internal/provider/bbolt/bbolt.go
// Single-process embedded KV. Rychle, jednoduche, ale nesdilitelne.
```

### PostgreSQL implementace (Enterprise)

```go
// internal/provider/pgstate/pgstate.go
// Pouzije existujici PostgreSQL. Nova tabulka 'run_states'.
// Sdilitelne mezi N instancemi crewshipd.
// Credential cooldowns taky v PostgreSQL (misto in-memory map).
```

**Proc PostgreSQL a ne etcd/Redis:**
- PostgreSQL uz mame (zero new infrastructure)
- Pro job state staci — neni to high-throughput KV store
- ACID transakce zdarma
- Jedna tabulka `run_states` s TTL na stare zaznamy

---

## 5. IPC (Next.js ↔ crewshipd)

IPC uz je HTTP — meni se jen transport.

```
MVP:    HTTP over Unix socket (/tmp/crewship.sock)
K8s:    HTTP over TCP (http://crewshipd.crewship.svc.cluster.local:8080)
```

**Zmena v kodu: NULOVA.** Jediny rozdil je URL v env var `CREWSHIPD_URL`:

```bash
# MVP (single node)
CREWSHIPD_URL=unix:///tmp/crewship.sock

# K8s
CREWSHIPD_URL=http://crewshipd:8080
```

Next.js HTTP client pouzije Unix socket nebo TCP podle URL scheme. Zadny gRPC.
gRPC by pridal slozitost (protobuf, codegen) bez realne vyhody — HTTP+JSON staci.

---

## 6. WEBSOCKET V K8S

### Problem
WebSocket connections jsou stavove — kazda je v pameti jedne instance crewshipd.
Pri N instancich: uzivatel se pripoji k instanci A, ale agent bezi na instanci B.

### Reseni: Sticky sessions + broadcast

```
K8s Ingress:
  - WebSocket: sticky sessions (cookie-based affinity)
  - Uzivatel se VZDY pripoji ke stejne instanci

Broadcast mezi instancemi:
  - PostgreSQL LISTEN/NOTIFY (zero new infrastructure!)
  - Kdyz agent na instanci B vyprodukovuje output:
    1. Instace B: NOTIFY agent_event, '{"agent_id":"...", "data":"..."}'
    2. Vsechny instance: LISTEN agent_event → dorucí svym WS klientum
```

**Proc PostgreSQL LISTEN/NOTIFY a ne Redis PubSub:**
- PostgreSQL uz mame
- Pro nase objemy (stovky eventu/s, ne miliony) to staci
- Zero new infrastructure

### Alternativa (Phase 3+, pokud nestaci)
NATS nebo Redis PubSub pro high-throughput broadcasting.
Ale pro 99% deploymentu bude PostgreSQL LISTEN/NOTIFY dostatecny.

---

## 7. RATE LIMITING V K8S

```
MVP:    In-memory token bucket (per-process, Go stdlib)
K8s:    PostgreSQL-backed counter (sdileny mezi instancemi)
```

Rate limiting tabulka:
```sql
CREATE TABLE rate_limits (
    key        TEXT NOT NULL,        -- "user:{id}:api_read" nebo "ip:{ip}:login"
    tokens     INT NOT NULL,
    last_reset TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (key)
);
```

Atomicky dekrementuje tokeny:
```sql
UPDATE rate_limits SET tokens = tokens - 1
WHERE key = $1 AND tokens > 0 AND last_reset > now() - $2::interval
RETURNING tokens;
```

---

## 8. K8S MANIFEST SADA (co budeme deployovat)

```yaml
# Namespace
crewship/
  ├─ Deployment: nextjs        (1-N replicas, stateless)
  ├─ Deployment: crewshipd     (1-N replicas, stateful-ish)
  ├─ StatefulSet: postgresql   (1 replica, PVC)
  ├─ Service: nextjs           (ClusterIP)
  ├─ Service: crewshipd        (ClusterIP)
  ├─ Service: postgresql       (ClusterIP)
  ├─ Ingress: crewship-ingress (TLS, sticky sessions for WS)
  ├─ NetworkPolicy: agent-isolation  (agenti nemohou k platforme)
  ├─ NetworkPolicy: llm-egress      (agenti jen k LLM allowlist)
  ├─ PVC: output-storage      (ReadWriteMany — NFS/EFS)
  ├─ PVC: log-storage         (ReadWriteMany — NFS/EFS)
  ├─ PVC: pg-data             (ReadWriteOnce)
  ├─ ConfigMap: crewship-config
  ├─ Secret: crewship-secrets  (ENCRYPTION_KEY, NEXTAUTH_SECRET, DB creds)
  └─ HPA: crewshipd-autoscaler (CPU/memory-based)
```

Agent kontejnery se vytvari DYNAMICKY (ne jako soucasty manifestu):
```yaml
# Kazdy team dostane:
  ├─ Deployment: team-{slug}   (1 replica, agent-runtime image)
  ├─ PVC: workspace-{slug}     (ReadWriteOnce, ephemeral)
  ├─ NetworkPolicy: team-{slug}-isolation
  └─ ResourceQuota: team-{slug}-limits
```

---

## 9. KONFIGURACE (env var driven)

```bash
# Provider selection (single env var per provider)
CREWSHIP_CONTAINER_PROVIDER=docker    # docker | k8s
CREWSHIP_STORAGE_PROVIDER=localfs     # localfs | s3
CREWSHIP_STATE_PROVIDER=bbolt         # bbolt | postgres

# IPC (transport-agnostic)
CREWSHIPD_URL=unix:///tmp/crewship.sock   # nebo http://crewshipd:8080

# S3 (jen kdyz STORAGE_PROVIDER=s3)
CREWSHIP_S3_ENDPOINT=                 # MinIO URL nebo AWS S3
CREWSHIP_S3_BUCKET=crewship
CREWSHIP_S3_ACCESS_KEY=
CREWSHIP_S3_SECRET_KEY=

# K8s (jen kdyz CONTAINER_PROVIDER=k8s)
CREWSHIP_K8S_NAMESPACE=crewship
CREWSHIP_K8S_AGENT_IMAGE=ghcr.io/crewship-ai/agent-runtime:latest
```

---

## 10. MIGRACNI CESTA (Docker → K8s)

| Krok | Co se meni | Co zustava |
|---|---|---|
| 1. State: bbolt → PostgreSQL | `CREWSHIP_STATE_PROVIDER=postgres` | Vsechno ostatni |
| 2. Storage: localfs → S3 | `CREWSHIP_STORAGE_PROVIDER=s3` | Vsechno ostatni |
| 3. Container: docker → k8s | `CREWSHIP_CONTAINER_PROVIDER=k8s` | Vsechno ostatni |
| 4. IPC: socket → HTTP | `CREWSHIPD_URL=http://crewshipd:8080` | Vsechno ostatni |

Kazdy krok je NEZAVISLY. Muzete jit postupne (napr. nejdriv S3 storage, pak K8s).
Nemusite menit zadny aplikacni kod — jen env vars.

---

## 11. TIMELINE

| Faze | Co | Kdy |
|---|---|---|
| MVP | Docker + LocalFS + bbolt | Ted |
| Phase 2 | PgState + S3 storage (priprava) | +2-3 mesice |
| Phase 3 | K8s provider + manifesty + NetworkPolicy | +4-6 mesicu |
| Enterprise | Helm chart, operator, multi-cluster | +8-12 mesicu |

---

## 12. OTEVRENE OTAZKY

1. **K8s Job vs Exec** — agent run jako novy Pod (Job) nebo exec do existujiciho Podu? Job = silnejsi izolace, Exec = rychlejsi start.
2. **PVC typ pro /output/** — NFS (ReadWriteMany) nebo S3 FUSE mount? NFS je jednodussi ale pomalejsi.
3. **Helm chart vs Kustomize** — jak distribuovat K8s manifesty? Helm je standard pro enterprise.
4. **Operator pattern** — vyplatí se Crewship Operator (CRD: CrewshipTeam, CrewshipAgent)? Silne pro enterprise, slozite na vyvoj.
5. **Multi-cluster** — jeden Crewship, agenti na vice clusterech? Overkill pro Phase 3, mozna Phase 4.
