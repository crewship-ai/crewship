# Issues: předávání práce mezi agenty a lidmi

Návrh a implementační/testovací plán, 2026-09-07. Navazuje na
`docs/ux/issues-analysis-proposal-2026-09-07.md` z dev1 a na
[PRD Issues and Routines](PRD-ISSUES-AND-ROUTINES-2026.md).
Nová analýza kódu: `origin/main` @ `038117517`; první implementační část
na větvi `feat/issues-work-clarity`, issue #2447. Routines se zde mění pouze
v návrhu pravidel pro jejich zápisy/spouštění nad issue.

**Stav:** návrh celého kontraktu; implementovaný první díl je popsaný na konci.
Následující nový model lidského převzetí zatím není implementován. Existující
testy dokazují konkrétní dílčí chování, nikoli celé uživatelské scénáře níže.

## Doporučení

Issue má být společné místo pro zadání, práci a převzetí výsledku. Defaultně
pracuje agent, člověk může dodat odpověď, vyřešit jeden krok nebo převzít celý
úkol. Klient potřebuje vidět aktuálního řešitele, další krok a výsledek;
technický způsob provedení má být dostupný až při rozbalení.

Největší další investice má jít do **trvalého záznamu aktuální odpovědnosti
za další krok a bezpečného předání**, nikoli do dalšího chatového panelu.

## Co dnes kód opravdu dělá

| Oblast | Současný stav | Důsledek |
|---|---|---|
| Lidský vlastník + agent | `issue_handler.go:setOwnerOrDelegate` zapisuje oddělené `owner_user_id` a `delegate_agent_id`, při změně jednoho zachovává druhý. | Dobré pravidlo odpovědnosti, ale není to záznam aktuálního lidského pracovníka. |
| Výběr řešitele v detailu | `issue-card-editors.tsx:AssigneePicker` nabízí agenty. Public Create/PATCH umí také `assignee_type: user`. | Lidské přiřazení je v API částečně možné, ale běžné UI a význam polí se rozcházejí. |
| Odebrání přiřazení | Prázdné `assignee_id` volá `clearOwnerAndDelegate`, tedy maže oba typed sloty. | Pouhé doplnění druhého pickeru by mohlo při odebrání agenta zrušit i lidského vlastníka. Nutné samostatné zápisové kontrakty. |
| Start | `IssueHandler.Start` požaduje skutečného delegovaného agenta. Původní UI kontroluje jen existenci `assignee_id`. | Člověk dostává tlačítko, jehož endpoint jeho práci neumí spustit. První díl opravuje tuto neshodu. |
| Take over v Inboxu | `inbox_act.go:Act` sdílí větev s dismiss: přepne čekající session na idle, zapíše receipt a vyřeší kartu. | Nezapisuje lidského řešitele a nenastavuje zákaz dalšího automatického spuštění issue. To není úplné převzetí práce. |
| Současná rozhodnutí | `resolveCardWithReceipt` provádí CAS až po účincích akce. Samotný kód popisuje, že druhá odpověď mohla být doručena, přestože její request skončí konfliktem. | Převzetí a odpověď musí nejprve soutěžit o stejnou revizi; HTTP 409 až po dispatchi není dostatečný kontrakt. |
| Plán a výsledky | `mission_tasks_completion.go:OnAssignmentCompleted` volí task status podle technického statusu, parsuje handoff a při COMPLETED pokračuje do approval/dependency logiky. Callback z `assignments_run.go` nepředává uložené outcome samostatným parametrem. | Nutná cílená zkouška COMPLETED+NEEDS_HUMAN/FAILED ve skutečném plánu. Předávání může mít správné shrnutí a přesto špatnou podmínku pokračování. Zde jde o nález z kódu, ne nově spuštěný důkaz celé kaskády. |
| Pokračování zprávami | `issue_mentions.go`, `issue_session_followups.go`, `issue_deliveries.go`: relace, claimy doručení, limit souběhu a navazující běhy. | Použít existující mechanismus; nevytvářet druhou nezávislou frontu. |
| Ochrana před řetězením | `delegation_limits.go`: hloubka, fan-out a atomické hlídání kapacit. | Základ existuje. Počítání musí dále platit i pro nové předávací akce a lidský návrat práce. |
| Čtení detailu | Comments se načítají celé; activity má LIMIT 50; runs jsou stránkované, standardně 100. Host načítá šest podzdrojů a na několik typů změn je znovu načte společně. | Dlouhé vlákno roste bez omezení, historie má jiné limity než běhy, jedna událost vyvolá zbytečné čtení ostatních částí. |

