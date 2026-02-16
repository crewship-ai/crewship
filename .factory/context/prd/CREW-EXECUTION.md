# Crewship -- Crew Execution & Progress Tracking (CREW-EXECUTION.md)

**Verze:** 1.0
**Datum:** 2026-02-16
**Status:** Architekturni navrh (implementace Phase 2A/2B/3)
**Zavislosti:** ORCHESTRATION.md (delegace, sidecar, leader modes),
AGENT-RUNTIME.md (kontejnery, sidecar API, loop modes),
DATABASE.md (existujici Prisma schema)

---

## 1. VIZE

Crew Execution je **zastresujici entita pro celou zakázku/ukol** zadanou Crew Leaderovi.
Inspirace: realny firemni workflow — sef dostane projekt, rozlozi ho na ukoly,
priradi lidem, sleduje postup, iteruje dokud neni hotovo.

**Klicove principy:**
- **External state, not context** (inspirace Ralph Loop) — postup se drzi v DB a na
  filesystemu, ne v AI pameti. Kazda iterace cte aktualni stav.
- **Tabulkovy Execution Board** — uzivatel vidi spreadsheet s ukoly, agenty, statusy,
  casovymi udaji a naklady. Analogie k realnemu project managementu.
- **Iterativni loop** — developer → tester → zpet, dokud neni hotovo.
  Workflow sablony pro opakovane patterny, leader muze overridnout.
- **Autonomni hiring** — leader muze dynamicky "najmat" nove agenty/skilly
  s konfigurovatelnou urovni autonomie (supervised/semi-auto/full-auto).

---

## 2. CREW EXECUTION — ZAKLADNI KONCEPT

### 2.1 Co je Crew Execution

```
CrewExecution = 1 zakázka/projekt zadany lidrovi
  ├── trace_id: "crew-exec-{uuid}" (prolinkovani vsech sessions, delegaci, logu)
  ├── plan: strukturovany plan (JSON s tasky a dependencies)
  ├── tasks[]: jednotlive ukoly prirazene agentum
  ├── status: PLANNING → IN_PROGRESS → REVIEW → COMPLETED/FAILED/CANCELLED
  └── JSONL mirror: /output/{leader-slug}/crew-exec-{id}/progress.jsonl
```

### 2.2 Lifecycle

```
PLANNING       Lidr analyzuje ukol, vytvari plan a rozdeluje na tasky
     │
     ▼
IN_PROGRESS    Workery pracuji na taskech, lidr koordinuje
     │
     ▼
REVIEW         Vsechny tasky hotove, lidr kontroluje vysledky
     │
     ├──→ COMPLETED   Lidr spokojeny, odpovida uzivatel
     ├──→ FAILED      Neresitelny problem, lidr reportuje
     └──→ CANCELLED   Uzivatel nebo lidr zrusil execution
```

**Kdo vytvari CrewExecution:**
- crewshipd automaticky pri kazdem netrivialnim ukolu zadanem lidrovi
- Leader rozhodne jestli ukol vyzaduje execution (jednoduche otazky = ne)
- Webhook trigger muze vytvorit execution automaticky
- Director deleguje na leadera → automaticky se vytvori execution

### 2.3 Trigger flow

```
1. Uzivatel: "Pripravte mesicni report socialnich siti"
2. crewshipd prijme pres WebSocket, spusti leadera (Docker exec)
3. Leader analyzuje ukol → rozhodne: "tohle je projekt, ne jednoducha odpoved"
4. Leader posle POST /execution/create na sidecar
5. crewshipd vytvori CrewExecution v DB (status: PLANNING)
6. Leader posle POST /execution/plan s rozlozenym planem
7. crewshipd ulozi plan, vytvori CrewExecutionTask zaznamy, status → IN_PROGRESS
8. Leader deleguje prvni vlnu tasku (POST /delegate)
9. Workery pracuji, vysledky se zapisuji do CrewExecutionTask
10. Leader cte aktualni stav (GET /execution/current), rozhoduje co dal
11. Vsechny tasky hotove → status → REVIEW → leader agreguje → COMPLETED
```

---

## 3. DATOVY MODEL

### 3.1 Nove enumy

```prisma
enum CrewExecutionStatus {
  PLANNING       // lidr analyzuje a planuje
  IN_PROGRESS    // workery pracuji
  REVIEW         // lidr kontroluje vysledky
  COMPLETED      // vsechno hotovo, uspesne
  FAILED         // neresitelny problem
  CANCELLED      // zruseno uzivatelem nebo lidrem
}

enum ExecutionTaskStatus {
  PENDING        // ceka na prirazeni nebo spusteni
  BLOCKED        // ceka na dependency (jiny task)
  IN_PROGRESS    // agent na tom pracuje
  COMPLETED      // hotovo, uspesne
  FAILED         // selhalo
  SKIPPED        // preskoceno (lidr rozhodl ze neni potreba)
}

enum HiringAutonomy {
  SUPERVISED     // lidr navrhe, clovek schvali
  SEMI_AUTO      // existujici agenti/skilly OK, nove vyzaduji schvaleni
  FULL_AUTO      // vse automaticky, jen budget limit
}
```

