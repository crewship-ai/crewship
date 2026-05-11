# Crewship -- Mission & Progress Tracking (CREW-EXECUTION.md)

**Verze:** 1.0
**Datum:** 2026-02-16
**Status:** Architekturni navrh (implementace Phase 2A/2B/3)
**Zavislosti:** ORCHESTRATION.md (assignments, sidecar, lead modes),
architecture.md (kontejnery, sidecar API, loop modes -- predchazejici spec doc retired commit `dd86356`),
DATABASE.md (existujici Prisma schema)

---

## 1. VIZE

Mission je **zastresujici entita pro celou zakázku/ukol** zadanou Lead agentovi.
Inspirace: realny firemni workflow — sef dostane projekt, rozlozi ho na ukoly,
priradi lidem, sleduje postup, iteruje dokud neni hotovo.

**Klicove principy:**
- **External state, not context** (inspirace Ralph Loop) — postup se drzi v DB a na
  filesystemu, ne v AI pameti. Kazda iterace cte aktualni stav.
- **Tabulkovy Mission Board** — uzivatel vidi spreadsheet s ukoly, agenty, statusy,
  casovymi udaji a naklady. Analogie k realnemu project managementu.
- **Iterativni loop** — developer → tester → zpet, dokud neni hotovo.
  Workflow sablony pro opakovane patterny, lead muze overridnout.
- **Autonomni hiring** — lead muze dynamicky "najmat" nove agenty/skilly
  s konfigurovatelnou urovni autonomie (supervised/semi-auto/full-auto).

---

## 2. MISSION — ZAKLADNI KONCEPT

### 2.1 Co je Mission

```
Mission = 1 zakázka/projekt zadany lead agentovi
  ├── trace_id: "mission-{uuid}" (prolinkovani vsech chats, assignments, logu)
  ├── plan: strukturovany plan (JSON s tasky a dependencies)
  ├── tasks[]: jednotlive ukoly prirazene agentum
  ├── status: PLANNING → IN_PROGRESS → REVIEW → COMPLETED/FAILED/CANCELLED
  └── JSONL mirror: /output/{lead-slug}/mission-{id}/progress.jsonl
```

### 2.2 Lifecycle

```
PLANNING       Lead analyzuje ukol, vytvari plan a rozdeluje na tasky
     │
     ▼
IN_PROGRESS    Agenti pracuji na taskech, lead koordinuje
     │
     ▼
REVIEW         Vsechny tasky hotove, lead kontroluje vysledky
     │
     ├──→ COMPLETED   Lead spokojeny, odpovida uzivateli
     ├──→ FAILED      Neresitelny problem, lead reportuje
     └──→ CANCELLED   Uzivatel nebo lead zrusil mission
```

**Kdo vytvari Mission:**
- crewshipd automaticky pri kazdem netrivialnim ukolu zadanem lead agentovi
- Lead rozhodne jestli ukol vyzaduje mission (jednoduche otazky = ne)
- Webhook trigger muze vytvorit mission automaticky
- Coordinator prirazuje lead agentovi → automaticky se vytvori mission

### 2.3 Trigger flow

```
1. Uzivatel: "Pripravte mesicni report socialnich siti"
2. crewshipd prijme pres WebSocket, spusti lead agenta (Docker exec)
3. Lead analyzuje ukol → rozhodne: "tohle je projekt, ne jednoducha odpoved"
4. Lead posle POST /mission/create na sidecar
5. crewshipd vytvori Mission v DB (status: PLANNING)
6. Lead posle POST /mission/plan s rozlozenym planem
7. crewshipd ulozi plan, vytvori MissionTask zaznamy, status → IN_PROGRESS
8. Lead prirazuje prvni vlnu tasku (POST /assign)
9. Agenti pracuji, vysledky se zapisuji do MissionTask
10. Lead cte aktualni stav (GET /mission/current), rozhoduje co dal
11. Vsechny tasky hotove → status → REVIEW → lead agreguje → COMPLETED
```

---

## 3. DATOVY MODEL

### 3.1 Nove enumy