Zvlášť prověřit vazbu `mission_id` proti starším `group_id/chat_id` při rozhodnutí
z Inboxu. Dnešní Act dohledává issue přes `COALESCE(group_id, chat_id)`;
nový kontrakt nesmí přisoudit převzetí jinému úkolu nebo skupině.

## 1. Tři role, které se nesmějí zaměnit

1. **Vlastník:** kdo ručí za výsledek. Dnešní lidský owner zůstává při delegování.
   Nevyžadovat člověka u každého agentního úkolu; tým může fungovat bez
   explicitně vyplněného lidského vlastníka.
2. **Aktuální řešitel:** agent nebo člověk, od kterého čekáme další práci.
   Jeden hlavní řešitel na jeden vykonávaný úkol; paralelismus přes podúkoly
   s vlastním řešitelem a vztahem k hlavnímu úkolu.
3. **Adresát požadavku:** člověk nebo role, která má dodat odpověď či schválení.
   Požadavek má vlastní stav a rozsah; jeho příjemce se automaticky nestává
   řešitelem celého issue.

Příklad: vlastníkem je Petra, řešitelem Jordan, Casey ověřuje podúkol.
Jordan čeká na rozpočet od Petry. UI řekne „Čeká na rozpočet od Petry“;
Petra odpoví, Jordan pokračuje. Vlastník, řešitel ani spolupráce s Casey se
kvůli jedné odpovědi nepřepisují.

Jiný příklad: Petra vybere „Převezmu celé řešení“. To je změna řešitele a režimu
provádění. Úkol zůstane její prací po reloadu a restartu, dokud ho neodevzdá
nebo znovu nepředá agentovi.

## 2. Předání jako skutečná akce

Společný formulář **Předat práci** nabízí agenta i oprávněného člena workspace.
Uvnitř projektu je předvybraný relevantní tým. Vyhledávání lidí i agentů,
jasná ikona identity, dostupnost a důvod případné nemožnosti předání.
Nezve automaticky externí osobu, nezaměňuje e-mail za existující členství.

Předávací záznam má minimálně:

- odesílatele, adresáta, issue/podúkol a revizi zadání;
- konkrétní další práci a ověřitelné podmínky dokončení;
- dokončené kroky, otevřené otázky a známé překážky;
- odkazy na vstupy a výstupy v konkrétní verzi;
- zvolený rozsah: konzultace / podúkol / celé převzetí;
- identifikátor operace, stav a čas převzetí, vazbu na běh či lidské dokončení.

Agentní předání se při splnění politiky přijímá automaticky. Zaneprázdněný
agent je „Ve frontě“, nikoli „Pracuje“. Lidské předání se ihned ukáže adresátovi
jako „Přiděleno, nezačato“; tlačítkem Zahájit práci potvrdí začátek. Nemá
vznikat druhý potvrzovací dialog po běžném kliknutí na Převzít.

Mention zůstává způsobem oslovení/konzultace, **nemění automaticky hlavního
řešitele**. „@Casey, zkontroluj…“ a „předávám celé issue Casey“ jsou rozdílné
úkony. Agentní API/CLI musí nabízet tentýž význam jako lidské UI.

Odmítnuté předání zachová původního řešitele a zobrazí důvod. Převzetí na
neexistujícího/neoprávněného adresáta nesmí odebrat práci původnímu řešiteli.

## 3. Co přesně znamená lidský zásah