### 3.2 CrewExecution model

```prisma
// ============================================================
// CREW EXECUTION (Zakazka/projekt prirazeny lidrovi)
// ============================================================
// Zastresujici entita pro celou zakázku. Obsahuje plan, tasky,
// metriky a propojeni na vsechny relevantni sessions a delegace.
// Viz prd/CREW-EXECUTION.md pro kompletni specifikaci.

model CrewExecution {
  id              String               @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  org_id          String               @db.Uuid
  team_id         String               @db.Uuid
  leader_agent_id String               @db.Uuid
  trace_id        String               @unique  // "crew-exec-{uuid}"
  title           String                         // "Mesicni report socialnich siti"
  description     String?              @db.Text
  status          CrewExecutionStatus  @default(PLANNING)
  plan            Json?                          // strukturovany plan (tasky, dependencies, workflow)
  workflow_template String?                      // nazev sablony (null = ad-hoc)
  total_token_count Int?                         // celkovy pocet tokenu (vsechny tasky)
  total_estimated_cost Decimal?        @db.Decimal(10, 4)  // celkove naklady v USD
  created_at      DateTime             @default(now()) @db.Timestamptz
  updated_at      DateTime             @default(now()) @updatedAt @db.Timestamptz
  completed_at    DateTime?            @db.Timestamptz

  organization Organization    @relation(fields: [org_id], references: [id], onDelete: Cascade)
  team         Team             @relation(fields: [team_id], references: [id], onDelete: Cascade)
  leader       Agent            @relation("ExecutionLeader", fields: [leader_agent_id], references: [id])
  tasks        CrewExecutionTask[]

  @@index([org_id], name: "idx_execution_org")
  @@index([team_id], name: "idx_execution_team")
  @@index([leader_agent_id], name: "idx_execution_leader")
  @@index([status], name: "idx_execution_status")
  @@index([created_at], name: "idx_execution_created")
  @@map("crew_executions")
}
```

### 3.3 CrewExecutionTask model

```prisma
// ============================================================
// CREW EXECUTION TASK (Jednotlivy ukol v ramci execution)
// ============================================================
// Kazdy task = 1 radek v Execution Board tabulce.
// Obsahuje prirazeni agenta, status, casove udaje a naklady.

model CrewExecutionTask {
  id                String              @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  execution_id      String              @db.Uuid
  assigned_agent_id String?             @db.Uuid  // null = neprirazeno (ceka na hiring)
  title             String
  description       String?             @db.Text
  status            ExecutionTaskStatus @default(PENDING)
  order             Int                 @default(0)   // poradi v planu
  depends_on        String[]            @db.Uuid      // task IDs na ktere tento ceka
  iteration         Int                 @default(1)   // kolikaty pokus (pro dev-test loop)
  max_iterations    Int?                              // null = bez limitu (pouzije se workflow default)
  result_summary    String?             @db.Text      // shrnuti vysledku od agenta
  output_path       String?                           // cesta k vystupu v /output/
  error_message     String?             @db.Text
  delegation_log_id String?             @db.Uuid      // odkaz na DelegationLog (pro propojeni)
  token_count       Int?                              // pocet tokenu spotrebovanych na tento task
  estimated_cost    Decimal?            @db.Decimal(10, 4)  // naklady v USD
  started_at        DateTime?           @db.Timestamptz
  completed_at      DateTime?           @db.Timestamptz
  duration_ms       Int?                              // delka trvani v ms (computed)
  created_at        DateTime            @default(now()) @db.Timestamptz
  updated_at        DateTime            @default(now()) @updatedAt @db.Timestamptz

  execution CrewExecution @relation(fields: [execution_id], references: [id], onDelete: Cascade)
  agent     Agent?        @relation("TaskAssignee", fields: [assigned_agent_id], references: [id])

  @@index([execution_id], name: "idx_task_execution")
  @@index([assigned_agent_id], name: "idx_task_agent")
  @@index([status], name: "idx_task_status")
  @@map("crew_execution_tasks")
}
```

### 3.4 Zmeny v existujicich modelech

**Agent model — nove relace:**
```prisma
model Agent {
  // ... existujici pole ...

  // Nove relace pro Crew Execution
  led_executions    CrewExecution[]      @relation("ExecutionLeader")
  assigned_tasks    CrewExecutionTask[]  @relation("TaskAssignee")

  // Nove pole pro auto-hiring
  is_temporary      Boolean  @default(false)  // true = dynamicky vytvoreny lidrem
  hired_by_agent_id String?  @db.Uuid         // ktery lidr ho "najal"
  hired_at          DateTime? @db.Timestamptz
  expires_at        DateTime? @db.Timestamptz  // null = permanentni
}
```

**Team model — nove pole:**
```prisma
model Team {
  // ... existujici pole ...

  // Crew Execution konfigurace
  hiring_autonomy    HiringAutonomy @default(SUPERVISED)
  auto_loop_enabled  Boolean        @default(true)   // povoleni dev-test loop
  max_loop_iterations Int           @default(10)      // max iteraci v loop
  git_worktree_enabled Boolean      @default(false)   // git worktree pro dev tymy

  // Nove relace
  executions CrewExecution[]
}
```