```prisma
enum MissionStatus {
  PLANNING       // lead analyzuje a planuje
  IN_PROGRESS    // agenti pracuji
  REVIEW         // lead kontroluje vysledky
  COMPLETED      // vsechno hotovo, uspesne
  FAILED         // neresitelny problem
  CANCELLED      // zruseno uzivatelem nebo lead agentem
}

enum MissionTaskStatus {
  PENDING        // ceka na prirazeni nebo spusteni
  BLOCKED        // ceka na dependency (jiny task)
  IN_PROGRESS    // agent na tom pracuje
  COMPLETED      // hotovo, uspesne
  FAILED         // selhalo
  SKIPPED        // preskoceno (lead rozhodl ze neni potreba)
}

enum HiringAutonomy {
  SUPERVISED     // lead navrhe, clovek schvali
  SEMI_AUTO      // existujici agenti/skilly OK, nove vyzaduji schvaleni
  FULL_AUTO      // vse automaticky, jen budget limit
}
```

### 3.2 Mission model

```prisma
// ============================================================
// MISSION (Zakazka/projekt prirazeny lead agentovi)
// ============================================================
// Zastresujici entita pro celou zakázku. Obsahuje plan, tasky,
// metriky a propojeni na vsechny relevantni chats a assignments.
// Viz prd/CREW-EXECUTION.md pro kompletni specifikaci.

model Mission {
  id              String          @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  workspace_id    String          @db.Uuid
  crew_id         String          @db.Uuid
  lead_agent_id   String          @db.Uuid
  trace_id        String          @unique  // "mission-{uuid}"
  title           String                   // "Mesicni report socialnich siti"
  description     String?         @db.Text
  status          MissionStatus   @default(PLANNING)
  plan            Json?                    // strukturovany plan (tasky, dependencies, workflow)
  workflow_template String?                // nazev sablony (null = ad-hoc)
  total_token_count Int?                   // celkovy pocet tokenu (vsechny tasky)
  total_estimated_cost Decimal?   @db.Decimal(10, 4)  // celkove naklady v USD
  created_at      DateTime        @default(now()) @db.Timestamptz
  updated_at      DateTime        @default(now()) @updatedAt @db.Timestamptz
  completed_at    DateTime?       @db.Timestamptz

  workspace Workspace    @relation(fields: [workspace_id], references: [id], onDelete: Cascade)
  crew      Crew         @relation(fields: [crew_id], references: [id], onDelete: Cascade)
  lead      Agent        @relation("MissionLead", fields: [lead_agent_id], references: [id])
  tasks     MissionTask[]

  @@index([workspace_id], name: "idx_mission_workspace")
  @@index([crew_id], name: "idx_mission_crew")
  @@index([lead_agent_id], name: "idx_mission_lead")
  @@index([status], name: "idx_mission_status")
  @@index([created_at], name: "idx_mission_created")
  @@map("missions")
}
```

### 3.3 MissionTask model

```prisma
// ============================================================
// MISSION TASK (Jednotlivy ukol v ramci mission)
// ============================================================
// Kazdy task = 1 radek v Mission Board tabulce.
// Obsahuje prirazeni agenta, status, casove udaje a naklady.

model MissionTask {
  id                String             @id @default(dbgenerated("gen_random_uuid()")) @db.Uuid
  mission_id        String             @db.Uuid
  assigned_agent_id String?            @db.Uuid  // null = neprirazeno (ceka na hiring)
  title             String
  description       String?            @db.Text
  status            MissionTaskStatus  @default(PENDING)
  order             Int                @default(0)   // poradi v planu
  depends_on        String[]           @db.Uuid      // task IDs na ktere tento ceka
  iteration         Int                @default(1)   // kolikaty pokus (pro dev-test loop)
  max_iterations    Int?                              // null = bez limitu (pouzije se workflow default)
  result_summary    String?            @db.Text      // shrnuti vysledku od agenta
  output_path       String?                           // cesta k vystupu v /output/
  error_message     String?            @db.Text
  assignment_id     String?            @db.Uuid      // odkaz na Assignment (pro propojeni)
  token_count       Int?                              // pocet tokenu spotrebovanych na tento task
  estimated_cost    Decimal?           @db.Decimal(10, 4)  // naklady v USD
  started_at        DateTime?          @db.Timestamptz
  completed_at      DateTime?          @db.Timestamptz
  duration_ms       Int?                              // delka trvani v ms (computed)
  created_at        DateTime           @default(now()) @db.Timestamptz
  updated_at        DateTime           @default(now()) @updatedAt @db.Timestamptz

  mission Mission @relation(fields: [mission_id], references: [id], onDelete: Cascade)
  agent   Agent?  @relation("TaskAssignee", fields: [assigned_agent_id], references: [id])

  @@index([mission_id], name: "idx_task_mission")
  @@index([assigned_agent_id], name: "idx_task_agent")
  @@index([status], name: "idx_task_status")
  @@map("mission_tasks")
}
```