| Akce | Vlastník | Aktuální řešitel | Automatické pokračování |
|---|---|---|---|
| Odpovědět na otázku | Stejný | Stejný agent | Naváže konkrétní čekající session, právě jednou |
| Schválit krok | Stejný | Stejný agent | Naváže konkrétní waitpoint, pokud ostatní podmínky platí |
| Vyřešit podúkol ručně | Stejný | Člověk pouze na podúkolu | Ostatní nezávislé větve mohou běžet |
| Převzít celé issue | Stejný | Konkrétní člověk | Nová agentní práce v převzatém rozsahu se neplánuje |
| Předat zpět agentovi | Stejný | Vybraný agent po přijetí | Jeden nový běh s ručními změnami a aktuálními podklady |
| Skrýt/odložit požadavek | Stejný | Stejný | Není to odpověď, dokončení ani převzetí |

Při převzetí běžícího úkolu: atomicky zaznamenat záměr, zablokovat nové
dispatchování a požádat o zastavení existující práce. Dokud executor nepotvrdí
ukončení, zobrazovat **„Přebírání — agent ještě ukončuje práci“**. Nepovolit
souběžné úpravy stejného prostředku člověkem na základě falešného „Zastaveno“.
Pozdní odpověď původního agenta se zachová v historii, ale nevrátí mu přiřazení
a neoznačí issue jako hotové. Pro neukončitelný proces nabídnout existující
možnost hard stop s přesným výsledkem, nikoli slibem okamžitého zastavení.

Agent žádající vstup nezaloží každým retry novou lidskou práci. Jeden aktivní
požadavek pro konkrétní překážku a rozsah; po odpovědi má receipt. Nová, věcně
odlišná otázka může založit nový požadavek ve stejném vlákně.

## 4. Trvalost a souběh

Logický zápisový kontrakt předání:

```text
operation_id + očekávaná work_revision + adresát + rozsah + podklady
  → kontrola oprávnění, existence, stavu a kapacit
  → krátká DB transakce:
       vyhrát změnu revize (CAS)
       změnit aktuální přiřazení / pending takeover
       uložit handoff a jednu událost s pořadovým číslem
       vytvořit trvalé doručení / požadavek
  → commit
  → worker provede odpovídající dispatch nebo stop
  → navázaný receipt a aktualizace UI
```

Žádné čekání na LLM, Docker nebo síť uvnitř transakce. Opakované operation_id
vrátí stejný výsledek; jiný obsah se stejným klíčem odmítnout. Prohra revize
znamená žádný vedlejší účinek, ne druhý běh s pozdějším HTTP 409.
Po pádu mezi commitem a spuštěním doručení dokončí existující durable worker.

Fyzické schéma musí mít autoritativní identitu aktuálního řešitele a revizi,
oddělenou od `owner_user_id`. Před migrací inventarizovat public/internal
Create/PATCH/Bulk, Start, Review, Inbox Act, sidecar /assign, recurring issues,
manifest a restore. Neudělat front-endovou iluzi přiřazení pomocí posledního
komentáře nebo přepisu owneru. Preferovat stávající deliveries a seq události;
nenavrhovat nový broker ani duplicitní obecný event store.

Všechny cesty agentního spuštění musí respektovat ruční režim ve stejném
serverovém guardu. Zakázání tlačítka v prohlížeči nebrání mention, rutině či CLI.
Autonomie a dostupný rozpočet zůstávají platné i po předání. Návrat člověk→agent
nesmí vynulovat historii limitů a umožnit nekonečné delegování.

## 5. Kvalita a dostupnost výsledků

Ukládat odděleně technický stav běhu, hlášený outcome, odevzdaný výstup a
výsledek ověření/přijetí. Confidence agenta není ověření. Dlouhý text nesmí
existovat jen jako useknutý automatický komentář.

Textový úkol může odevzdat plný text přímo v issue; souborový úkol trvalý
artefakt s ID, verzí/hash, typem, velikostí a autorizovaným odkazem. Cesta
`/crew/shared/report.md` je kontejnerová cesta, nikoli odkaz dostupný klientovi.
Předání dostane krátké shrnutí a manifest; velký obsah načte podle potřeby.
Chybějící artefakt je pojmenovaná blokace, nikoli `SUCCEEDED` bez dokumentu.