**Organization model — nova relace:**
```prisma
model Organization {
  // ... existujici pole ...

  // Crew Execution
  executions CrewExecution[]
}
```

### 3.5 Entity Relationship (doplneni k existujicimu diagramu)

```
CrewExecution (1) ──── (*) CrewExecutionTask (*) ──── (1) Agent (assignee)
     │                        │
     │                        └── (0..1) DelegationLog (propojeni)
     │
     ├── (1) Organization
     ├── (1) Team
     └── (1) Agent (leader)
```

---

## 4. STORAGE MODEL

### 4.1 Dual storage: DB + JSONL mirror

| Data | Kde | Ucel |
|---|---|---|
| CrewExecution metadata | PostgreSQL | UI, dashboard, historie, dotazy |
| CrewExecutionTask zaznamy | PostgreSQL | Execution Board tabulka, real-time updaty |
| Progress log (detailni) | JSONL v /output/ | Agenti ctou/pisuji bez DB pristupu |
| Task vystupy (soubory) | /output/{agent-slug}/ | Persistentni deliverables |

### 4.2 JSONL mirror format

Cesta: `/output/{leader-slug}/crew-exec-{trace-id}/progress.jsonl`

```jsonl
{"ts":"2026-02-16T14:32:00Z","type":"execution_created","execution_id":"uuid","title":"Mesicni report"}
{"ts":"2026-02-16T14:32:05Z","type":"plan_created","tasks":[{"id":"t1","title":"Twitter data","agent":"bob"},{"id":"t2","title":"LinkedIn data","agent":"bob"}]}
{"ts":"2026-02-16T14:32:10Z","type":"task_started","task_id":"t1","agent":"bob"}
{"ts":"2026-02-16T14:35:00Z","type":"task_completed","task_id":"t1","agent":"bob","summary":"Stazeno 4,200 zaznamu","tokens":1200}
{"ts":"2026-02-16T14:35:01Z","type":"task_started","task_id":"t2","agent":"bob"}
{"ts":"2026-02-16T14:38:00Z","type":"task_completed","task_id":"t2","agent":"bob","summary":"Stazeno 5,100 zaznamu","tokens":980}
{"ts":"2026-02-16T14:38:05Z","type":"task_unblocked","task_id":"t4","reason":"dependencies [t1,t2,t3] completed"}
{"ts":"2026-02-16T14:38:10Z","type":"task_started","task_id":"t4","agent":"claudia"}
{"ts":"2026-02-16T14:50:00Z","type":"task_completed","task_id":"t4","agent":"claudia","summary":"15-strankovy report","output_path":"/output/claudia/report.pdf","tokens":8500}
{"ts":"2026-02-16T14:50:05Z","type":"execution_review","leader":"anna"}
{"ts":"2026-02-16T14:51:00Z","type":"execution_completed","total_tokens":15200,"total_cost":"$0.42","duration_ms":1140000}
```

**Proc JSONL mirror:**
- Agenti ctou progress bez DB pristupu (jen filesystem)
- Leader na zacatku kazde iterace nacte progress.jsonl jako kontext
- Drzime konzistenci s existujicim patternem (JSONL pro zpravy, logy)
- JSONL je append-only → bezpecne pro paralelni zapis

### 4.3 Synchronizace DB ↔ JSONL

```
Agent dokonci task:
  1. crewshipd updatne CrewExecutionTask v DB (source of truth)
  2. crewshipd appendne radek do progress.jsonl (mirror)
  3. crewshipd posle WebSocket event do UI (real-time update)

Pokud JSONL a DB diverguji → DB je source of truth, JSONL se regeneruje.
```

---

## 5. EXECUTION BOARD (UI)

### 5.1 Primarni view: tabulka (spreadsheet)

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  Crew Execution: Mesicni report socialnich siti                              │
│  Leader: Anna | Status: IN_PROGRESS | Started: 14:32 | Duration: 18m        │
├──────┬──────────────────┬──────────┬────────────┬─────────┬────────┬────────┤
│  #   │ Task             │ Agent    │ Status     │ Started │ Dur.   │ Cost   │
├──────┼──────────────────┼──────────┼────────────┼─────────┼────────┼────────┤
│  1   │ Twitter data     │ Bob      │ ✅ Done    │ 14:32   │ 3m     │ $0.03  │
│  2   │ LinkedIn data    │ Bob      │ ✅ Done    │ 14:32   │ 3m     │ $0.02  │
│  3   │ Instagram data   │ Bob      │ ✅ Done    │ 14:33   │ 4m     │ $0.03  │
│  4   │ Write report     │ Claudia  │ 🔄 Working │ 14:41   │ 9m+    │ $0.18  │
│  5   │ SEO optimize     │ Dave     │ ⏳ Blocked │ --      │ --     │ --     │
├──────┴──────────────────┴──────────┴────────────┴─────────┴────────┴────────┤
│  Total: 5 tasks | 3 done | 1 working | 1 blocked | Est. cost: ~$0.42       │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 5.2 Sloupce