### 3.4 Zmeny v existujicich modelech

**Agent model — nove relace:**
```prisma
model Agent {
  // ... existujici pole ...

  // Nove relace pro Mission
  led_missions      Mission[]       @relation("MissionLead")
  assigned_tasks    MissionTask[]   @relation("TaskAssignee")

  // Nove pole pro auto-hiring
  is_temporary      Boolean  @default(false)  // true = dynamicky vytvoreny lead agentem
  hired_by_agent_id String?  @db.Uuid         // ktery lead ho "najal"
  hired_at          DateTime? @db.Timestamptz
  expires_at        DateTime? @db.Timestamptz  // null = permanentni
}
```

**Crew model — nove pole:**
```prisma
model Crew {
  // ... existujici pole ...

  // Mission konfigurace
  hiring_autonomy    HiringAutonomy @default(SUPERVISED)
  auto_loop_enabled  Boolean        @default(true)   // povoleni dev-test loop
  max_loop_iterations Int           @default(10)      // max iteraci v loop
  git_worktree_enabled Boolean      @default(false)   // git worktree pro dev crews

  // Nove relace
  missions Mission[]
}
```

**Workspace model — nova relace:**
```prisma
model Workspace {
  // ... existujici pole ...

  // Missions
  missions Mission[]
}
```

### 3.5 Entity Relationship (doplneni k existujicimu diagramu)

```
Mission (1) ──── (*) MissionTask (*) ──── (1) Agent (assignee)
     │                        │
     │                        └── (0..1) Assignment (propojeni)
     │
     ├── (1) Workspace
     ├── (1) Crew
     └── (1) Agent (lead)
```

---

## 4. STORAGE MODEL

### 4.1 Dual storage: DB + JSONL mirror

| Data | Kde | Ucel |
|---|---|---|
| Mission metadata | PostgreSQL | UI, dashboard, historie, dotazy |
| MissionTask zaznamy | PostgreSQL | Mission Board tabulka, real-time updaty |
| Progress log (detailni) | JSONL v /output/ | Agenti ctou/pisuji bez DB pristupu |
| Task vystupy (soubory) | /output/{agent-slug}/ | Persistentni deliverables |

### 4.2 JSONL mirror format

Cesta: `/output/{lead-slug}/mission-{trace-id}/progress.jsonl`

```jsonl
{"ts":"2026-02-16T14:32:00Z","type":"mission_created","mission_id":"uuid","title":"Mesicni report"}
{"ts":"2026-02-16T14:32:05Z","type":"plan_created","tasks":[{"id":"t1","title":"Twitter data","agent":"bob"},{"id":"t2","title":"LinkedIn data","agent":"bob"}]}
{"ts":"2026-02-16T14:32:10Z","type":"task_started","task_id":"t1","agent":"bob"}
{"ts":"2026-02-16T14:35:00Z","type":"task_completed","task_id":"t1","agent":"bob","summary":"Stazeno 4,200 zaznamu","tokens":1200}
{"ts":"2026-02-16T14:35:01Z","type":"task_started","task_id":"t2","agent":"bob"}
{"ts":"2026-02-16T14:38:00Z","type":"task_completed","task_id":"t2","agent":"bob","summary":"Stazeno 5,100 zaznamu","tokens":980}
{"ts":"2026-02-16T14:38:05Z","type":"task_unblocked","task_id":"t4","reason":"dependencies [t1,t2,t3] completed"}
{"ts":"2026-02-16T14:38:10Z","type":"task_started","task_id":"t4","agent":"claudia"}
{"ts":"2026-02-16T14:50:00Z","type":"task_completed","task_id":"t4","agent":"claudia","summary":"15-strankovy report","output_path":"/output/claudia/report.pdf","tokens":8500}
{"ts":"2026-02-16T14:50:05Z","type":"mission_review","lead":"anna"}
{"ts":"2026-02-16T14:51:00Z","type":"mission_completed","total_tokens":15200,"total_cost":"$0.42","duration_ms":1140000}
```