Úspěch plánu smí odblokovat závislý krok až podle splněných podmínek:
`NEEDS_HUMAN`, `FAILED` a `PARTIAL` nejsou automaticky úspěšný předchůdce.
`NO_CHANGE` může být legitimní dokončení, pokud to připouští zadání.
`WORK_CREATED` říká, že vznikla další práce, ne že je hotový její výsledek.
Na dokončení nemusí vždy čekat člověk: nízkorizikový automaticky ověřitelný
úkol může uzavřít schválená politika. UI uvede, kdo/co splnění ověřilo.

Obsah úkolu se může změnit během běhu. Výsledek patří k revizi zadání;
starší výsledek se nesmí bez upozornění vydávat za splnění nového zadání.
Při návratu z ruční práce uvést konkrétní změny a aktuální verze podkladů,
aby agent nepokračoval jen ze svého starého checkpointu.

## 6. UI pro klienta

Tabule zůstává základní navigace. Karta: název, aktuální řešitel (lidský nebo
agentní avatar), případně jedna důležitá věta. Například „Čeká na Petru:
schválit nabídku“. Vlastník zůstane v detailu; neukazovat dva nerozlišené avatary.

Filtry: **Moje práce**, **Potřebuje mou odpověď**, **Agenti pracují**, **Blokované**.
„Moje práce“ není `owner_user_id = já`; „Potřebuje mou odpověď“ musí zohlednit
adresáta, oprávnění a nevyřešený action contract. REVIEW celé firmy není můj inbox.

Detail prioritizuje:

1. Aktuální situaci, řešitele a jednu kontextovou hlavní akci.
2. Výsledek/rozhodnutí, pokud čeká na klienta; jinak právě probíhající práci.
3. Jednu časovou osu: zadání kroku → předání → převzetí → výsledek → rozhodnutí.
4. Zadání, podmínky dokončení a podúkoly.
5. Vlastnosti a rozbalitelné technické informace.

Nepřidávat další samostatné panely Chat / History / Runs / Journal pro tentýž
obsah. Technické události seskupit pod běh, systémové potvrzení komentáře
nezobrazovat jako duplicitní zprávu. Zprávy zůstanou úplně dostupné.
Při nových událostech neposouvat rozečtenou historii; nabídnout „3 nové události“.

## 7. Rychlost a výkon

Nezavádět LLM volání pro běžné zobrazení stavu, třídění nebo každé otevření
detailu. UI čte uložená fakta; shrnutí může vzniknout jednou při předání nebo
odevzdání. Náklady a limity vyhodnocuje backend.

Navržené rozpočty níže jsou **cíle k měření, ne naměřené výsledky**:

| Měření | Počáteční cíl |
|---|---|
| Přiřadit / předat / přijmout požadavek | p95 serverové odpovědi do 500 ms bez práce executoru |
| První použitelný detail | p95 do 1 s na dohodnuté dev/test konfiguraci, oddělit cold/warm |
| Potvrzení zaznamenané zprávy v UI | do 1 s; zahájení LLM má vlastní čas |
| Počet načtených událostí | první stránka 50; starší na vyžádání přes kurzor |
| Frontend při burstu událostí | max. jedna rozpracovaná aktualizace podzdroje + jeden sloučený následný refresh |
| Dotazy na seznam | žádný N+1 na kartu; měřit počet SQL a velikost odpovědi |
| Ztracené operace / dvojí převzetí | nula v deterministických fault/concurrency scénářích |

Prakticky: serverový souhrn karty bez stahování promptů, stránkované komentáře
a sjednocené seq události, oddělené načítání velkých výstupů, selektivní
invalidace cache podle issue a typu změny, slučování burstů, cancellation
zastaralých requestů. Odpojení WS řešit cursor resync; pravidelný polling jen
jako omezený fallback pro viditelný aktivní detail. Board counts musejí
odpovídat serverovým filtrům, nikoli jen první stažené stránce.