| Sloupec | Zdroj | Popis |
|---|---|---|
| # | `order` | Poradi v planu |
| Task | `title` | Nazev ukolu |
| Agent | `assigned_agent_id → Agent.name` | Prirazeny agent |
| Status | `status` | PENDING/BLOCKED/IN_PROGRESS/COMPLETED/FAILED/SKIPPED |
| Iteration | `iteration` | Cislo iterace (pro loop — "2nd try") |
| Started | `started_at` | Cas zahajeni |
| Duration | `duration_ms` (nebo live vypocet) | Jak dlouho trva/trvalo |
| Cost | `estimated_cost` | Token naklady v USD |
| Result | `result_summary` (tooltip/expand) | Shrnuti vysledku |

### 5.3 Kde se zobrazi

1. **Team page** (`/teams/{teamId}`) — sekce "Active Executions" s posledni aktivni execution
2. **Agent detail** (`/agents/{agentId}`) — pro leadera: seznam jeho execution
3. **Dedicated page** (`/teams/{teamId}/executions/{executionId}`) — plny detail s tabulkou
4. **Dashboard** (`/`) — widget "Running Executions" s top-level prehledem

### 5.4 Real-time updaty

```
crewshipd → WebSocket event → Zustand store → React component re-render

Event typy:
  execution.created      → pridani do seznamu
  execution.status       → zmena statusu execution
  task.status            → zmena statusu tasku v tabulce
  task.assigned          → prirazeni agenta k tasku
  task.progress          → prubezny update (tokeny, cas)
  execution.completed    → finalni stav
```

### 5.5 Historie

Vsechny dokoncene executions zustavaji v DB. Uzivatel muze:
- Prohlizet historii execution per tym a per lidr
- Filtrovat podle statusu, data, nakladu
- Prokliknout se do detailu (tabulka + delegacni timeline)
- Videt trendy: prumerna doba, prumerne naklady, uspesnost

---

## 6. DEV-TEST LOOP (WORKFLOW SABLONY)

### 6.1 Koncept

Workflow sablona = preddefinovana sekvence kroku ktere se opakuji.
Leader muze pouzit sablonu jako zaklad a overridnout za behu (active mode).

### 6.2 Sablona format (JSON)

```json
{
  "name": "dev-test-loop",
  "display_name": "Development → Testing Loop",
  "description": "Developer implementuje, tester testuje, pri failu se vraci zpet.",
  "steps": [
    {
      "id": "implement",
      "role_title": "developer",
      "action": "implement",
      "description": "Implementuj featuru podle zadani",
      "output_type": "code"
    },
    {
      "id": "test",
      "role_title": "tester",
      "action": "test",
      "description": "Otestuj implementaci (unit testy, integracni testy, manualni review)",
      "output_type": "test_result"
    },
    {
      "id": "loop_check",
      "type": "condition",
      "condition": "steps.test.output.status == 'fail'",
      "on_true": "implement",
      "on_false": "done"
    }
  ],
  "max_iterations": 10,
  "leader_override": true,
  "iteration_context": "append"
}
```

### 6.3 Vestavene sablony

| Sablona | Popis | Kroky |
|---|---|---|
| `dev-test-loop` | Developer → Tester → zpet | implement → test → (fail?) → implement |
| `research-write-review` | Sber dat → psani → review | research → write → review → (fail?) → write |
| `sequential` | Jednoduchy sekvencni plan | task1 → task2 → task3 |
| `parallel-aggregate` | Paralelni sber → agregace | [task1, task2, task3] → aggregate |

### 6.4 Jak loop funguje

```
Iterace 1:
  1. Leader vytvori execution s sablonou "dev-test-loop"
  2. Task "implement" → prirazeno developer agentovi
  3. Developer implementuje, dokonci → task COMPLETED
  4. Task "test" se odblokuje → prirazeno tester agentovi
  5. Tester testuje → vysledek: FAIL (3 testy selhaly)
  6. Condition check: status == 'fail' → goto "implement"
  7. Leader dostane notifikaci: "Iterace 1 selhala, 3 failing testy"

Iterace 2:
  8. Novy task "implement" (iteration=2) → developer dostane:
     - Puvodni zadani
     - Vysledky testu z iterace 1 (error messages)
     - Kontext z progress.jsonl
  9. Developer opravuje, dokonci → task COMPLETED
  10. Novy task "test" (iteration=2) → tester testuje
  11. Vysledek: PASS → condition: status != 'fail' → done
  12. Execution → REVIEW → leader zkontroluje → COMPLETED
```

### 6.5 Kontext mezi iteracemi (Ralph Loop inspirace)

**External state pattern:**
- Kazda iterace cte `progress.jsonl` — vi co se stalo drive
- Developer v iteraci 2 dostane do kontextu:
  - Puvodni zadani
  - Svuj kod z iterace 1 (na filesystemu — `/workspace/` nebo git)
  - Chybove hlasky od testera
  - Summary z progress.jsonl
- Zadna AI pamet mezi iteracemi — vsechno je na filesystemu