**Proc JSONL mirror:**
- Agenti ctou progress bez DB pristupu (jen filesystem)
- Lead na zacatku kazde iterace nacte progress.jsonl jako kontext
- Drzime konzistenci s existujicim patternem (JSONL pro zpravy, logy)
- JSONL je append-only → bezpecne pro paralelni zapis

### 4.3 Synchronizace DB ↔ JSONL

```
Agent dokonci task:
  1. crewshipd updatne MissionTask v DB (source of truth)
  2. crewshipd appendne radek do progress.jsonl (mirror)
  3. crewshipd posle WebSocket event do UI (real-time update)

Pokud JSONL a DB diverguji → DB je source of truth, JSONL se regeneruje.
```

---

## 5. MISSION BOARD (UI)

### 5.1 Primarni view: tabulka (spreadsheet)

```
┌──────────────────────────────────────────────────────────────────────────────┐
│  Mission: Mesicni report socialnich siti                                     │
│  Lead: Anna | Status: IN_PROGRESS | Started: 14:32 | Duration: 18m          │
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

1. **Crew page** (`/crews/{crewId}`) — sekce "Active Missions" s posledni aktivni mission
2. **Agent detail** (`/agents/{agentId}`) — pro lead: seznam jeho missions
3. **Dedicated page** (`/crews/{crewId}/missions/{missionId}`) — plny detail s tabulkou
4. **Dashboard** (`/`) — widget "Running Missions" s top-level prehledem

### 5.4 Real-time updaty

```
crewshipd → WebSocket event → Zustand store → React component re-render

Event typy:
  mission.created      → pridani do seznamu
  mission.status       → zmena statusu mission
  task.status          → zmena statusu tasku v tabulce
  task.assigned        → prirazeni agenta k tasku
  task.progress        → prubezny update (tokeny, cas)
  mission.completed    → finalni stav