Zátěžová sada: 10 000 issues, 100 000 událostí, jedno issue s 10 000 komentáři,
20 souběžných čtenářů, 10 souběžných zapisovatelů; oddělit výkonnost SQLite
na disku od testů v tmpfs. Zaznamenat CPU, RAM, DB velikost, indexy, cache,
p50/p95/p99 a chybovost. Až potom rozhodnout o FTS či virtualizaci;
nejdříve odstranit plná opakovaná čtení historie.

## 8. Ověřovací matice

Každý nový scénář má pozorovat API i to, co následně vidí druhý klient po
reloadu. Mock executor je vhodný pro deterministické souběhy, skutečný agent
potvrdí dostupnost kontextu a výstupů; nesměšovat tyto důkazy.

| ID | Scénář | Rozhodující důkaz | Dnešní opora / zbývající práce |
|---|---|---|---|
| H01 | Vytvoření lidského úkolu, ruční zahájení a odevzdání | Člověk jako řešitel, nula agentních běhů, uložený výstup a historie | Nový plný scénář; UI guard v #2447 |
| H02 | Člověk→agent při zachování vlastníka | Jedno předání, jeden běh, owner beze změny | `TestIssueDelegate_PreservesOwner_Scenario9`; doplnit receipt+UI |
| H03 | Agent A→B s konkrétním souborem | B přečte očekávanou verzi souboru a vrátí výstup A | Nutné integrační a live ověření; pouhé založení assignments nestačí |
| H04 | Agent→člověk, jen odpověď | Owner/řešitel stejní, pokračuje správná session jednou | `TestInboxAct_Answer_ResumesTheSessionThatAsked`; doplnit rozhodovací race |
| H05 | Člověk převezme čekající issue | Trvalý lidský řešitel, ruční režim, reload souhlasí | Současný takeover test dokazuje pouze idle+receipt; nový kontrakt chybí |
| H06 | Člověk převezme běžící issue | Žádná nová práce v rozsahu, viditelné ukončování, pozdní callback nepřepíše přiřazení | Nová stavová/integr. zkouška |
| H07 | Ruční podúkol vedle agentní větve | Nezávislá větev pokračuje; závislá čeká; parent není předčasně DONE | Existující terminal-children testy + nový smíšený plán |
| H08 | Člověk vrátí práci agentovi | Agent dostane změny člověka a aktuální revize, jedno pokračování | Nový kontrakt a ověření obsahu promptu + dostupnosti artefaktu |
| H09 | Dva lidé převezmou současně | Jeden vítěz, jeden čistý konflikt, žádný účinek poraženého | Současný Inbox CAS je pozdní; nový race test |
| H10 | Odpověď a převzetí současně | Vítěz určuje jediný další stav a případné spuštění | Nový race test před účinky |
| H11 | Opakovaný request / restart mezi commit a dispatch | Stejný receipt, žádná ztráta nebo druhý běh | `TestDeliveries_TenConcurrentIdenticalDeliveriesProduceOneRun`, restart test; rozšířit na handoff |
| H12 | Zpráva za běhu a následná korekce | Jedna session, další běh s korekcí, správná vazba na zprávy | `issue_session_followups_test.go`, priority testy; UI potvrzení doplnit |
| H13 | COMPLETED+FAILED / PARTIAL / NEEDS_HUMAN | UI nehlásí Done; závislý krok se nespustí jako po úspěchu | UI+realtime regrese v #2447; engine část prověřit/opravená scénářem |
| H14 | Úspěch bez dostupného výstupu | Žádný vymyšlený artefakt; jasný chybějící výstup | Regrese chybějícího summary v #2447; artefaktový kontrakt doplnit |
| H15 | Zadání se změní před dokončením | Výsledek označen starou revizí, přijetí vyžaduje posouzení změny | Nový verzovaný kontrakt |
| H16 | Cizí workspace / neoprávněný uživatel / odebrané členství | 403/404 bez změny, dispatchování nebo úniku identity | Rozšířit existující workspace/role guard testy na nové endpointy |
| H17 | Cyklus závislostí a nekonečné předávání | Odmítnuto; platí hloubka, fan-out a rozpočet | Existující dependency/delegation testy + nové akce |
| H18 | 10 000 komentářů, event burst, dva otevřené detaily | Omezené čtení, pouze správné issue se aktualizuje, cílové latence | Výkonnostní scénář nově připravit nad diskovou DB |
| H19 | Výpadek jednoho endpointu / WS mezera | Chyba neznamená prázdno, retry, dohledání chybějící historie | Existující surface error/realtime testy; doplnit kurzory |
| H20 | Mobil 390 px, tablet 820 px, desktop 1440 px, klávesnice | Bez horizontálního overflow, dostupné akce a celý výstup | Browser fixture první části; celý workflow až po implementaci |