**Git worktree (volitelne, pro dev tymy):**
- Kazdy worker dostane vlastni git worktree (branch)
- Developer pracuje na `feature/task-{id}`, tester testuje na stejne branch
- Po uspesnem testu lidr merguje do main
- Viz sekce 9 pro detaily

### 6.6 Leader override

V active mode muze leader:
- Zmenit prirazeni agenta za behu ("Bob to nezvlada, dej to Alici")
- Pridat/odebrat tasky z planu
- Preskocit krok (SKIPPED status)
- Zmenit max_iterations
- Ukoncit loop predcasne ("tohle uz je dost dobre")
- Eskalovat na uzivatele ("potrebuji lidsky vstup")

---

## 7. AUTO-HIRING (DYNAMICKE VYTVARI AGENTU)

### 7.1 Koncept

Leader muze za behu zjistit, ze potrebuje specialistu ktereho nema v tymu.
Misto cekani na cloveka muze "najmat" — vytvorit noveho agenta nebo priradit
existujiciho z organizace.

### 7.2 Tri urovne autonomie

Konfigurovano per-tym v `Team.hiring_autonomy`:

| Uroven | Kdo rozhoduje | Co muze lidr |
|---|---|---|
| **SUPERVISED** | Clovek vzdy schvaluje | Lidr navrhe, UI zobrazi notifikaci, clovek schvali/zamitne |
| **SEMI_AUTO** | Lidr pro existujici, clovek pro nove | Lidr priradi existujici agenty/skilly. Nove agenty vytvori jen se schvalenim |
| **FULL_AUTO** | Lidr sam (budget limit) | Lidr vytvari agenty, prirazuje marketplace skilly. Omezeno jen budget limitem |

### 7.3 Sidecar API

```
POST http://localhost:9119/hire
X-Crewship-Session: {session-id}
Content-Type: application/json

{
  "skill_description": "Potrebuji specialistu na Tailwind CSS a responsive design",
  "source": "auto",         // "existing" | "marketplace" | "create_new" | "auto"
  "estimated_duration": "2h",
  "budget_limit_usd": 5.00
}
```

**Response (SUPERVISED — ceka na schvaleni):**
```json
{
  "hire_request_id": "uuid",
  "status": "pending_approval",
  "message": "Navrh na najeti agenta odeslan ke schvaleni",
  "suggested": {
    "type": "existing",
    "agent_id": "uuid",
    "agent_name": "CSS Specialist",
    "match_score": 0.87
  }
}
```

**Response (FULL_AUTO — okamzite vytvoreni):**
```json
{
  "hire_request_id": "uuid",
  "status": "hired",
  "agent_id": "uuid",
  "agent_slug": "css-specialist-temp",
  "agent_name": "CSS Specialist",
  "type": "created",
  "is_temporary": true,
  "expires_at": "2026-02-16T18:00:00Z"
}
```

### 7.4 Hiring flow

```
1. Leader posle POST /hire na sidecar
2. Sidecar validuje: RBAC, team hiring_autonomy, budget
3. Sidecar forwardne do crewshipd
4. crewshipd podle zdroje:
   a) "existing": prohleda agenty v organizaci, match podle popisu
   b) "marketplace": prohleda Skill Hub, najde relevantni skilly
   c) "create_new": vytvori noveho agenta s dynamickym system promptem
   d) "auto": zkusi a) → b) → c) v tomto poradi
5. Podle autonomie:
   a) SUPERVISED: crewshipd vytvori HireRequest, posle WebSocket notifikaci do UI
      → clovek schvali → crewshipd dokonci hiring → notifikace lidrovi
   b) SEMI_AUTO: existujici = rovnou, nove = notifikace + schvaleni
   c) FULL_AUTO: rovnou vytvori/priradi, zaloguje do audit logu
6. Novy/prirazeny agent se objevi v CrewExecutionTask jako assignee
7. Audit log: "agent.hired" s metadata (kdo, proc, kolik to stalo)
```

### 7.5 Docasni agenti

- `Agent.is_temporary = true` — oznaceni docasneho agenta
- `Agent.hired_by_agent_id` — ktery lidr ho najal
- `Agent.expires_at` — po expiraci se agent deaktivuje (soft delete)
- V UI: badge "Temporary" / "Hired by {leader}"
- Audit log zaznamenava celý lifecycle (hired → worked → expired)

### 7.6 RBAC a bezpecnost

- Auto-hiring podleha team-level RBAC (kdo ma pristup k tymu)
- Budget limit per-team a per-execution (prevence runaway nakladu)
- Docasny agent dedi credentials od tymu (stejne jako normalni agent)
- Docasny agent je omezen na danou execution (nemuze pracovat na jinych)
- Marketplace skilly podlehaji verifikacnimu pipeline (ADR-020)

---

## 8. CROSS-TEAM SPOLUPRACE PRES DIRECTORA

### 8.1 Hub-and-spoke pattern

```
Marketing Leader ──→ Director ──→ Development Leader
                         │
                         └──→ Finance Leader
```

Vsechna cross-team komunikace jde pres Directora (bezpecnejsi, auditovatelne).
Primo lidr-lidr komunikace az Phase 3.

