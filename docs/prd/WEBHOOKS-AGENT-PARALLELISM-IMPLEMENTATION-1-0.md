# Crewship 1.0: implementační a akceptační kontrakt

Stav: návrh určený k implementaci, nikoli výsledky měření. Datum: 2026-09-10. Tento dokument je normativní rozpracování [rozhodovacího PRD](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md); čte se společně s ním. [Příloha](WEBHOOKS-AGENT-PARALLELISM-ANNEX.md) obsahuje audit a výzkum. Číselné hodnoty níže jsou výchozí návrhové limity a release cíle. Naměřené baseline zatím nemáme.

## 1. Výsledek pro uživatele

Jamie zpracovává rozhovor a zároveň jeden background úkol. GitHub pošle událost k PR: Crewship ověří podpis, trvale uloží přijetí a práci, vrátí receipt a spustí background běh, jakmile je kapacita. Chat běží dál. Druhá background událost čeká; další zpráva stejného chatu čeká na dokončení jeho aktuálního tahu. Uživatel vidí oba běhy odděleně a může zrušit jeden z nich.

Oba běhy mohou přidávat do společné, oprávněním vymezené paměti. Jestliže oba přepisují stejnou revizi, uspěje jeden; druhý dostane konflikt, načte nový podklad a připraví nový návrh. Nikdo tiše neztratí potvrzený zápis přes podporované memory API. Pád serveru neztratí potvrzenou práci; nejasný výsledek externího volání vyžaduje ověření místo slepého opakování.

**Release profil:** pouze ověřený Claude adaptér, jeden chat + jeden background na agenta, jeden aktivní tah na session, jeden server. Ostatní adaptéry mají celkový limit jednoho běhu. E0 odděluje runtime a cesty, neposkytuje bezpečnostní izolaci procesů stejného UID. Přímé shell zápisy do společné paměti zůstávají mimo garanci CAS; UI toto omezení zobrazí při zapnutí profilu.

## 2. Technologie a hranice odpovědnosti

| Varianta | Rozhodnutí pro tento release |
|---|---|
| Stávající Go + SQLite | Výchozí cesta: rozšířit dosavadní fronty o jednotný durable dispatch kontrakt. Bez další služby a bez druhého nezávislého scheduleru. |
| River / riversqlite | Podmíněná náhrada implementace fronty, pokud projde [spikem](SPIKE-RIVER-SQLITE-1-0.md) před zahájením queue implementace. Neúspěch či neuzavřený výsledek znamená výchozí cestu. |
| goqite | Menší transakční queue primitive; nepřináší sama naše admission, session, runtime ani memory pravidla. Není další paralelní implementační větev 1.0. |
| Redis | Nepřidávat: další provozní služba a hranice mezi uložením domény v SQLite a enqueue. SQLite outbox by stejně zůstal potřeba. |
| Postgres / Temporal | Nový deployment model; mimo 1.0. Znovu hodnotit při požadavku multi-host nebo naměřeném limitu jednoho writeru. |
| React / Next.js | Klient stavů a ovládání; scheduling, podpisy, credentials a durability jsou v Go serveru. Žádné API routes v Next.js static exportu. |

