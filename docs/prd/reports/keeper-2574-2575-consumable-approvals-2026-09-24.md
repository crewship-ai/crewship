# Keeper #2574 a #2575 — konzumovatelná schválení a směrování sampled verdiktů (2026-09-24)

## Shrnutí

Oba security issues opraveny v kódu, ne jen v dokumentaci. Větev `fix/keeper-2574-2575`
(clone crewship_3), PR odkazuje na oba issues. Žádný issue nezavírám bez end-to-end
důkazu nad reálným nasazením — testy níže jsou fixturní (reálný gatekeeper, reálná
governance řada, reálný sampler), nikoli jen interní admin endpoint.

## #2574 — schválená L4 eskalace byla no-op

### Reprodukce (před opravou)

Test `TestL4Escalation_ApprovedButRetriedWithoutApproval_ReEscalates`
(internal/api/keeper_approval_test.go) píne původní chování: judge ALLOW na L4
→ `ApplyTierPolicyFloor` → ESCALATE; člověk resolve ALLOW; agentův retry BEZ
prezentace approval je posouzen znovu a znovu eskaluje. To byl dead-end popsaný
v issue — schválení nikam nevedlo.

Oprava podle první varianty z issue: **schválení je konzumovatelné**.

### Design a bezpečnostní analýza

Agent prezentuje id schváleného requestu na retry (`approval_request_id` v body,
nebo hlavička `X-Keeper-Approval`, kterou sidecar překládá). Server ho buď
spotřebuje (ALLOW bez nového posouzení — příkaz proběhne / credential se
dodá), nebo odmítne s kódem, podle kterého agent pozná, co dál.

Vazby v `consumeKeeperApproval` (internal/api/keeper_approval.go):

- **Provenience:** konzumovatelný je jen řádek vyřešený ČLOVĚKEM
  (`resolved_by_user_id IS NOT NULL` — sloupec plní pouze HandleResolve).
  Judge-ALLOW na L3 (nebo L4 s `escalate-from=never`) tak není mincovna
  schválení. Bez toho by každý minulý ALLOW na tabulce byl použitelný jako
  approval.
- **Binding:** stejný agent + credential + request_type + (pro execute)
  byte-identický příkaz. Mismatch → 403 s instrukcí podat žádost bez approval.
- **Jednorázovost:** spotřeba je podmíněný `UPDATE … WHERE approval_consumed_at
  IS NULL AND decision='ALLOW' AND resolved_by_user_id IS NOT NULL`; souběh
  řeší RowsAffected — přesně jeden vyhrává (test 8 souběžných presenterů,
  `TestL4Escalation_ConcurrentPresentations_ConsumeExactlyOnce`).
- **Žádné řetězení:** řádek, který konzumovaný approval vytvoří, se rodí
  „spotřebovaný" (`approval_consumed_at` nastaveno při INSERTu) a bez
  `resolved_by_user_id` — není sám utratitelný
  (`TestL4Escalation_JudgeAllowIsNotAnApproval`).
- **TTL 15 minut** od `decided_at` (zápis z téže resolve transakce); poté 410
  Gone.
- **Spotřeba před exekucí:** když exek později selže (kontejner dolé),
  approval je propálen — refund by z nestabilního kontejneru udělal retry
  oracle.
- **Workspace scoping** přes credential (jako HandleResolve); cizí tenant → 404
  bez existence-oracle.
- **Restart:** stav je v SQLite, ne v paměti
  (`TestL4Escalation_ConsumptionSurvivesAHandlerRebuild` — nový handler nad
  stejnou DB = model restartu).