### 8.2 Director konfigurace (styly)

Director ma konfigurovatelny styl (per-org nastaveni):

| Styl | LLM model | Co dela | Token naklady |
|---|---|---|---|
| **Passive router** | Haiku/GPT-4o-mini | Jen parsuje a routne na spravny tym | Nizke (~$0.001/req) |
| **Dual model** | Mini pro routing, Opus pro agregaci | Routne levne, agreguje chytre | Stredni |
| **Full reasoning** | Opus/GPT-4o | Plne reasoning, strategicke rozhodovani | Vysoke |
| **Budget-limited** | Konfigurovatelny | Full reasoning ale s token/cost limitem | Strop |

### 8.3 Director flow pri cross-team pozadavku

```
1. Marketing Leader: "Potrebuji informace o nakladech na servery"
   → POST /delegate s team_target: "finance"
2. Sidecar forwardne do crewshipd
3. crewshipd spusti Directora (lightweight LLM call, viz ORCHESTRATION.md 5.2)
4. Director (v routing mode): parsuje pozadavek → routne na Finance Leader
5. crewshipd deleguje na Finance Leadera
6. Finance Leader zpracuje (pripadne deleguje na sve workery)
7. Vysledek → Director → Marketing Leader
8. Marketing Leader vlozi vysledek do sve execution
```

### 8.4 Setreni tokenu

- **Passive router** nepouziva reasoning — jen pattern matching na team popis
- crewshipd cachuje team/leader informace — director nedostava plny kontext kazdy request
- Worker output compression (ADR-005) — director dostava sumarizace, ne plne vystupy
- Budget limit per-director zabraní runaway nakladum

---

## 9. GIT WORKTREE PRO DEV TYMY

### 9.1 Koncept

Pro development tymy kde agenti pracuji na kodu:
kazdy worker dostane vlastni git worktree (branch) pro paralelni praci
na stejnem repozitari.

### 9.2 Flow

```
1. Execution "Implementuj OAuth2 login":
   Task 1: Backend API endpoints (Alice)
   Task 2: Frontend login form (Charlie)
   Task 3: QA testing (Diana)

2. crewshipd pripravi worktrees:
   git worktree add /workspace/alice/oauth feature/oauth-backend-{task-id}
   git worktree add /workspace/charlie/oauth feature/oauth-frontend-{task-id}

3. Alice a Charlie pracuji paralelne na svych branch

4. Po dokonceni:
   a) Leader (Tomas) review kodu obou branches
   b) Leader merguje do hlavni branch (nebo deleguje na QA)
   c) Diana testuje na mergnutem kodu
   d) Pri failu → loop zpet na developera se specifickou branch

5. Cleanup: crewshipd odstrani worktrees po dokonceni execution
```

### 9.3 Konfigurace

- `Team.git_worktree_enabled = true` — zapnuto per-tym
- Pouze pro tymy kde agenti pracuji s git repozitarem
- Repozitar musi byt naklonovan v `/workspace/` kontejneru
- Leader ma merge prava, workery maji jen svou branch

### 9.4 Omezeni (MVP)

- 1 repozitar per team container (MVP)
- Max 5 simultaneoustich worktrees (prevence disk space)
- Automaticky cleanup po 24h neaktivity
- Merge konflikty eskaluje leader na uzivatele

---

## 10. SIDECAR API ROZSIRENI

### 10.1 Nove endpointy (doplneni k existujicim z ORCHESTRATION.md)

```
CREW EXECUTION:
  POST /execution/create        — vytvorit crew execution
  POST /execution/plan          — nastavit/updatovat plan (tasky)
  PATCH /execution/task/:id     — updatovat status/result tasku
  GET  /execution/:id           — precist aktualni stav execution + tasks
  GET  /execution/current       — aktualni execution pro tento tym
  GET  /execution/history       — seznam dokoncenych execution

AUTO-HIRING:
  POST /hire                    — pozadat o noveho specialistu
  GET  /hire/:id                — stav hire requestu
  DELETE /hire/:id              — zrusit hire request

WORKFLOW:
  GET  /workflows               — seznam dostupnych sablon
  POST /execution/apply-workflow — aplikovat sablonu na execution
```

### 10.2 Validace

Vsechny nove endpointy podlehaji:
- Session-id overeni (X-Crewship-Session header)
- RBAC check (jen leader/director mohou vytvaret executions)
- Team membership check
- Budget check (pro hiring a execution)

---

## 11. GO SERVICE — NOVA LOGIKA

### 11.1 ExecutionEngine (novy modul v crewshipd)

```go
// internal/orchestrator/execution.go

type ExecutionEngine struct {
    db           *prisma.Client      // Prisma DB pristup (pres IPC do Next.js)
    delegator    *DelegationEngine   // existujici delegacni engine
    ws           *ws.Server          // WebSocket pro real-time updaty
    wal          *state.WAL          // bbolt WAL pro crash recovery
}

type CreateExecutionRequest struct {
    OrgID       string
    TeamID      string
    LeaderID    string
    Title       string
    Description string
    TraceID     string   // "crew-exec-{uuid}"
}

type TaskPlan struct {
    Tasks []TaskDefinition `json:"tasks"`
}

type TaskDefinition struct {
    Title       string   `json:"title"`
    Description string   `json:"description"`
    AgentSlug   string   `json:"agent"`      // slug agenta nebo role_title
    Order       int      `json:"order"`
    DependsOn   []string `json:"depends_on"` // task IDs nebo indexy
}
```