River dokumentuje transakční enqueue; SQLite driver uvádí omezené provozní ověření a doporučuje pool s jedním připojením. Některé pokročilé funkce jsou River Pro. Proto použití Riveru není samo o sobě důkaz garancí ani důvod pro novou produkční závislost. Ověřit přesnou verzi, licenci a potřebné funkce. Zdroje: [River](https://riverqueue.com/), [SQLite driver](https://raw.githubusercontent.com/riverqueue/river/master/riverdriver/riversqlite/river_sqlite_driver.go), [goqite a transakční metody](https://github.com/maragudk/goqite).

## 3. Identita, data a nepřekročitelné invarianty

Navržená jména jsou implementační kontrakt, nikoli popis již existujících tabulek. Lze využít existující tabulky, pokud výsledné schéma zachová stejné invarianty.

| Entita | Identita a povinná informace |
|---|---|
| Delivery | workspace + endpoint + source delivery ID jsou unikátní; hash raw body, profil, čas přijetí, rozhodnutí filtru, target revision, work ID nebo důvod ignorování. |
| Work item | Stabilní work ID; zdroj a doménový odkaz, agent/session/scope, autor oprávnění, immutable vstup a jeho verze, stav, priority, eligible_at, deadline, replay_of. |
| Attempt | Nový run ID pro každý pokus; work ID, generation, vlastník lease, heartbeat/expiry, runtime locator, důvod začátku/konce, náklady a exit evidence. |
| Event | Work ID + monotónní sequence; změna stavu a čas. Autoritativní stav a událost se potvrzují v jedné transakci. |
| External operation | Stabilní operation ID napříč retry; work ID, typ/cíl, request hash, idempotency key, stav a provider receipt. Tajné hodnoty nejsou součást veřejného auditu. |
| Memory mutation | Scope + klíč + operation ID; request hash, původní/cílová revize a hash, durable intent, obsah nebo durable odkaz potřebný pro recovery, provenance a výsledek. |

**I1:** žádné `202` před durable commitem delivery a práce. **I2:** jedna delivery vytváří nejvýše jednu logickou práci; attempts nejsou další práce. **I3:** jeden aktivní tah session; claim a rezervace všech limitů atomické. **I4:** podporované změny stavu, paměti a sidecar operace ověřují aktuální run/generation. **I5:** terminal výsledek starého pokusu nepřepíše novější pokus. **I6:** zápisy potvrzené memory API se neztratí při konkurenci ani po recovery. **I7:** žádný producent nesmí obcházet společné admission. **I8:** práva a scope se kontrolují při přijetí i dispatchi, nikoli pouze v prohlížeči.

Queue má jediného vlastníka claim/retry/recovery. Doménové assignments a pending_runs mohou zůstat, ale staré dispatch pumpy nesmějí nezávisle spouštět stejnou práci. Případný společný `work_items` ledger přebírá toto vlastnictví, nestává se třetí soupeřící frontou. Doménový zápis a navázání na práci jsou v jedné `*sql.Tx`, jinak přes explicitní outbox.

## 4. Stavový automat a obnova

Základní cesta: `queued → starting → running → succeeded`. Claim založí attempt a rezervuje kapacitu před přípravou runtime; Docker/CLI ani HTTP volání nejsou uvnitř DB transakce.

- `retry_wait`: bezpečně opakovatelná chyba, další attempt po eligible_at; maximálně 5 attempts celkem, exponential backoff s full jitter do 300 s (základ 2 s). Čas i RNG jsou v testech ovladatelné.
- `waiting`: durable waitpoint nebo čekání na dítě; uvolní execution slot až po potvrzeném zaparkování/ukončení vykonávajícího procesu. Session zůstává logicky obsazená, další tah ji nepředběhne.
- `failed`: známé konečné selhání; `expired`: vypršela explicitní deadline před spuštěním; `cancelled`: potvrzené ukončení. Bez deadline práce automaticky neexpiruje.
- `needs_reconciliation`: nejasný externí efekt nebo živý runtime, jehož vlastnictví nelze bezpečně obnovit. Není to úspěch ani automatický retry.

Terminální historie se nepřepisuje. Ruční replay terminální práce vytváří nové work ID s `replay_of`, důvodem, aktuální autorizací a původním vstupem; použití jiné target revision musí být explicitní. Automatický retry drží work ID a mění run ID. Neopakovat celý agentní tah jen proto, že queue umí retry.

Heartbeat 10 s, lease 60 s, recovery scan nejvýše po 5 s. Expirace lease sama neopravňuje spustit druhý proces: nejprve ověřit locator, zastavit nebo bezpečně převzít původní runtime a revokovat starou capability. Není-li výsledek jistý, reconciliace blokuje jeho konfliktní kapacitu. Fencing DB/API nezastaví svévolný shell ve společném containeru.

Cancel je idempotentní požadavek, nikoli okamžitá terminal hodnota. Queued práci lze ukončit atomicky; běžící dostane signál, po 10 s grace následuje eskalace na konkrétní procesní skupinu runu. `cancelled` až po potvrzení zastavení; nezastavitelný nebo nejasný proces vyžaduje reconciliaci. Pokud dokončení již vyhrálo transakční závod, API vrátí skutečný dokončený stav. Zrušení nevrací již provedené externí změny.

External operation ukládá intent před voláním, následně receipt. Pád mezi voláním a receipt znamená unknown: cílovou službu dotázat podle idempotency key/provider ID; bez takové možnosti vyžádat rozhodnutí uživatele. Tento release negarantuje právě jeden libovolný externí efekt. Podporované write tools musí tento protokol používat; přímé shell/network operace mimo něj musí být v capability profilu výslovně označené.

## 5. Webhook kontrakt

### Profily a přijetí

`github`: raw-body HMAC-SHA256, explicitně povolené event/action a repo/installation podle konfigurace. Minimální release fixture: `ping` a `pull_request` opened/reopened/synchronize. Další eventy vyžadují vlastní mapping test; žádný implicitní wildcard, dynamické spouštění scriptu ani LLM v acceptance cestě.

`standard-webhooks`: `webhook-id`, `webhook-timestamp`, `webhook-signature`; HMAC v1 nad ID.timestamp.raw-body, tolerance timestampu ±5 minut. Použít připnutou reference knihovnu a oficiální test vectors, ne vlastní kryptografii. Payload je JSON objekt; statický endpoint mapping z něj vytvoří vstup práce, standard nevnucuje náš vlastní envelope. [Specifikace](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md), [Go verifier](https://github.com/standard-webhooks/standard-webhooks/blob/main/libraries/go/webhook.go).

Ověřit podpis a aktuální policy i při duplicate requestu. Chyba lookupu není vypnutá policy. Rotace: nejvýše dva aktivní ověřovací klíče, old/new s explicitním časem ukončení překryvu; žádné secrets v logu či receipt. Rotace nesmí obejít timestamp kontrolu. Legacy endpointy nezmění své profily heuristicky podle přítomnosti hlavičky.

GitHub podpis nekryje delivery ID a nemá tento signed timestamp. Pro stejný endpoint a přesně stejný ověřený raw body použít po dobu retence také obsahový replay klíč; jiné delivery ID nevytvoří další práci. Je to záměrné sloučení byte-identických payloadů tohoto profilu, nikoli tvrzení o univerzální identitě GitHub eventu. Upravené unsigned event hlavičky nesmějí z totožného body vytvořit jinou akci. Znovuprovedení je autorizovaný replay. Odlišné payloady pro stejný PR nejsou automaticky duplicate; nepředpokládat pořadí GitHub deliveries. Práce nad repozitářem před publikací výsledku ověří cílový commit/revizi.

### Odpovědi

| Situace | HTTP a výsledek |
|---|---|
| Nová přijatá delivery | `202 {delivery_id, work_id, status: "queued", duplicate: false}` po commitu. |
| Shodná duplicate v retenčním okně | `202`, původní ID a aktuální stav, `duplicate: true`; žádný nový attempt jen kvůli doručení. |
| Validní ping nebo událost odfiltrovaná konfigurací | `200`, `status: "ignored"` a bezpečný reason; žádný agent. Rozhodnutí filtru auditovat. |
| Malformed body/required header, podpis, velikost | `400`, `401`, `413`; žádná práce. Neprozrazovat očekávaný podpis. |
| Stejné source ID s odlišným body | `409 delivery_conflict`; původní záznam zůstane. |
| Neznámý nebo vypnutý endpoint | `404`; již accepted práce se řídí dispatch autorizací, ne smazáním historie. |
| Plná ingress kapacita | `429` a `Retry-After: 5`; nic nového nepřevzato. Duplicate již přijaté práce není odmítnuta kvůli zaplnění. |
| Nedostupná DB/policy, překročený acceptance budget | `503`; nikdy falešné `202`. Při nejasném commitu může být práce uložená, opakování stejného ID ji dohledá. |

Receipt není bearer oprávnění ke čtení. Raw webhook odpověď neobsahuje text konverzace, credentials ani LLM výstup. Odpověď vzniká nejpozději 2 s po úplném načtení těla za testovaných podmínek; výchozí read-header timeout 5 s a body deadline 10 s brání neomezenému pomalému uploadu; body limit platí i bez Content-Length. Ověřit přerušení SQLite busy wait, ne předpokládat, že context timeout přeruší 30s driver wait.

## 6. Kapacita a pořadí

Výchozí profil referenčního deploymentu: 8 aktivních executions na server, z toho background nejvýše 6, 2 místa rezervovaná pro chat. Workspace i crew mohou využít server cap, explicitní nižší limit má přednost. Agent Claude má chat ≤1, background ≤1, celkem ≤2; ostatní celkem ≤1. Provider start admission a resource gate zůstávají dalšími podmínkami. Konfigurace nevhodná pro 1+1 nesmí UI deklarovat podporu tohoto profilu.

V každé třídě round-robin mezi workspace a agenty, uvnitř FIFO podle eligible_at a přijetí; aging po 60 s posune dlouho čekající eligible práci před mladší v téže třídě. Chat rezervace se nepůjčuje backgroundu v 1.0. Požadavek na kapacitu není záruka času dokončení při dlouhé práci, nedostupném provideru či vyčerpaném rozpočtu. UI ukazuje konkrétní queue reason.

Max body 1 MiB; výchozí ingress limity 2 000 neterminálních prací na endpoint a 10 000 na workspace, společný limit 256 MiB raw vstupů neterminálních prací na workspace. Kapacita se kontroluje atomicky, včetně chatu a plánovaných zdrojů. Accepted práce se při tlaku neeviktuje. Endpoint rate limit je doplňkový; vyšší limity musí být explicitně nakonfigurované. Chat při nepřijetí zachová rozepsanou zprávu a zobrazí chybu, nesmí tvrdit, že je uložena.

Dedup klíče a receipts nejméně 30 dní od přijetí a po celou neterminální životnost. Raw body nejméně do terminal stavu, poté výchozí retence 7 dní; ruční replay po expiraci payloadu je nedostupný a UI to vysvětlí. Po expiraci dedup okna není slíbené potlačení starého replaye. Run secrets odstranit po potvrzeném konci, diagnostiku ponechat bez credentials. Terminální payloady se do ingress byte limitu nepočítají; jejich diskovou spotřebu je nutné započítat do kapacitního reportu (36 000 × 64 KiB ≈ 2,2 GiB bez indexů a WAL). Při nedostatku disku odmítat nové přijetí; neterminální payload se nesmí odstranit. Retence paměti a její historie se řídí samostatným scope/export/delete kontraktem, není automaticky 7 dní.

## 7. E0, session a všechny vstupní cesty

Run locator obsahuje crew/container a run ID. Každý attempt má vlastní tmux session, args/env/FIFO/exit, HOME, config, auth, workdir, output a stream. Opětovné použití agent slug jako mutable runtime klíče je chyba. Read-only podklady se mohou sdílet; session resume data publikuje jediný aktivní tah session. Nechat sekret v persistentním HOME kvůli resume není přípustné.

Povinně převést: chatbridge, direct agent start/query, assignments a jejich pumpy, scheduler/routines, pipeline kroky a webhooky. Pro každou cestu implementační report uvede konkrétní entrypoint a test dokazující společné admission. Pipeline coordinator čekající na dítě nedrží execution slot. Existující unikátní aktivní assignment per session se neobchází druhým assignmentem: další zprávy patří do durable mailboxu a při uvolnění se teprve předají dalšímu tahu.

Mailbox má unikátní client_message_id scoped na workspace/uživatele/session. Retry odeslání vrátí stejnou zprávu, pořadí určuje server sequence v transakci. Další zpráva v 1.0 nepřerušuje tah a nevstupuje do něj jako steering. Nová session stejného Jamieho také nedostane druhý chat slot; čeká. Background používá samostatnou session, nepřimíchává soukromý transcript chatu.

Claude parallel capability zapnout až po testu start A → start B → cancel/cleanup B → pokračování A. Zahrnout sidecar policy, memory scope a nákladovou/credential attribution, ne jen tmux. Codex/Gemini zůstanou sériové, dokud projdou stejnou sadou včetně souběžného write/remove login souboru v odlišných HOME. Zakázané UID změny se touto specifikací nepovolují.

## 8. Přesný memory kontrakt

Read vrací `content`, `revision`, `content_sha256`, scope a provenance. Revision je monotónní per scope/key; hash se kontroluje vůči skutečnému souboru. UTF-8 vstup normalizovat CRLF na LF před verzováním, zachovat koncový newline; odmítnout nevalidní UTF-8. Existující soubory projdou explicitním importem s novou revizí, nikoli tichou normalizací při čtení.

Write vždy obsahuje operation_id, scope/key a typ. Append přidává obsah pod stejným mutation zámkem jako kontrola capu a aktuálního souboru. Replace obsahuje expected_revision, celý new_content a removals. Removals jsou seznam `{start_line, line_count, old_sha256}` v původním normalizovaném podkladu, číslování od 1, neprotínající se intervaly. Hash je SHA256 přes přesné bajty odstraněných řádků včetně jejich LF; poslední řádek LF nemít může.

Diff je deterministický line-based shortest edit script (Myers; při shodě preferovat delete před insert, sloučit sousední deletions). Referenční implementace a golden fixtures jsou součást změny, server ji používá pro kontrolu removals. Opakované řádky určuje jejich pozice v původní revizi. Přepsaný řádek je delete + insert. I append přes replace musí nést expected_revision a prázdné removals; bez CAS použít append operaci.

Pod zámkem: ověřit autorizaci → idempotency request hash → revizi a drift → skutečný diff/removals → chráněné záznamy a cap → durable intent. Po fsync nového dočasného souboru na stejném filesystemu provést atomic rename a fsync nadřazeného adresáře, potom potvrdit revizi a mutation v DB. Nedržet otevřenou SQLite write transaction během filesystem I/O; jedinečný pending intent a mutation koordinátor blokují další zápis klíče. Zámek je pro celý podporovaný write kontrakt, ne jen pro jeden tool handler.

Recovery před novými zápisy: soubor má cílový hash → dokončit DB potvrzení; původní hash → dokončit intent; jiný hash → drift/memory_conflict, ne automatický overwrite. Nepotvrzený intent musí obsahovat durable data pro dokončení. Index rebuild je asynchronní; read-after-write čte potvrzený kanonický obsah i při zpoždění indexu.

Chyby: `memory_conflict` pro stale revision/drift, `undeclared_removal` pro neshodu diffu, `operation_conflict` pro reuse ID s jiným requestem, permission/cap chyby podle API konvencí. Identický retry vrátí původní výsledek. Agent po konfliktu nejvýše dvakrát znovu načte a navrhne změnu s novým operation ID, pak konflikt zviditelní; žádný nekonečný retry ani automatická obnova smazaného textu.

Metadata přidat k explicitně evidovaným záznamům/mutacím: source, actor/run, scope, recorded_at, nullable valid_from/valid_to a odkazy opravy/odvolání. Neodvozovat atomická tvrzení ze všech Markdown řádků ani nevymýšlet validitu. Historie zachová předchozí verzi a její metadata. Chráněný záznam lze odstranit jen explicitně autorizovanou operací, samotná removals deklarace oprávnění nedává.

## 9. API a React UI

Nové Go API poskytne workspace-scoped work list/detail, delivery list/detail, attempts/events, cancel a replay. Konkrétní routes navržené pod `/api/v1/workspaces/{workspaceID}/work-items` a `/webhook-deliveries`; přijetí zůstává na existujícím webhook routingu s explicitním profilem. Specifikovat v OpenAPI dle místní error envelope, ne měnit souběžně celý error standard. API kontroluje read/manage oprávnění i vlastnictví session; webhook token není oprávnění k těmto endpointům.

UI ukazuje source, Jamie/session, queued reason, eligible čas, attempt, stav, náklady a výsledek. Dva run streams se nikdy nesloučí jen podle agent slug/ChatID. WebSocket nese work/run ID a sequence; po reconnectu klient načte autoritativní snapshot a chybějící události, toleruje duplicitní/out-of-order push. Push není jediná evidence dokončení.

Chat rozlišuje lokálně odesílanou a serverem přijatou zprávu. Client message ID přežije retry a reconnect; lokální chyba neztratí draft. Cancel míří na run/work, replay ukáže původní vstup, target revision, důvod a vznik nové práce. Potvrzení uživatele potřebuje pouze nejasný externí efekt nebo výslovný replay, ne běžné přijetí webhooku.

Použít existující fetch/auth/permission infrastrukturu a React Query dle repo konvencí; cache oddělit workspace/session/work identitou. Dotčené obrazovky fungují na telefonu a desktopu, touch target podle pointeru. Secrets zobrazit jen při vytvoření podle stávající credential policy; seznam/logy je nevrací.

## 10. Měření: baseline a release cíle

Před změnou i po ní stejný harness, izolovaná DB a declared hardware. Referenční profil: Linux, 8 vCPU, 16 GiB RAM, lokální SSD, jeden Go server, lokální Docker; zaznamenat filesystem, kernel, SQLite/Go/driver verze, DSN, checkpoint, pool, image a adapter verzi. Mock executor (100 ms/run) oddělit od reálného CLI. LLM latence a kvalita nejsou benchmark fronty.

Baseline report vyplní acceptance latency, queue age, start latency, SQLITE_BUSY, DB/WAL růst, fsync režii NORMAL/FULL, Go RSS/CPU, počet goroutines/FD, run přípravu/cleanup, memory conflicts a CLI RSS na jeden a dva běhy. Neexistující funkcionalitu označit N/A, nikoli nulou. NORMAL je pouze testovací srovnání, release durability zůstává FULL.

| Metrika / scénář | Cíl a definice |
|---|---|
| Acceptance | Od úplného body po HTTP odpověď: p95 ≤500 ms, p99 ≤2 s při 10 req/s, body 64 KiB, 60 minut, nejméně 10 agentů s 100ms mocky, bez uměle drženého DB locku. |
| Ztráty a duplicate | 0 ztracených acknowledged prací; 0 dalších work items ze shodné delivery v dedup okně. Bilance accepted = queued/active/waiting/retry/reconciliation/terminal. |
| Dispatch režie | Eligible práce s volnými všemi sloty: p95 do claimu ≤2 s. Zvlášť reportovat celkový queue age a čas přípravy CLI; žádné skrytí čekání za tuto metriku. |
| Kapacitní invariant | Žádné překročení slotů při 50 souběžných producentech; chat rezervace dostupná při 6 běžících background mockech. |
| Burst | 1 000 unikátních requests, concurrency 50: každý accepted dohledatelný; případné 429/503 správné a samostatně spočítané. Další 100 souběžných kopií jedné delivery vytvoří jednu práci. |
| Recovery | Po ready serveru do 75 s obnovit bezpečně obnovitelný queued/abandoned mock work nebo viditelně zařadit do reconciliace. Nečekat tuto dobu na každou obyčejnou queued práci. |
| Cancel | API acknowledgement p95 ≤500 ms bez contention; mock proces zastaven do 15 s, soused pokračuje. Provider nedostupnost se nesmí vykázat jako cancelled. |
| Memory | 100 závodů dvou replace stejné revize: vždy jeden úspěch a jeden konflikt; 100 retry identického append: jeden přírůstek. |
| Soak | 60 minut zatížení a následné vyprázdnění: žádné orphan procesy, leaked slots ani růst FD/goroutines v dalších třech shodných cyklech. RSS/WAL trend zaznamenat, nevydávat libovolné číslo za baseline. |

Metriky exportovat bez raw payloadu, credentials a high-cardinality run IDs v labels. IDs patří do strukturovaného auditu. Počítadla: accept/reject/duplicate, queue length/oldest age podle třídy, attempts/retries, lease loss, reconciliation, memory conflict, WAL/checkpoint a cleanup failure. Samostatně sledovat náklady obou běhů proti sekvenční baseline; paralelismus nemá automaticky slibovat levnější LLM.

## 11. Povinné testy a důkaz vydání

| ID | Scénář | Povinná kontrola |
|---|---|---|
| T01 | GitHub a Standard Webhooks valid/invalid, key rotation, timestamp ±hranice, raw-body změna, oversized/chunked body | Správné HTTP, žádný worker při odmítnutí; ping bez LLM. |
| T02 | Duplicate pod concurrency, changed body stejného ID, GitHub changed unsigned ID/header | Stabilní receipt, jedna práce, konflikt či replay suppression dle profilu. |
| T03 | Pád před commitem, po commitu před response, rollback enqueue | Žádný orphan; resend vrátí správné ID i při ztracené odpovědi. |
| T04 | Write lock jiným connection/pool, checkpoint, FULL všechny connections | Budget/503 bez falešného ack; žádné překvapivé 30s čekání; žádný nested pool deadlock. |
| T05 | Všichni producenti zároveň, session mailbox, parent/child, budget a permissions revoked | Limity atomické; pořadí a zprávy zachované; žádný bypass či slot deadlock. |
| T06 | Skutečný Claude: chat + background Jamie současně, třetí práce queued | Prokázaný překryv dvou aktivních runtime, samostatné odpovědi/output/HOME a náklady; sériový průchod FAIL. |
| T07 | Start/cleanup/cancel B během A; auth write/remove; attach/logs | A pokračuje, credentials/policy/memory attribution A beze změny. Ostatní adaptéry zůstávají sériové do PASS. |
| T08 | Lost heartbeat, zombie runtime, late completion, server restart | Žádný nekontrolovaný druhý runtime, generation fencing, viditelná reconciliace. |
| T09 | Externí úspěch a pád před receipt, s provider idempotency i bez ní | Žádný slepý retry; reuse stabilního key nebo explicitní unknown. |
| T10 | CAS, append retry, duplicate operation s jiným obsahem, cap závod, repeated lines/CRLF/newline/Unicode | Přesné chybové kódy, žádný lost update či automatická resurrection. |
| T11 | Memory crash po intentu, fsync, rename a před/po confirm; ruční drift | Recovery hash protokol, žádné přepsání třetího obsahu; index lze obnovit. |
| T12 | UI reconnect/out-of-order events, dva streams, cancel závod, cross-workspace access | Správný snapshot a scope, žádný únik obsahu ani falešný terminal stav. |
| T13 | Retence, endpoint disable/delete, plná ingress kapacita, disk full | Accepted práce zůstane evidovaná; žádné tiché zahazování, přístup dle práv a replay dle dostupnosti payloadu. |
| T14 | Crash OS/VM nebo storage fault harness | Oddělený důkaz od kill -9 procesu; zaznamenat fsync/storage předpoklady a nepodporované konfigurace. |

Unit testy pro state machine, diff, normalizaci, podpisy a policy; integration nad skutečnou modernc SQLite a dočasným filesystemem; process crash harness; Docker E0 E2E s mocky a samostatný reálný Claude test; frontend behavior testy. Použít deterministické bariéry a fault hooks místo náhodného sleep v race testech. Pro concurrency změny cílené `go test -race`, dále repo `go test ./... -count=1`, `go vet ./...`, migrační lint; pro UI `pnpm lint` a statický `pnpm build`. Timeout/skipped nutného scénáře není PASS.

Release artefakt musí obsahovat commit SHA, konfiguraci/verze/hardware, příkazy a exit codes, JSON/CSV metriky, ledger bilanci, fault body, anonymizované logs a T01–T14 PASS/FAIL s odkazy. Žádné skutečné credentials či soukromé transcript fixtures. Reálný CLI test uvádí model/adapter verzi a náklady. Neověřená platforma nedostane označení podporovaného parallel profilu.

## 12. Implementace, migrace a rollout

| Etapa | Výstup a exit gate |
|---|---|
| A: baseline a queue volba | Report metrik, ADR River adopt/reject; při unresolved výchozí SQLite cesta. FULL a cancellation ověření. |
| B: durable acceptance/dispatch | Schéma a jediný claim owner, lease/recovery, operation evidence, všechny vstupy a mailbox; T02–T05/T08–T09. |
| C: E0 a memory | Run identity/cesty, sidecar attribution, mutation kontrakt a recovery; T06–T11. |
| D: podpisy a ovládání | Oba profily, legacy compatibility, delivery/work API/UI, retence; T01/T12–T13. |
| E: release drill | Zátěž a T01–T14, migration/rollback drill, zveřejněný adapter capability matrix; všechny povinné gates PASS. |

Každá implementační issue odkazuje na invarianty a T IDs, uvede vlastníka, dotčené entrypointy, migraci a důkaz dokončení. Nezakládat implementační závazek na počtu odškrtnutých balíčků; chybějící 1+1 blokuje tuto release funkci.

Migrations pouze append podle repo konvencí. Před změnou ownership existujících front pozastavit dispatch, nechat aktivní běhy dokončit/reconcile a transakčně převést pending položky se stabilním mapováním ID. Otestovat upgrade fixture ze stávajícího schématu bez dvojího spuštění. Staré webhook URL/signature profily zůstávají přes legacy adaptér; nepřevádět podpisové chování bez explicitní změny konfigurace.

Zapínání po workspace: nový durable ingress → scheduler v sériovém režimu pro ověření → ověřený Claude 1+1. Shadow scheduler smí jen počítat rozhodnutí, nikdy spouštět práci. Vypnutí parallel flagu zastaví nové paralelní claims a drainuje současné běhy; nesmaže frontu ani receipts. Downgrade na starý binár bez podpory nových pending stavů není online rollback: vyžaduje drain/export a ověřený restore postup. Záloha DB bez kanonických memory souborů a intents není konzistentní záloha celé funkce.

**Dokument je dokončené zadání, ne osvědčení hotové implementace.** River/E1 spiky, nové schéma, API, zátěž a fault-injection sada zůstávají implementační prací. Výzkum E1/E2 a op-log memory neblokuje popsaný omezený profil, ale nesmí maskovat jeho explicitní hranice.