- **Dedup (#1329):** approval-presenting retry má vlastní dedup klíč — původní
  eskalace nic nespustila, takže debounce okno (5 s) potlačující transportní
  retry nesmí potlačit jedinou záměrnou re-submisi, pro kterou schválení
  existuje. Mezi approved retry samotnými klíč dál koliduje a jednorázovost
  spotřeby je tvrdší záruka za ním.

Druhá polovina issue — agent se musí dozvědět o výsledku:

- **Sidecar `GET /keeper/request/{id}`** (nová routa, `handleKeeperRequestStatus`)
  forwarduje na interní `GET /api/v1/internal/keeper/request/{id}?agent_id=…`
  s acting agentem z bearer tokenu (#812): sourozenec v kontejneru nedostane
  stav cizího requestu (404). Odpověď nyní nese `request_type` a
  `approval_consumed_at`, takže poll loop pozná, že ALLOW je ještě utratitelné.
- **Audit:** retry řádek má v ledger actor `user` + id schvalovatele (ne keeper),
  journal `keeper.decision` nese `approval_request_id` + `approved_by_user_id`,
  resolve zapisuje `resolved_by_user_id` i na projekci (dříve jen v ledgeru).

### Akceptační důkaz

`TestL4Escalation_ApprovalIsConsumedAndTheCommandRuns`: reálný gatekeeper
(canned llm.Provider vracející ALLOW) → L4 floor → ESCALATE, exek NEVOLÁN;
resolve ALLOW; retry s approval → **ALLOW, příkaz proběhl právě jednou, judge
nezavolán**; druhá prezentace → 409, exek neproběhl. Dále: DENY → 403 bez
exekuce, unresolved → 403 „awaiting a human decision", expired → 410,
binding mismatchy (agent/credential/command/access↔execute) → 403.

## #2575 — sampled watchdog zahazoval WARN a ESCALATE

### Zjištění (potvrzeno z kódu na main)

- `behaviorhook.MaybeEvaluateEvery` vracel jen `(*BlockedError, fired)` —
  verdikt s `PolicyDecision` se ke volajícímu nedostal.
- Adapter (internal/server/post_tool_call_adapter.go) reagoval jen na block
  (DENY v block módu) → journal `hook.blocked`. WARN/ESCALATE: nic.
- Inbox fan-out v `HandleBehavior` existoval, ale sampled cesta ho nikdy
  nenavolala — potvrzeno: jediný produktní volající hooku je observer.

### Oprava

- Hook vrací `*Sample{Verdict, Blocked}` — plný verdikt včetně předpočítaného
  `PolicyDecision` (matice mode × autonomy z behavior_evaluator.go).
- Observer routuje každý vydaný sample přes **reálnou persistenci endpointu**:
  `KeeperPhase2Handler.RecordSampledBehavior` (sdílená s HandleBehavior přes
  `persistBehaviorVerdict` — stejná keeper_requests řada, stejný inbox item,
  stejný realtime push; rozdílné jen zpracování chyb: endpoint 500, async log).
- Journal: každý fired sample → `keeper.decision` (DENY/ESCALATE warn,
  ostatní notice); block navíc `hook.blocked` jako dřív.
- Výsledek podle governance konfigurace: WARN → non-blocking inbox (guided),
  ESCALATE → inbox (blocking v block × strict/guided), DENY block → blocking
  inbox + hook.blocked, ALLOW → jen journal, full autonomy → jen journal.

### Důkaz na skutečné sampled cestě

`internal/server/post_tool_call_fanout_test.go`: `postToolCallObserver.Observe`
nad reálnou governance řadou (enabled, sample=1), reálným behaviorhook
singletonem, reálným evaluátorem — NE interní admin endpoint. Varianty:
sampled ESCALATE → inbox item (pin přímo z issue), WARN → non-blocking inbox,
DENY block/strict → blocking inbox + hook.blocked, ESCALATE block/strict →
blocking, ALLOW → journal only, full autonomy → journal only.

### Nesoulad L4 MinRisk (též z issue)

`internal/keeper/tier.go`: L4 `MinRisk` 6 → **7** (= `governance
.DefaultDenyNotifyMinRisk`). Dříve L4 DENY floored na 6 nedosáhl notify prahu
7 na nekonfigurovaném workspace — komentář tvrdil opak. Pin test
`TestL4MinRiskMeetsDefaultDenyNotifyThreshold`. Změna je only-tightening
(podlaha smí risk jen zvednout).

Ostatní drobnosti z issue: komentář „≥3 distinct" → 5 (`l1MinDistinctChars`),
help text `cmd_credential.go` L4 nyní zmiňuje `escalate-from`, `keeper/doc.go`
ESCALATE popisuje konzumovatelné schválení.

## Dokumentace

`docs/guides/keeper.mdx`: Warning „To je vše, co ruling dělá" nahrazen popisem
konzumovatelného schválení (bindingy, single-use, TTL, spotřeba před exekucí);
Watchdog Governance Warning nyní popisuje persistenci všech fired samples.
`autonomy-and-self-learning.mdx` + `keeper-reviews-panel.mdx`: sampled path =
stejná persistence jako endpoint.

## Migrace

`20260924132900_keeper_approval_consume.sql`: `keeper_requests.resolved_by_user_id`,
`keeper_requests.approval_consumed_at`. Čisté `ALTER TABLE ADD COLUMN`,
bez rebuildů; spotřeba je podle PK, bez nového indexu.

## Ověření

- `go vet ./...` — čisté.
- Cílené sady: `internal/api`, `internal/sidecar`, `internal/server`,
  `internal/keeper/...`, migrace v `internal/database` — zelené.
- `go test ./... -count=1` — probíhá/dokončeno v PR CI; na tomto boxu
  (3 paralelní dev instance) trvá plná sada >20 min jen pro `internal/api`,
  timeouty balíků byly environmentální (potvrzeno: `TestCheckpointModeShrinksWALFile`
  trvá 244 s i na čistém main).
- `go run ./scripts/lint-migrations` — ok po commitu migrace.
- `go run ./scripts/agents-invariants` — 4 checkable pravidla drží.

## Otevřené body

- Judge prompt a `--escalate-from` (`gatekeeper.go` ~L857 v issue: prompt používá
  `tierPolicyBlock(req.SecurityLevel)` místo resolved tier) — v issue uvedeno
  jako „Related, same audit", nebylo součástí tohoto PR; nechávám jako
  samostatný follow-up.
- Live ověření přes dev3 UI (klikací approve → agent poll → retry) až PR
  pojede na stage; fixturní testy pokrývají kontrakt.