### 11.2 Dependency resolution

```go
// internal/orchestrator/dependency.go

// ResolveReadyTasks vraci tasky ktere maji vsechny dependencies splnene
func (e *ExecutionEngine) ResolveReadyTasks(exec *CrewExecution) []CrewExecutionTask {
    completed := map[string]bool{}
    for _, t := range exec.Tasks {
        if t.Status == "COMPLETED" {
            completed[t.ID] = true
        }
    }

    var ready []CrewExecutionTask
    for _, t := range exec.Tasks {
        if t.Status != "PENDING" && t.Status != "BLOCKED" {
            continue
        }
        allDepsComplete := true
        for _, dep := range t.DependsOn {
            if !completed[dep] {
                allDepsComplete = false
                break
            }
        }
        if allDepsComplete {
            ready = append(ready, t)
        }
    }
    return ready
}
```

### 11.3 Loop controller

```go
// internal/orchestrator/loop.go

type LoopController struct {
    engine *ExecutionEngine
}

// CheckLoopCondition kontroluje workflow sablonu a rozhoduje o dalsi iteraci
func (lc *LoopController) CheckLoopCondition(
    exec *CrewExecution,
    task *CrewExecutionTask,
    workflow *WorkflowTemplate,
) LoopDecision {
    if task.Iteration >= exec.MaxIterations() {
        return LoopDecision{Action: "stop", Reason: "max iterations reached"}
    }

    step := workflow.FindStep(task.StepID)
    if step.Type != "condition" {
        return LoopDecision{Action: "continue"}
    }

    // Evaluate condition (napr. "test_result.status == 'fail'")
    result := evaluateCondition(step.Condition, task.ResultSummary)
    if result {
        return LoopDecision{
            Action:    "loop",
            GotoStep:  step.OnTrue,
            Iteration: task.Iteration + 1,
        }
    }
    return LoopDecision{Action: "done"}
}
```

### 11.4 JSONL progress writer

```go
// internal/orchestrator/progress_writer.go

type ProgressWriter struct {
    basePath string // /output/{leader-slug}/crew-exec-{trace-id}/
}

func (pw *ProgressWriter) WriteEvent(event ProgressEvent) error {
    path := filepath.Join(pw.basePath, "progress.jsonl")
    f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
    if err != nil {
        return fmt.Errorf("open progress file: %w", err)
    }
    defer f.Close()

    event.Timestamp = time.Now().UTC()
    data, _ := json.Marshal(event)
    _, err = f.Write(append(data, '\n'))
    return err
}
```

---

## 12. BEZPECNOST

### 12.1 RBAC pro Crew Execution

| Akce | OWNER | ADMIN | MANAGER | MEMBER | VIEWER |
|---|---|---|---|---|---|
| Zobrazit execution board | Vsechny tymy | Vsechny tymy | Prirazene tymy | Prirazene tymy | Prirazene tymy (read-only) |
| Vytvorit execution (pres chat) | Ano | Ano | Prirazene tymy | Ne | Ne |
| Zrusit execution | Ano | Ano | Prirazene tymy | Ne | Ne |
| Schvalit hiring request | Ano | Ano | Prirazene tymy | Ne | Ne |
| Zmenit team hiring_autonomy | Ano | Ano | Ne | Ne | Ne |

### 12.2 Agent-level omezeni

- Jen LEADER a DIRECTOR mohou vytvaret executions
- Worker nemuze pristupovat k /execution/ endpointum na sidecar
- Docasny agent je omezen na svou execution (nemuze delegovat dal)
- Budget limit zabraní runaway nakladum

### 12.3 Audit log

Vsechny akce se loguji:

```
execution.created      — kdo/co vytvorilo execution
execution.plan_set     — lidr nastavil plan
execution.completed    — finalni stav + metriky
task.assigned          — prirazeni agenta k tasku
task.completed         — dokonceni tasku + vysledek
agent.hired            — dynamicke najeti agenta
agent.hire_approved    — schvaleni hire requestu
agent.hire_rejected    — zamitnut hire requestu
workflow.applied       — pouziti workflow sablony
workflow.overridden    — lidr overridnul sablonu
```

---

## 13. FAZOVANI IMPLEMENTACE

### Phase 2A: Zakladni Crew Execution

| ID | Feature | Popis |
|---|---|---|
| EXEC-01 | CrewExecution + CrewExecutionTask Prisma modely | Nove tabulky + enumy + migrace |
| EXEC-02 | Sidecar endpointy pro execution | /execution/create, /plan, /task/:id, /current |
| EXEC-03 | ExecutionEngine v crewshipd | Vytvareni, plan, dependency resolution |
| EXEC-04 | JSONL progress writer | Mirror do /output/, append-only |
| EXEC-05 | Execution Board UI (tabulka) | Spreadsheet view pod /teams/{id}/executions/{id} |
| EXEC-06 | WebSocket real-time updaty | execution.* a task.* eventy |
| EXEC-07 | Execution historie | Seznam dokoncenych execution per tym |
| EXEC-08 | Dashboard widget | "Running Executions" na hlavni strance |

