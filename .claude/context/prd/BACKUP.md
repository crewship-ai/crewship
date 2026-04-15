# Crewship -- Backup & Restore (BACKUP.md)

**Verze:** 1.1 (draft po kriticke review)
**Datum:** 2026-04-15
**Status:** PRD draft -- MVP sereazny na 80/20 rule, ceka na schvaleni, zadny kod jeste nevznikl
**Zavislosti:** AGENT-RUNTIME.md, SECURITY.md, DATABASE.md, MEMORY.md, SIDECAR.md,
`.claude/context/DISCUSSION-2026-04-10-architecture-options.md` (Tema 2)

---

## 1. PREHLED A MOTIVACE

Crewship dnes neumi zadnou formu backupu -- `RemoveCrewVolumes()` maze data bez
ceremonie, DB neni periodicky zalohovana, a klient nema zadny zpusob jak
exportovat svuj crew, workspace ani celou instalaci.

Po adopci devcontainers (PR #154, CRE-123) se scope zmenil -- `cached_image`
je nove reprodukovatelny artefakt, takze backup se muze zamerit na **data**
(workspace, memory, volumes, DB rows) a `devcontainer.json` / `mise.toml` /
image digesty slouzi k reprodukci image pri restore.

### 1.1 Cile

| Cil | Popis |
|---|---|
| **Robustni MVP** | 80 % hodnoty za 2-3 tydny prace. Workspace/crew backup + restore + sifrovani + audit + CLI |
| **Killer feature: portabilita** | Jeden `.tar.zst` soubor funguje kdekoliv, restore na druhem stroji na 1 prikaz |
| **Admin-only** | Jen OWNER/ADMIN roles (RBAC). Backup neni self-service |
| **Forward compat (N-2)** | Backup ze dnesni verze se restauruje na pristich N-2 major verzich. Pak deprecation warning |
| **Sifrovany default** | AGE (X25519 + ChaCha20-Poly1305). Bundle nikdy neni plaintext na disku |
| **Filesystem consistency** | Docker pause + tar. APP-level consistency (bezi DB/web server uvnitr) = **user responsibility**, ne nase |
| **Zero-regret format** | MANIFEST v1 se stabilizuje ted, golden bundle fixture v CI |

### 1.2 Ne-cile (vyslovne mimo scope MVP)

- **Per-agent backup.** Granularita je crew nebo workspace. Per-agent export je future feature.
- **Auto-scheduling / cron.** Admin rucne spousti. V2.
- **Remote backends** (S3/B2/Azure/GCS). MVP jen lokalni filesystem. V2 pres `kopia` library.
- **Dedup / content-addressed storage.** MVP kazdy backup = samostatny tar.zst. V2.
- **Backward restore** (backup z v2.0 na v1.0). Unsupported.
- **CRIU / process checkpoint.** Backup je vzdy offline (pause).
- **Cross-instance crew-level portability.** MVP = workspace-level portability only. Crew backup je pro intra-instance use (smaz a obnov, presun crew mezi workspacy ve stejne instanci). Cross-instance crew = V2.
- **App-level consistency** pro procesy bezici uvnitr kontejneru. User zodpovida -- doporucujeme stop DB/server pred backupem.
- **UI admin panel.** V1.5 (po MVP). MVP = CLI only.
- **Instance backup** (credstore + auth keys). V1.5 po security review. MVP neobsahuje `/secrets` ani signing keys.
- **Dry-run, rate-limiting, progress reporting, `--include-image`.** V2.
- **docker-only restore.sh** v bundle. DROPPED (viz 3.1).

---

## 2. GRANULARITA A ROLE (MVP)

### 2.1 Dve scope varianty v MVP

```
crewship backup create --scope=<crew|workspace> [...]
```

| Scope | Obsah | Kdo smi | Kdy pouzit |
|---|---|---|---|
| `crew` | Jeden crew + vsichni agenti v nem + memory + volumes + workspace bind + relevantni DB rows | OWNER/ADMIN workspaceu | Presun crew mezi workspacy; selektivni backup v ramci instance |
| `workspace` | Workspace meta + vsechny crews v nem + workspace-level skills/memory | OWNER/ADMIN workspaceu | **Primarni use case** -- backup cele klientske prace |

`instance` scope existuje v roadmape (V1.5, sekce 15), ne v MVP.

### 2.2 Izolace

- **Workspace backup nesmi nikdy obsahovat data jineho workspaceu.** Hard isolation.
- Oba scope generuji bundle se stejnym MANIFEST formatem, lisi se jen sekce obsahu.

### 2.3 Cross-instance portability (MVP omezeni)

- **Workspace backup** = plne portable mezi Crewship instancemi (ID prostor workspace je self-contained).
- **Crew backup** = restore **jen do stejne instance** v MVP (FK na workspace-level skills, cross-workspace memory reference, audit log IDs). Cross-instance crew backup = V2 (pozaduje ID remapping + skills deduplication).
- MVP MANIFEST ma pole `compatible_targets: ["same-instance"]` pro crew scope, `["any-instance"]` pro workspace scope. Restore command to overuje.

### 2.4 RBAC

Implementace:
- Frontend (V1.5): CASL ability `backup.create` / `backup.restore` / `backup.delete`
- Backend: Go middleware v `internal/api/` kontroluje role + scope ownership
- Audit: kazdy backup/restore = row v `audit_log` tabulce (basic fields v MVP; EE: SIEM export)

---

## 3. BUNDLE FORMAT (MANIFEST v1)

### 3.1 Layout

```
crewship-<scope>-<slug>-<iso-ts>.tar.zst
  |
  +-- MANIFEST.json            # strojove citelne metadata (NIKDY sifrovane)
  +-- RESTORE.md               # stručný pointer na DEPLOYMENT.md "Restore mimo Crewship"
  |
  +-- payload.tar.zst.age      # sifrovany obsah (default)
      nebo
  +-- payload/                 # nesifrovany obsah (jen kdyz --no-encrypt, warning)
      |
      +-- devcontainer/        # devcontainer.json, mise.toml, image digesty
      +-- workspace/           # /workspace/{CrewID} bind mount per crew
      +-- volumes/             # named Docker volumes per crew (home.tar, tools.tar)
      +-- memory/              # /output/{CrewID}/.memory/ per crew + FTS5 dump
      +-- db/                  # JSON export relevantnich DB rows
```

**Co NENI v bundle (proti verzi 1.0 PRD):**
- `restore.sh` -- DROPPED. Zadny auto-generovany bash skript. Jen MANIFEST + data.
- `credstore/`, `auth/` -- NENI v MVP. Az V1.5 v instance scope.
- `image/` -- NENI v MVP. `--include-image` flag prijde v V2.

MANIFEST.json je **vzdy** v cleartextu (aby `inspect` fungoval bez hesla).
Payload je sifrovany AGE blob (default).

### 3.2 MANIFEST schema

```json
{
  "format_version": 1,
  "crewship_version_at_backup": "0.5.0",
  "schema_migration_versions": [46],
  "scope": "workspace",
  "compatible_targets": ["any-instance"],
  "created_at": "2026-04-15T12:34:56Z",
  "created_by": {
    "user_id": "cuid...",
    "email": "admin@example.com",
    "role": "OWNER"
  },
  "source_instance": {
    "hostname": "host1",
    "platform": "linux/amd64",
    "docker_version": "28.0.1"
  },
  "contents": {
    "workspace": {
      "id": "cuid...",
      "slug": "my-ws",
      "name": "My Workspace"
    },
    "crews": [
      {
        "id": "cuid...",
        "slug": "my-crew",
        "name": "My Crew",
        "runtime_image": "crewship/agent-runtime",
        "base_image_digest": "sha256:abc123...",
        "cached_image_digest": "sha256:def456...",
        "config_hash": "sha256:...",
        "devcontainer_config_included": true,
        "mise_config_included": true,
        "features": [
          {"name": "ghcr.io/devcontainers/features/node", "digest": "sha256:..."}
        ],
        "workspace_included": true,
        "volumes_included": ["home", "tools"],
        "memory_included": true,
        "agent_count": 3,
        "payload_size_bytes": 1234567890
      }
    ]
  },
  "encryption": {
    "enabled": true,
    "algorithm": "age-v1",
    "key_derivation": "scrypt",
    "recipients": ["age1..."]
  },
  "checksums": {
    "payload_sha256": "sha256:..."
  }
}
```

### 3.3 Naming

`crewship-<scope>-<slug>-<iso-ts>.tar.zst` -- priklad:
`crewship-workspace-acme-20260415T123456Z.tar.zst`.

---

## 4. BACKUP FLOW

### 4.1 Pre-flight kontroly

1. **RBAC check** -- ma caller pravo na tento scope?
2. **Scope validation** -- existuje crew/workspace se zadanym slug?
3. **Free disk space** -- `available >= estimate * 1.2`. Abort s cisly kdyz ne.
4. **Backup lock** -- ziska per-workspace advisory lock (DB tabulka `backup_locks`, CAS insert). Pokud jiny backup bezi, abort "another backup in progress".
5. **Agent idle check** -- pod lockem zkontroluje ze zadny agent ve scope nema status `running`. Jasna chyba pokud ano. Lock zabranuje TOCTOU race.
6. **Encryption key check** -- passphrase/keyfile dostupny PRED pausnutim.

Pri selhani kterekoliv: bail brzy, release lock, zadne side-effects.

### 4.2 Kroky backupu

```
1. Ziskej backup_lock (DB CAS insert).
2. Pre-flight kontroly.
3. Pro kazdy crew ve scope:
   a. `docker pause <container>`
   b. tar nad workspace bind + volumes + memory dir -> stream do payload
   c. `docker unpause <container>`
4. DB dump (BEGIN IMMEDIATE TRANSACTION, SELECT * FROM ... WHERE scope=...) -> JSON.
5. Zkonstruuj MANIFEST.json.
6. Streamuj payload pres age encryption (pokud nejde --no-encrypt).
7. Baluj MANIFEST + RESTORE.md + payload do outer tar.zst.
8. SHA-256 outer tar.
9. Atomic rename .partial -> finalni jmeno do ~/.crewship/backups/.
10. Audit log row.
11. Release backup_lock.
12. Vrat path.
```

**Pause time** pro typicky crew (5-10 GB): 10-60 s. Workspace = sekvencni pres vsechny crews (jeden pause najednou).

### 4.3 Advisory lock (`backup_locks` tabulka)

```sql
CREATE TABLE backup_locks (
  workspace_id TEXT PRIMARY KEY,
  acquired_at TEXT NOT NULL DEFAULT (datetime('now')),
  acquired_by TEXT NOT NULL,    -- user_id
  expires_at TEXT NOT NULL       -- acquired_at + 1h
);
```

- Insert s `ON CONFLICT` fail = lock held jinym. Abort backup.
- TTL 1 hodina (soft) -- crashly backup neuvazni workspace navzdy.
- Scheduler/orchestrator kontroluje lock pred startem noveho agent run a odmita, kdyz lock existuje.

---

## 5. RESTORE FLOW -- DVE UROVNE (v1.0 melo 3, drop docker-only)

### 5.1 Uroven 1: Data-only (coreutils + age)

Klient ma jen `tar`, `zstd`, `age`. Postup:

```bash
tar --zstd -xf backup.tar.zst
cat MANIFEST.json | less                              # zjistit co je v bundle
age -d -i keyfile payload.tar.zst.age | tar -xf -     # pokud sifrovano
```

Vysledek: soubory na disku. Zadny running kontejner. Pouziva se pro forensic
inspect nebo klientsky audit obsahu. Dokumentace: `.claude/context/prd/DEPLOYMENT.md`
sekce "Restore mimo Crewship".

### 5.2 Uroven 2: Crewship-native

```bash
crewship backup restore backup.tar.zst [--as-workspace <new-slug>] [--as-crew <new-slug>]
```

- Verify MANIFEST checksum + `format_version` compat
- Verify `compatible_targets` (same-instance vs any-instance)
- Request passphrase
- Restore DB rows pres forward migration chain
- Restore workspace bind / volumes / memory
- `docker pull` podle digestu (pokud image NENI v bundle), fallback provisionuj z `devcontainer.json`
- Credentials NE-restore -- credstore zustava na target instance, re-injection pri prvnim runu
- Audit log

**Slug collision** = abort s pokynem `--as-*` flag.

---

## 6. SIFROVANI

### 6.1 Volba: `filippo.io/age`

- **Pure Go**, BSD-3, stabilni format, modern crypto (X25519 + ChaCha20-Poly1305)
- Klient muze dekryptovat backup sam pres `age` CLI
- Passphrase mode nebo keyfile (X25519 pub/priv)

### 6.2 Default chovani

- `crewship backup create` bez flagu = interaktivni passphrase prompt (2x potvrzeni)
- `--passphrase-file <path>` = neinteraktivni
- `--recipient <age-pubkey>` = asymetricky
- `--no-encrypt` = plaintext payload (explicit opt-out, logged warning)

### 6.3 Key management

- MVP: passphrase/keyfile = **zodpovednost admina**. Crewship hesla neuklada.
- V1.5: integrace se systemem keyring
- EE: KMS integrace, rotation policies

---

## 7. CONSISTENCY GUARANTEES

### 7.1 Co backup garantuje

- **Filesystem-level consistency** -- docker pause + tar dava stable filesystem snapshot.
- **DB consistency** -- `BEGIN IMMEDIATE TRANSACTION` pri DB dump.
- **Checksum integrity** -- SHA-256 pres outer tar; overeno pri restore.
- **Atomicity** -- bundle je atomically renamed z `.partial` az po uspesnem dokonceni.
- **Lock safety** -- per-workspace advisory lock chrani proti konkurencnim backupum a TOCTOU race s agent runs.

### 7.2 Co backup NEGARANTUJE (DULEZITE)

**Application-level consistency pro procesy bezici v kontejneru JE USER ZODPOVEDNOST.**

Priklady:
- Postgres s pending WAL fsync -> docker pause nezavola checkpoint -> tar dostane poskozena data. User musi `pg_ctl stop` pred backupem.
- Node.js server s half-written JSON log -> tar zkopiruje inkonzistentni soubor. User musi process drainovat.
- SQLite uvnitr kontejneru s WAL -> musi byt checkpointed (`PRAGMA wal_checkpoint(FULL)`) pred backupem.

PRD to neresi automaticky. V2 prida `preBackup`/`postBackup` hooks v devcontainer.json.

### 7.3 Agent idle guard + lock

Full-mode backup **odmitne startovat**, pokud ve scope existuje agent se statusem `running` nebo `busy`. Pod advisory lockem (ne mimo). Orchestrator pred startem noveho runu kontroluje lock a odmita.

V2 doplni graceful drain.

---

## 8. FORWARD COMPATIBILITY (N-2 policy)

### 8.1 Pravidla

1. **`format_version: 1`** pri prvnim releasu.
2. **Reader podporuje N-2 major verze back.** Pri Crewship 3.0 se precte format_version 1-3. Format_version 0 (hypoteticky) dostane deprecation warning. Format_version -1 = drop.
3. **Writer vzdy pise nejnovejsi format_version** aktualni verze.
4. **Unknown fields** pri readu = ignorovat, log warning.
5. **Deprecated fields** = warning, zustanou v readeru pres N-2.
6. **DB schema migrace** pri restore: forward chain migraci nad importovanymi daty.

### 8.2 Deprecation cesta pro klienta

- Backup z format_version X se neresti na verzi ktera umi jen X+3+.
- Crewship v release notes: "format_version X deprecated, upgrade backup pres `crewship backup migrate <file>`".
- `crewship backup migrate` (V1.5) = nacte stary format, zapise novy.

### 8.3 Golden bundle CI

`testdata/backup-fixtures/v1/` -- fixture bundle vytvoreny pri prvnim release.
CI job `backup-compat-check` pri kazdem PR:
- Pokusi se restorovat golden bundle do cisteho stavu
- Overi, ze vse projde bez chyby
- Fail build kdyz reader se rozbije

---

## 9. CLI (MVP -- UI + API az V1.5)

### 9.1 CLI komandy

```
crewship backup create --scope=<crew|workspace> <slug> [flags]
  --output <path>           default ~/.crewship/backups/
  --no-encrypt              plaintext payload (warning)
  --passphrase-file <path>  non-interaktivni
  --recipient <age-pubkey>  asymetricky encryption

crewship backup list
  Skenuje ~/.crewship/backups/ + parsuje MANIFEST ka kazdeho souboru.

crewship backup inspect <file>
  Vypise MANIFEST + compat check (format_version vs aktualni reader).

crewship backup restore <file> [flags]
  --as-workspace <slug>     restore pod jinym slug
  --as-crew <slug>

crewship backup delete <file>
  Smaze soubor. Audit log.
```

**NENI v MVP:** `--include-image`, `--dry-run`, progress bar, rate-limit,
`backup migrate`, `backup schedule`, remote backends, UI, API endpointy.

---

## 10. STORAGE A NAMING

- Default adresar: `~/.crewship/backups/` (mode 0700, vytvari se pri prvnim backupu)
- Naming: `crewship-<scope>-<slug>-<iso-ts>.tar.zst`. ISO timestamp do sekund; kolize pridaji `-<hash8>` suffix.
- Partial files: `.partial` suffix, atomic rename po uspesnem dokonceni.
- Free space pre-flight: `free >= estimate * 1.2`.

---

## 11. ERROR HANDLING (MVP)

| Situace | Chovani |
|---|---|
| RBAC fail | HTTP 403, audit row |
| Backup lock held | Abort "another backup in progress", retry after X |
| Agent running | Jasna hlaska, abort, release lock |
| Disk full pre-flight | Abort, hlaska s cisly |
| Disk full behem | Cleanup partial, abort, release lock, log |
| Docker pause fail | Abort, container v puvodnim stavu, release lock |
| Docker unpause fail PO tar | KRITICKY log, alert admina, rucny zasah. Release lock |
| Encryption fail | Cleanup plaintext partial, abort, release lock |
| DB dump fail | Abort pred pausem (drive v sekvenci), release lock |
| Invalid checksum pri restore | Abort pred extrakci |
| Format version > aktualni | Abort "upgrade crewship" |
| Format version < N-2 | Abort "backup too old, use `backup migrate`" (V1.5) |
| Slug collision pri restore | Abort, vyzva `--as-*` |
| DB migration fail | Rollback transakce, abort |
| Decryption fail | Abort "wrong passphrase or corrupted bundle" |

---

## 12. SECURITY

### 12.1 Co je chranene

- **`/secrets` mount** -- NIKDY v MVP bundlu.
- **Credstore + auth keys** -- NIKDY v MVP bundlu. Az V1.5 instance backup.

### 12.2 Co NENI automaticky chranene

- Workspace bind mount (uzivatelsky kod). Bundle je AGE-encrypted, klientska zodpovednost za fyzickou distribuci.
- Memory markdown soubory. Idem.

### 12.3 Audit log

Kazdy backup/restore event -> row v `audit_log`:
- `action`: backup.create | backup.delete | backup.restore | backup.download
- `user_id`, `role`, `scope`, `scope_slug`
- `file_path`, `file_size`, `file_sha256`
- `source_ip`
- `success`, `error_message`

MVP: basic fields. EE: SIEM stream.

---

## 13. MVP vs V1.5 vs V2 FEATURE MATRIX

| # | Feature | Value | MVP | V1.5 | V2 | Komentar |
|---|---|---|---|---|---|---|
| 1 | Workspace backup + restore (intra-instance) | 30 % | ✅ | | | Killer feature |
| 2 | Crew backup + restore (intra-instance) | 4 % | ✅ | | | Free-drop pri workspace |
| 3 | AGE encryption default | 15 % | ✅ | | | Enterprise signal |
| 4 | Portable tar.zst + MANIFEST v1 stabilni | 12 % | ✅ | | | Forward compat guarantee |
| 5 | CLI (create/list/inspect/restore/delete) | 8 % | ✅ | | | |
| 6 | Admin-only RBAC | 5 % | ✅ | | | B2B procurement |
| 7 | Audit log basic | 3 % | ✅ | | | Levne, zakladni |
| 8 | Golden bundle CI compat test | 3 % | ✅ | | | Levne, vysoka ROI |
| 9 | Pre-flight checks (disk, agent) | 3 % | ✅ | | | Bez toho UX fail |
| 10 | Per-workspace advisory lock | 2 % | ✅ | | | TOCTOU safety |
| 11 | UI admin panel | 4 % | | ✅ | | Post-MVP |
| 12 | Instance backup (credstore + auth) | 4 % | | ✅ | | PO security review |
| 13 | API endpointy | -- | | ✅ | | Spolu s UI |
| 14 | Keyring integration pro passphrase | 1 % | | ✅ | | |
| 15 | `--include-image` flag | 2 % | | | ✅ | Default: docker pull |
| 16 | Dry-run mode | 1 % | | | ✅ | |
| 17 | Progress reporting (WS) | 1 % | | | ✅ | |
| 18 | Rate limiting | 0.5 % | | | ✅ | |
| 19 | Scheduled/auto backup | 1 % | | | ✅ | |
| 20 | Retention policies (keep N) | 0.5 % | | | ✅ | |
| 21 | Remote backends (S3/B2/GCS) | 0.5 % | | | ✅ | Pres kopia library |
| 22 | Differential/incremental | -- | | | ✅ | Pres kopia |
| 23 | KMS integration | -- | | | EE | |
| 24 | Compliance retention (WORM, legal hold) | -- | | | EE | |
| 25 | `preBackup`/`postBackup` hooks | -- | | | ✅ | App-level consistency |
| 26 | `backup migrate` (format upgrade) | -- | | ✅ | | Deprecation path support |

**MVP cumulative value: ~85 %** behem 2-3 tydnu. Zbytek dopada postupne.

### 13.1 Killer features diferenciatory vs konkurence

1. **Jeden .tar.zst a funguje kdekoliv** -- 90 % self-hosted AI tools nema. Obrovsky diferenciator.
2. **Sifrovani defaultne** -- enterprise sales signal.
3. **Admin-only + audit log** -- B2B procurement question #1.
4. **Forward compat (N-2)** -- verejna duvera v trvanlivost backupu.

Zbytek features (scheduled, remote, dedup) konkurence ma, za ne neni extra bodove.

---

## 14. TESTING STRATEGY

### 14.1 Unit

- `internal/backup/` balicek: manifest serialization, encryption roundtrip, tar writer/reader, checksum
- Cil: >85 % line coverage

### 14.2 Integration

- E2E: vytvor workspace -> backup -> smaz -> restore -> over
- Napric crew + workspace scope
- S sifrovanim i bez
- Forward compat: `testdata/backup-fixtures/v1/` MUSI restorovat na HEAD

### 14.3 E2E smoke

- `scripts/e2e-backup-test.sh` (analogicky k `scripts/e2e-devcontainer-test.sh`)
- V CI

### 14.4 Coverage cile

- `internal/backup/`: >85 %
- Kriticka cesta: >95 %

### 14.5 Golden bundle policy

`testdata/backup-fixtures/v<N>/` -- pri kazdem majoru se prida fixture bundle.
CI job `backup-compat-check` overuje.

---

## 15. IMPLEMENTACNI FAZOVANI

### PR 1: Foundation (`internal/backup/` package) -- MVP

**Co:**
- `tar.go` -- tar.zst writer/reader
- `manifest.go` -- MANIFEST v1 serialization + validation
- `encrypt.go` -- age wrapper
- `checksum.go` -- SHA-256 helpers
- `format.go` -- format_version constants, N-2 compat matrix
- `lock.go` -- backup_locks table + advisory lock semantika
- `errors.go` -- typed errors
- `testdata/backup-fixtures/v1/` -- golden fixture
- Unit testy (>85 %)
- CI job `backup-compat-check`

**Akceptacni kriteria:**
- `go test ./internal/backup/... -count=1` pass
- `go vet ./...` clean
- CI job aktivni

### PR 2: Workspace + crew backup/restore CLI -- MVP

**Co:**
- `cmd/crewship/backup.go` -- CLI `create/list/inspect/restore/delete`
- Integrace s `internal/provider/docker` (pause/unpause, volume export)
- DB dump logika + forward migration chain pri restore
- Integration testy (napric crew + workspace, s/bez sifrovani)
- `scripts/e2e-backup-test.sh`
- Dokumentace: `.claude/context/prd/CLI-TOOLS.md` update + `DEPLOYMENT.md` sekce "Restore mimo Crewship"

**Zavislost:** PR 1 merged
**Ne-scope PR 2:** UI, API endpointy, instance scope, `--include-image`, `--dry-run`, progress

**Akceptacni kriteria:**
- Manualni E2E: workspace backup, smaz crew/ws, restore, over memory/workspace/volumes
- Workspace backup restore do jine instance (cross-host, fresh install)
- Agent-running guard + lock test
- RBAC: non-admin dostane 403
- Format_version v1 inspect pracuje

### PR 3: UI admin panel + API -- V1.5

**Co:**
- `internal/api/backup.go` -- REST endpointy (jen crew + workspace)
- `components/admin/backup-*.tsx` + `app/admin/backups/page.tsx`
- Wireframe `.claude/context/wireframes/22-admin-backups.html`
- Keyring integration

**Zavislost:** PR 2 merged (nekolik tydnu po)

### PR 4: Instance backup -- V1.5 po security review

**Co:**
- Extension `internal/backup/` o credstore + auth sections
- Server-level OWNER endpoint
- Rotace auth keys guide v `DEPLOYMENT.md`
- Security review podklady (dokumentovano, co se deje s klici)
- Integration testy na fresh VM

**Zavislost:** PR 3 merged + explicit security review sign-off
**Bezpecnostni pravidla:**
- Credstore zustava v bundle sifrovany svym klicem (defense in depth), navic AGE wrapper
- Pri restore do cizi instance MANDATORNI prompt: "rotate auth keys? [y/N]"
- Auth keys v bundle jsou AGE recipient-encrypted (ne passphrase) -- vyzaduje X25519 key, ne jen heslo
- Audit log obsahuje full crypto chain record

**Akceptacni kriteria:**
- Instance backup + restore na ciste VM -- vse funguje
- Security review pass
- Dokumentace: `.claude/context/prd/DEPLOYMENT.md` "Disaster recovery" sekce

---

## 16. OTEVRENE OTAZKY (k vyreseni v PR 1)

1. **Partial restore** (jen memory, jen workspace) -- V MVP NE. V2.
2. **Backup catalog DB** -- V MVP ne, filesystem scan + manifest inspect staci pro <100 backupu. V1.5 prida tabulku pri UI implementaci.
3. **Backup migrate command** -- V1.5.
4. **Parallel tarring** -- sekvencni v MVP, paralelni V2.
5. **Orchestrator interakce s backup_lock** -- implementace v PR 1/2. Existujici orchestrator loop musi check-ovat lock pred startem noveho runu.

---

## 17. ZAVISLOSTI

### 17.1 Existujici kod

| Komponenta | Kde | Jak |
|---|---|---|
| `internal/provider/docker/` | Docker ops | pause/unpause, volume inspect |
| `internal/devcontainer/` | devcontainer.json parser | Rozbaleni config pri restore |
| `internal/dockerutil/imagedigest.go` | Image digest resolution | Pin digesty v MANIFEST |
| `internal/database/database.go` | DB access | Raw SQL dump + forward migration |
| `internal/database/migrate.go` | Schema migrace | Forward chain pri restore |
| `internal/memory/` | Memory engine | FTS5 dump + restore |
| `internal/api/audit` (TBD) | Audit log | backup.* actions |

### 17.2 Nove zavislosti v go.mod (MVP)

| Package | Ucel | Licence |
|---|---|---|
| `filippo.io/age` | AGE encryption | BSD-3 |
| `github.com/klauspost/compress/zstd` | Zstd compression | Apache-2.0 (overit tranzitivne) |

**Nic jineho v MVP.** Kopia/restic/kms/keyring az V1.5/V2.

### 17.3 Runtime deps na hostu

- `docker` -- jiz je vyzadovan
- **Nic dalsiho noveho**. Age je embedded, zstd embedded, tar stdlib.

### 17.4 Klientovy runtime deps pro mimo-Crewship restore (level 1)

- `tar` + `zstd` (nebo `tar --zstd`)
- `age` CLI (pokud sifrovany) -- single binary z github.com/FiloSottile/age

Dokumentovano v `.claude/context/prd/DEPLOYMENT.md` sekce "Restore mimo Crewship".

---

## 18. TL;DR

- **MVP seřezny na 80/20** -- 2-3 tydny, 85 % hodnoty.
- **Admin-only** CLI backup/restore pro **crew + workspace** scope.
- **Jeden `.tar.zst`** bundle, 2 urovne restore (coreutils / crewship). Docker-only vrstva DROPPED.
- **AGE sifrovani default**, MANIFEST v1 s N-2 forward compat policy.
- **App-level consistency = user responsibility** (stop DB/server pred backupem).
- **Per-workspace advisory lock** chrani proti TOCTOU race s agent runs.
- **Roadmap:** PR 1 foundation -> PR 2 CLI -> PR 3 UI (V1.5) -> PR 4 instance backup (V1.5 po security review).
- **Killer features:** portabilita + sifrovani + admin+audit + forward compat. Diferenciator vuci konkurenci.
- **Nove deps:** jen `filippo.io/age` + `klauspost/compress/zstd`. Zadna nova host-side instalace.