## 9. Implementační pořadí

1. **Pravdivé zobrazení** (#2447): outcome a chybějící shrnutí, lidský owner
   není executable delegate, čitelné názvy běhů. Zachovat současnou tabuli.
2. **Autoritativní předání:** samostatná identita aktuálního řešitele a revize,
   atomické převzetí/vrácení, všechny spouštěcí cesty respektují ruční práci.
   Nejprve scénáře H05/H06/H09/H10; teprve pak tlačítko převzetí všude v UI.
3. **Spolehlivé odevzdání:** artefakty, verze zadání, outcome-aware odblokování
   plánu, dostupný celý výstup; H03/H13/H14/H15.
4. **Jedna pracovní plocha:** účastníci, timeline a lidské požadavky, jednotné
   pickery a filtry. Současně stránkování a selektivní realtime, ne až po růstu.
5. **Live přijetí celého smíšeného scénáře:** člověk zadá → agent A předá B →
   B žádá člověka → člověk převezme část → vrátí A → otevře a přijme výstup.

Není nutné čekat na velký redesign pro první opravy. Není ale bezpečné vydávat
pouhou výměnu avataru a PATCH owneru za skutečné převzetí úkolu člověkem.

## První implementační díl

V #2447 jsou nové regresní testy prezentace všech outcomes a aktualizace
po skutečně přijatém `run.outcome` eventu, nikoli jen registrace subscription.
Testy rozlišují lidské přiřazení, agentní přiřazení i současný owner+delegate.
Chybějící výsledek je explicitní; technický prompt není název karty; celé
uložené shrnutí lze rozbalit vedle případné chyby. Nemění to obsah historických
komentářů, neobnovuje chybějící artefakty a neimplementuje lidské převzetí.

Ověření první části dne 2026-09-07:

- `pnpm exec vitest run components/features/issues/__tests__`: 212 testů,
  22 souborů, všechny prošly.
- `TMPDIR=/dev/shm go test ./... -count=1 -timeout=40m -p=3`: prošlo celé;
  API 131 s, databázový balík 417 s. Tmpfs je urychlení testů, není to
  výkonnostní důkaz produkčního SQLite. Docker testy respektují vlastní skipy.
- `go vet -p=3 ./...`: prošlo.
- `pnpm lint`: bez chyb, 32 existujících varování mimo tuto změnu.
- `pnpm build`: prošlo po standardním vygenerování Prisma klienta v novém
  worktree. Nebyla spuštěna databázová migrace přes Prisma.
- `node e2e/issues-work-clarity.mjs`: Chromium 390/820/1440 px, skutečné
  komponenty a CSS aplikace, přístupná práce s klávesnicí, rozbalení celého
  shrnutí, rozlišení lidského vlastníka a delegáta, žádné horizontální
  přetečení ani browser exception. Screenshoty `/tmp/issues-work-clarity-*.png`.
  Jde o izolovanou komponentovou browser fixture, nikoli živé předání úkolu.

První část nepřidává síťová volání ani LLM požadavky. Nový celý návrh nebyl
nasazen na dev1; běžící Inbox/Dashboard zůstaly na své původní větvi.