### Phase 2B: Workflow sablony + Auto-hiring

| ID | Feature | Popis |
|---|---|---|
| EXEC-09 | Workflow sablony (JSON format) | Vestavene sablony (dev-test-loop, sequential, parallel) |
| EXEC-10 | Loop controller v crewshipd | Condition check, iterace, max_iterations |
| EXEC-11 | Dev-test loop integrace | Developer → Tester → zpet pattern |
| EXEC-12 | Auto-hiring: SUPERVISED mode | POST /hire, UI notifikace, schvaleni |
| EXEC-13 | Auto-hiring: SEMI_AUTO mode | Automaticke prirazeni existujicich |
| EXEC-14 | Team hiring_autonomy nastaveni v UI | Konfigurace per-tym |
| EXEC-15 | Docasni agenti (is_temporary) | Lifecycle: hired → working → expired |
| EXEC-16 | Director routing mode | Passive router pro cross-team pozadavky |
| EXEC-17 | Inline metriky v Execution Board | Duration, token count, cost per task |

### Phase 3: Pokrocile funkce

| ID | Feature | Popis |
|---|---|---|
| EXEC-18 | Auto-hiring: FULL_AUTO mode | Plne autonomni vcetne marketplace skillů |
| EXEC-19 | Git worktree integrace | Per-worker branch, leader merge |
| EXEC-20 | Cross-team execution (director level) | Director koordinuje execution pres vice tymu |
| EXEC-21 | Execution replay/debug | Prehrat celou execution krok po kroku |
| EXEC-22 | Primo lidr-lidr komunikace | Bez directora, s RBAC |
| EXEC-23 | Execution analytics (trendy) | Grafy, srovnani efektivity, historicke trendy |
| EXEC-24 | Custom workflow sablony | Uzivatel si vytvari vlastni sablony v UI |
| EXEC-25 | Director full reasoning + budget limit | Plny director s konfigurovatelnym stropem |

---

## 14. SROVNANI S EXISTUJICIMI KONCEPTY

| Aspekt | Ralph Loop | OpenClaw Sessions | CrewAI Hierarchical | **Crewship Execution** |
|---|---|---|---|---|
| Scope | Single agent, iterativni | Multi-session messaging | Manager → workers | **Leader → workers + loop** |
| Progress tracking | Filesystem (PROGRESS.md) | Session history | Pipeline implicit | **DB + JSONL + UI Board** |
| Autonomie | Plna (agent sam iteruje) | Session spawn (omezene) | Kod-definovane | **3 urovne (configurable)** |
| Workflow | Implicit (same prompt loop) | Manual session mgmt | Python dekorator | **JSON sablony + override** |
| Cross-team | N/A | N/A | Nested crews | **Director hub-and-spoke** |
| UI | Terminal only | ClawDeck | Terminal/LangSmith | **Tabulkovy Board + dashboard** |
| Git integrace | Git commits per iterace | Ne | Ne | **Git worktree per worker** |
| Hiring | N/A | N/A | Kod-definovane agenty | **Dynamicke auto-hiring** |

---

## 15. OTEVRENE OTAZKY

### Rozhodnute v teto verzi

1. **Storage model:** DB + JSONL mirror (DB source of truth, JSONL pro agenty)
2. **Cross-team:** Pres directora (hub-and-spoke), primo lidr-lidr az Phase 3
3. **Hiring autonomie:** Konfigurovatelne per-tym (supervised/semi-auto/full-auto)
4. **UI styl:** Tabulka (spreadsheet) jako primarni view
5. **Workflow override:** Ano — sablony jako zaklad, leader muze overridnout
6. **Git worktree:** Ano, volitelne pro dev tymy
7. **Director styly:** Konfigurovatelne (passive router, dual model, full reasoning, budget-limited)

### Stale otevrene

1. **Execution budget:** Jak nastavit budget limit per execution? Per-task nebo celkovy?
2. **Notification system:** Jak notifikovat cloveka pri SUPERVISED hiring? WebSocket + email?
3. **Workflow marketplace:** Budou workflow sablony sdilelne pres marketplace jako skilly?
4. **Execution templates:** Muze uzivatel ulozit celou execution jako sablonu pro opakovani?
5. **Concurrent executions:** Muze lidr bezet vice execution naraz? Pravdepodobne ano s limitem.
6. **Execution priority:** Muze uzivatel prioritizovat execution (urgentni vs normalni)?
7. **Human-in-the-loop:** Muze task mit status "AWAITING_HUMAN" kde ceka na lidsky vstup?

---

*Tento dokument doplnuje ORCHESTRATION.md o koncepty Crew Execution, Workflow sablon,
Auto-hiringu a Progress trackingu. Implementace podle fazovani v sekci 13.*