```

### 5.5 Historie

Vsechny dokoncene missions zustavaji v DB. Uzivatel muze:
- Prohlizet historii missions per crew a per lead
- Filtrovat podle statusu, data, nakladu
- Prokliknout se do detailu (tabulka + activity feed)
- Videt trendy: prumerna doba, prumerne naklady, uspesnost

---

## 6. DEV-TEST LOOP (WORKFLOW SABLONY)

### 6.1 Koncept

Workflow sablona = preddefinovana sekvence kroku ktere se opakuji.
Lead muze pouzit sablonu jako zaklad a overridnout za behu (active mode).

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
  "lead_override": true,
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
  1. Lead vytvori mission s sablonou "dev-test-loop"
  2. Task "implement" → prirazeno developer agentovi
  3. Developer implementuje, dokonci → task COMPLETED
  4. Task "test" se odblokuje → prirazeno tester agentovi
  5. Tester testuje → vysledek: FAIL (3 testy selhaly)
  6. Condition check: status == 'fail' → goto "implement"
  7. Lead dostane notifikaci: "Iterace 1 selhala, 3 failing testy"

Iterace 2:
  8. Novy task "implement" (iteration=2) → developer dostane:
     - Puvodni zadani
     - Vysledky testu z iterace 1 (error messages)
     - Kontext z progress.jsonl
  9. Developer opravuje, dokonci → task COMPLETED
  10. Novy task "test" (iteration=2) → tester testuje
  11. Vysledek: PASS → condition: status != 'fail' → done
  12. Mission → REVIEW → lead zkontroluje → COMPLETED
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

**Git worktree (volitelne, pro dev crews):**
- Kazdy agent dostane vlastni git worktree (branch)
- Developer pracuje na `feature/task-{id}`, tester testuje na stejne branch
- Po uspesnem testu lead merguje do main
- Viz sekce 9 pro detaily

### 6.6 Lead override

V active mode muze lead:
- Zmenit prirazeni agenta za behu ("Bob to nezvlada, dej to Alici")
- Pridat/odebrat tasky z planu
- Preskocit krok (SKIPPED status)
- Zmenit max_iterations
- Ukoncit loop predcasne ("tohle uz je dost dobre")
- Eskalovat na uzivatele ("potrebuji lidsky vstup")

---

## 7. AUTO-HIRING (DYNAMICKE VYTVARI AGENTU)

### 7.1 Koncept

Lead muze za behu zjistit, ze potrebuje specialistu ktereho nema v crew.
Misto cekani na cloveka muze "najmat" — vytvorit noveho agenta nebo priradit
existujiciho z workspace.

### 7.2 Tri urovne autonomie

Konfigurovano per-crew v `Crew.hiring_autonomy`:

| Uroven | Kdo rozhoduje | Co muze lead |
|---|---|---|
| **SUPERVISED** | Clovek vzdy schvaluje | Lead navrhe, UI zobrazi notifikaci, clovek schvali/zamitne |
| **SEMI_AUTO** | Lead pro existujici, clovek pro nove | Lead priradi existujici agenty/skilly. Nove agenty vytvori jen se schvalenim |
| **FULL_AUTO** | Lead sam (budget limit) | Lead vytvari agenty, prirazuje marketplace skilly. Omezeno jen budget limitem |

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
1. Lead posle POST /hire na sidecar
2. Sidecar validuje: RBAC, crew hiring_autonomy, budget
3. Sidecar forwardne do crewshipd
4. crewshipd podle zdroje:
   a) "existing": prohleda agenty ve workspace, match podle popisu
   b) "marketplace": prohleda Skill Hub, najde relevantni skilly
   c) "create_new": vytvori noveho agenta s dynamickym system promptem
   d) "auto": zkusi a) → b) → c) v tomto poradi
5. Podle autonomie:
   a) SUPERVISED: crewshipd vytvori HireRequest, posle WebSocket notifikaci do UI
      → clovek schvali → crewshipd dokonci hiring → notifikace lead agentovi
   b) SEMI_AUTO: existujici = rovnou, nove = notifikace + schvaleni
   c) FULL_AUTO: rovnou vytvori/priradi, zaloguje do audit logu
6. Novy/prirazeny agent se objevi v MissionTask jako assignee
7. Audit log: "agent.hired" s metadata (kdo, proc, kolik to stalo)
```

### 7.5 Docasni agenti

- `Agent.is_temporary = true` — oznaceni docasneho agenta
- `Agent.hired_by_agent_id` — ktery lead ho najal
- `Agent.expires_at` — po expiraci se agent deaktivuje (soft delete)
- V UI: badge "Temporary" / "Hired by {lead}"
- Audit log zaznamenava celý lifecycle (hired → worked → expired)

### 7.6 RBAC a bezpecnost

- Auto-hiring podleha crew-level RBAC (kdo ma pristup ke crew)
- Budget limit per-crew a per-mission (prevence runaway nakladu)
- Docasny agent dedi credentials od crew (stejne jako normalni agent)
- Docasny agent je omezen na danou mission (nemuze pracovat na jinych)
- Marketplace skilly podlehaji verifikacnimu pipeline (ADR-020)

---

## 8. CROSS-CREW SPOLUPRACE PRES COORDINATORA

### 8.1 Hub-and-spoke pattern

```
Marketing Lead ──→ Coordinator ──→ Development Lead
                         │
                         └──→ Finance Lead
```

Vsechna cross-crew komunikace jde pres Coordinatora (bezpecnejsi, auditovatelne).
Primo lead-lead komunikace az Phase 3.

### 8.2 Coordinator konfigurace (styly)

Coordinator ma konfigurovatelny styl (per-workspace nastaveni):

| Styl | LLM model | Co dela | Token naklady |
|---|---|---|---|
| **Passive router** | Haiku/GPT-4o-mini | Jen parsuje a routne na spravnou crew | Nizke (~$0.001/req) |
| **Dual model** | Mini pro routing, Opus pro agregaci | Routne levne, agreguje chytre | Stredni |
| **Full reasoning** | Opus/GPT-4o | Plne reasoning, strategicke rozhodovani | Vysoke |
| **Budget-limited** | Konfigurovatelny | Full reasoning ale s token/cost limitem | Strop |

### 8.3 Coordinator flow pri cross-crew pozadavku

```
1. Marketing Lead: "Potrebuji informace o nakladech na servery"
   → POST /assign s crew_target: "finance"
2. Sidecar forwardne do crewshipd
3. crewshipd spusti Coordinatora (lightweight LLM call, viz ORCHESTRATION.md 5.2)
4. Coordinator (v routing mode): parsuje pozadavek → routne na Finance Lead
5. crewshipd prirazuje Finance Lead agentovi
6. Finance Lead zpracuje (pripadne prirazuje svym agentum)
7. Vysledek → Coordinator → Marketing Lead
8. Marketing Lead vlozi vysledek do sve mission
```

### 8.4 Setreni tokenu

- **Passive router** nepouziva reasoning — jen pattern matching na crew popis
- crewshipd cachuje crew/lead informace — coordinator nedostava plny kontext kazdy request
- Agent output compression (ADR-005) — coordinator dostava sumarizace, ne plne vystupy
- Budget limit per-coordinator zabrani runaway nakladum

---

## 9. GIT WORKTREE PRO DEV CREWS

### 9.1 Koncept

Pro development crews kde agenti pracuji na kodu:
kazdy agent dostane vlastni git worktree (branch) pro paralelni praci
na stejnem repozitari.

### 9.2 Flow

```
1. Mission "Implementuj OAuth2 login":
   Task 1: Backend API endpoints (Alice)
   Task 2: Frontend login form (Charlie)
   Task 3: QA testing (Diana)

2. crewshipd pripravi worktrees:
   git worktree add /workspace/alice/oauth feature/oauth-backend-{task-id}
   git worktree add /workspace/charlie/oauth feature/oauth-frontend-{task-id}

3. Alice a Charlie pracuji paralelne na svych branch

4. Po dokonceni:
   a) Lead (Tomas) review kodu obou branches
   b) Lead merguje do hlavni branch (nebo prirazuje QA)
   c) Diana testuje na mergnutem kodu
   d) Pri failu → loop zpet na developera se specifickou branch

5. Cleanup: crewshipd odstrani worktrees po dokonceni mission
```

### 9.3 Konfigurace

- `Crew.git_worktree_enabled = true` — zapnuto per-crew
- Pouze pro crews kde agenti pracuji s git repozitarem
- Repozitar musi byt naklonovan v `/workspace/` kontejneru
- Lead ma merge prava, agenti maji jen svou branch

### 9.4 Omezeni (MVP)

- 1 repozitar per crew container (MVP)
- Max 5 simultaneous worktrees (prevence disk space)
- Automaticky cleanup po 24h neaktivity
- Merge konflikty eskaluje lead na uzivatele

---

## 10. SIDECAR API ROZSIRENI

### 10.1 Nove endpointy (doplneni k existujicim z ORCHESTRATION.md)

```
MISSION:
  POST /mission/create        — vytvorit mission
  POST /mission/plan          — nastavit/updatovat plan (tasky)
  PATCH /mission/task/:id     — updatovat status/result tasku
  GET  /mission/:id           — precist aktualni stav mission + tasks
  GET  /mission/current       — aktualni mission pro tuto crew
  GET  /mission/history       — seznam dokoncenych missions

AUTO-HIRING:
  POST /hire                    — pozadat o noveho specialistu
  GET  /hire/:id                — stav hire requestu
  DELETE /hire/:id              — zrusit hire request

WORKFLOW:
  GET  /workflows               — seznam dostupnych sablon
  POST /mission/apply-workflow  — aplikovat sablonu na mission
```

### 10.2 Validace

Vsechny nove endpointy podlehaji:
- Session-id overeni (X-Crewship-Session header)
- RBAC check (jen lead/coordinator mohou vytvaret missions)
- Crew membership check
- Budget check (pro hiring a missions)

---

## 11. GO SERVICE — NOVA LOGIKA

### 11.1 MissionEngine (novy modul v crewshipd)

```go
// internal/orchestrator/mission.go

type MissionEngine struct {
    db           *prisma.Client      // Prisma DB pristup (pres IPC do Next.js)
    assigner     *AssignmentEngine   // existujici assignment engine
    ws           *ws.Server          // WebSocket pro real-time updaty
    wal          *state.WAL          // bbolt WAL pro crash recovery
}

type CreateMissionRequest struct {
    WorkspaceID string
    CrewID      string
    LeadID      string
    Title       string
    Description string
    TraceID     string   // "mission-{uuid}"
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
func (e *MissionEngine) ResolveReadyTasks(mission *Mission) []MissionTask {
    completed := map[string]bool{}
    for _, t := range mission.Tasks {
        if t.Status == "COMPLETED" {
            completed[t.ID] = true
        }
    }

    var ready []MissionTask
    for _, t := range mission.Tasks {
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
    engine *MissionEngine
}

// CheckLoopCondition kontroluje workflow sablonu a rozhoduje o dalsi iteraci
func (lc *LoopController) CheckLoopCondition(
    mission *Mission,
    task *MissionTask,
    workflow *WorkflowTemplate,
) LoopDecision {
    if task.Iteration >= mission.MaxIterations() {
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
    basePath string // /output/{lead-slug}/mission-{trace-id}/
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

### 12.1 RBAC pro Mission

| Akce | OWNER | ADMIN | MANAGER | MEMBER | VIEWER |
|---|---|---|---|---|---|
| Zobrazit mission board | Vsechny crews | Vsechny crews | Prirazene crews | Prirazene crews | Prirazene crews (read-only) |
| Vytvorit mission (pres chat) | Ano | Ano | Prirazene crews | Ne | Ne |
| Zrusit mission | Ano | Ano | Prirazene crews | Ne | Ne |
| Schvalit hiring request | Ano | Ano | Prirazene crews | Ne | Ne |
| Zmenit crew hiring_autonomy | Ano | Ano | Ne | Ne | Ne |

### 12.2 Agent-level omezeni

- Jen LEAD a COORDINATOR mohou vytvaret missions
- Agent (default role) nemuze pristupovat k /mission/ endpointum na sidecar
- Docasny agent je omezen na svou mission (nemuze prirazovat dal)
- Budget limit zabraní runaway nakladum

### 12.3 Audit log

Vsechny akce se loguji:

```
mission.created        — kdo/co vytvorilo mission
mission.plan_set       — lead nastavil plan
mission.completed      — finalni stav + metriky
task.assigned          — prirazeni agenta k tasku
task.completed         — dokonceni tasku + vysledek
agent.hired            — dynamicke najeti agenta
agent.hire_approved    — schvaleni hire requestu
agent.hire_rejected    — zamitnut hire requestu
workflow.applied       — pouziti workflow sablony
workflow.overridden    — lead overridnul sablonu
```

---

## 13. FAZOVANI IMPLEMENTACE

### Phase 2A: Zakladni Mission

| ID | Feature | Popis |
|---|---|---|
| MISS-01 | Mission + MissionTask Prisma modely | Nove tabulky + enumy + migrace |
| MISS-02 | Sidecar endpointy pro mission | /mission/create, /plan, /task/:id, /current |
| MISS-03 | MissionEngine v crewshipd | Vytvareni, plan, dependency resolution |
| MISS-04 | JSONL progress writer | Mirror do /output/, append-only |
| MISS-05 | Mission Board UI (tabulka) | Spreadsheet view pod /crews/{id}/missions/{id} |
| MISS-06 | WebSocket real-time updaty | mission.* a task.* eventy |
| MISS-07 | Mission historie | Seznam dokoncenych missions per crew |
| MISS-08 | Dashboard widget | "Running Missions" na hlavni strance |

### Phase 2B: Workflow sablony + Auto-hiring

| ID | Feature | Popis |
|---|---|---|
| MISS-09 | Workflow sablony (JSON format) | Vestavene sablony (dev-test-loop, sequential, parallel) |
| MISS-10 | Loop controller v crewshipd | Condition check, iterace, max_iterations |
| MISS-11 | Dev-test loop integrace | Developer → Tester → zpet pattern |
| MISS-12 | Auto-hiring: SUPERVISED mode | POST /hire, UI notifikace, schvaleni |
| MISS-13 | Auto-hiring: SEMI_AUTO mode | Automaticke prirazeni existujicich |
| MISS-14 | Crew hiring_autonomy nastaveni v UI | Konfigurace per-crew |
| MISS-15 | Docasni agenti (is_temporary) | Lifecycle: hired → working → expired |
| MISS-16 | Coordinator routing mode | Passive router pro cross-crew pozadavky |
| MISS-17 | Inline metriky v Mission Board | Duration, token count, cost per task |

### Phase 3: Pokrocile funkce

| ID | Feature | Popis |
|---|---|---|
| MISS-18 | Auto-hiring: FULL_AUTO mode | Plne autonomni vcetne marketplace skillů |
| MISS-19 | Git worktree integrace | Per-agent branch, lead merge |
| MISS-20 | Cross-crew mission (coordinator level) | Coordinator koordinuje mission pres vice crews |
| MISS-21 | Mission replay/debug | Prehrat celou mission krok po kroku |
| MISS-22 | Primo lead-lead komunikace | Bez coordinatora, s RBAC |
| MISS-23 | Mission analytics (trendy) | Grafy, srovnani efektivity, historicke trendy |
| MISS-24 | Custom workflow sablony | Uzivatel si vytvari vlastni sablony v UI |
| MISS-25 | Coordinator full reasoning + budget limit | Plny coordinator s konfigurovatelnym stropem |

---

## 14. SROVNANI S EXISTUJICIMI KONCEPTY

| Aspekt | Ralph Loop | OpenClaw Sessions | CrewAI Hierarchical | **Crewship Mission** |
|---|---|---|---|---|
| Scope | Single agent, iterativni | Multi-session messaging | Manager → workers | **Lead → agents + loop** |
| Progress tracking | Filesystem (PROGRESS.md) | Session history | Pipeline implicit | **DB + JSONL + UI Board** |
| Autonomie | Plna (agent sam iteruje) | Session spawn (omezene) | Kod-definovane | **3 urovne (configurable)** |
| Workflow | Implicit (same prompt loop) | Manual session mgmt | Python dekorator | **JSON sablony + override** |
| Cross-crew | N/A | N/A | Nested crews | **Coordinator hub-and-spoke** |
| UI | Terminal only | ClawDeck | Terminal/LangSmith | **Tabulkovy Board + dashboard** |
| Git integrace | Git commits per iterace | Ne | Ne | **Git worktree per agent** |
| Hiring | N/A | N/A | Kod-definovane agenty | **Dynamicke auto-hiring** |

---

## 15. OTEVRENE OTAZKY

### Rozhodnute v teto verzi

1. **Storage model:** DB + JSONL mirror (DB source of truth, JSONL pro agenty)
2. **Cross-crew:** Pres coordinatora (hub-and-spoke), primo lead-lead az Phase 3
3. **Hiring autonomie:** Konfigurovatelne per-crew (supervised/semi-auto/full-auto)
4. **UI styl:** Tabulka (spreadsheet) jako primarni view
5. **Workflow override:** Ano — sablony jako zaklad, lead muze overridnout
6. **Git worktree:** Ano, volitelne pro dev crews
7. **Coordinator styly:** Konfigurovatelne (passive router, dual model, full reasoning, budget-limited)

### Stale otevrene

1. **Mission budget:** Jak nastavit budget limit per mission? Per-task nebo celkovy?
2. **Notification system:** Jak notifikovat cloveka pri SUPERVISED hiring? WebSocket + email?
3. **Workflow marketplace:** Budou workflow sablony sdilelne pres marketplace jako skilly?
4. **Mission templates:** Muze uzivatel ulozit celou mission jako sablonu pro opakovani?
5. **Concurrent missions:** Muze lead bezet vice missions naraz? Pravdepodobne ano s limitem.
6. **Mission priority:** Muze uzivatel prioritizovat mission (urgentni vs normalni)?
7. **Human-in-the-loop:** Muze task mit status "AWAITING_HUMAN" kde ceka na lidsky vstup?

---

*Tento dokument doplnuje ORCHESTRATION.md o koncepty Mission, Workflow sablon,
Auto-hiringu a Progress trackingu. Implementace podle fazovani v sekci 13.*
