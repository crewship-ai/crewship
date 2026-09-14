# Routines — validace aktuálního dev1, 14. září 2026

**Verdikt: hlavní funkční průchody jsou doložené; celé PRD nelze přijmout jako uzavřené.**
Dnešní validace našla jedno občasné selhání velkého běhu a dvě reprodukovatelné
UX vady při zavírání editoru. Navíc nasazená binárka hlásí `dirty=true` a lidská
přejímka zůstává otevřená. Tato zpráva neoznačuje úspěch testů za přijetí UX.

Závazný rozsah: [ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md](../ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md),
včetně omezení Release 1.0 v §11. Historické §12–14 a protokoly z 10.–12. září
jsou předchozí důkazy, nikoli dnešní výsledky.

## Identita, metoda a ochrana práce

- Cíl: https://crewship-dev1.unifylab.cz/login; přihlášení provedeno skutečným
  formulářem v Chromium. Následovaly skutečné stránky a API stejné veřejné domény,
  bez lokálního přesměrování na jiný frontend.
- `main` a čerstvě načtený `origin/main` shodně
  `bfa28fd8794ca3ec44369a21f0c7277631246777`. Checkout již byl na latest main;
  nebylo potřeba přepínat větev nebo přepisovat pracovní soubory.
- API `/api/v1/system/version`: stejný commit, build `2026-09-14T20:07:29Z`,
  **dirty=true**, Go 1.27.1, schema `20260913155203`.
- Proces dev1 PID `1130831`; SHA-256 `/proc/1130831/exe` shodná s binárkou
  `/tmp/crewship-1-dev`:
  `4c122b6eee565b87fcba54e2fb9cb8bf2da992cf9caa0b5d5559b171a0c0d900`.
- Frontend marker uvádí stejný commit. Samotný marker ani shoda VCS revize
  neprokazují čistý reprodukovatelný build. Dnešní výsledky platí pro uvedenou
  binárku a živý frontend, ne pro tvrzení „nasazeno čisté main“.
- Použit vlastní existující testovací účet/workspace
  `cmtx1r5bh0273f3c24619`, vlastní dočasné recepty, plány a běhy. Žádné cizí
  rutiny nebyly upravovány. Původní necommitnuté soubory checkoutu zachovány.
- Bez restartu serveru, změny systémového času, nasazení, volání placených modelů
  nebo skutečných externích HTTP účinků. Vzorek má deterministické transformace,
  lidská čekání a foreach; neposuzuje kvalitu agentní práce.
- Důkazy, reprodukční skripty, screenshoty, logy a privátní autentizační stav:
  `/srv/crewship/backups/crewship_1/routines-validation-20260914T201310Z/`.
  Celý adresář je soukromý; autentizační soubory nepublikovat.

## Otevřené nálezy

### V1 — velký běh může selhat při uložení výsledku kroku

**Závažnost: funkční problém, příčina zatím nepotvrzená.**
Scénář: 99 top-level transformací a foreach nad 1 000 položkami, každý s jednou
transformací, paralelismus 2. Žádní agenti ani externí integrace.

Běh `run_cmu1oqrvq0039dc9e034b`, 20:18:26–20:19:06 UTC, skončil FAILED:

```text
foreach step "items": item 758 failed: body step "item" failed:
record step result: context deadline exceeded
```

Následné čtení vrátilo 1 100 exekucí: 1 098 completed, 1 failed,
1 interrupted. Výstup čisté transformace se nepodařilo včas zapsat;
nešlo o špatný vstup testovacího receptu. Místo propagace chyby:
`internal/pipeline/step_executions.go`, `ExecutionStore.finish` (samostatný
pětisekundový kontext) a defer v `Executor.runStep`. To určuje místo selhání,
**ne diagnózu** zámku databáze nebo přetížení.

Ve stejné době běžela cílená frontendová sada se dvěma workery. Souvislost se
zatížením je hypotéza. Samostatné opakování
`run_cmu1osn8p00426af7ec1e` dokončilo 1 100 exekucí, API je vrátilo v 11 stranách;
první použitelné zobrazení trvalo 810 ms a pozorovalo 35 API odpovědí. Také první
předchozí pokus dokončil běh. Není to deterministicky reprodukovaná vada ani
bezvýhradný PASS zátěže. 1 100 exekucí neznamená 1 000 retry pokusů.

Důkazy: `large-failure-evidence.json`, `recheck.json`, `large-detail.png`.
Další práce: odlišit čekání na DB, plánování na hostu a timeout zápisu; přidat
regresi reprodukující skutečnou příčinu. Nepřidávat slepý retry externích účinků.

### V2 — zavírání uloženého draftu nepravdivě tvrdí, že draft neexistuje

**Závažnost: UX a pravdivost obsluhy.**
Reprodukce: uložit draft se změnou názvu → znovu otevřít Edit → nic neměnit →
Cancel. Editor přitom ukazuje „Saved draft · Publish updates the live recipe“.
Potvrzení říká:

```text
You have unsaved input. Closing throws it away — there is no draft.
```

Po potvrzení Discard zůstal draft na serveru beze změny ID, revize i dokumentu.
Nejde o ztrátu dat; dialog popisuje jiný stav, než skutečně existuje.

`components/features/routines/routine-create-dialog.tsx:1173` odvozuje dirty
mimo jiné porovnáním názvu s publikovanou rutinou; uložený draft tak nadále
působí jako neuložená práce. Společný text je
`components/layout/create-surface.tsx:340`.
Důkazy: `saved-draft-close.json`, `saved-draft-misleading-close.png`.
Oprava má odlišit publikovanou verzi, uložený draft a změny od posledního
uložení; text zavírání musí odpovídat tomu, co se opravdu zahodí.

### V3 — návrat z editoru nezachová klávesnicový fokus

**Závažnost: přístupnost / §9 klávesnice.**
Na stejném průchodu Edit → Cancel → potvrzení skončil `document.activeElement`
na `BODY`, nikoli na Edit. Aktuální editor je stránka (`presentation="page"`),
ne původní modal. Escape jej také nezavřel; to samo nehodnotím jako samostatnou
vadu stránky, ale historický důkaz Escape/modalu již neplatí pro tuto cestu.

`components/features/routines/routine-card-detail.tsx:125` přepíná zpět pohled;
`components/layout/create-surface.tsx:368` vykresluje stránku. Obnova focusu ve
stejném shellu existuje pouze v dialogové větvi. Důkaz: `saved-draft-close.json`.
Oprava má po návratu do detailu zaměřit dostupný smysluplný ovládací prvek.

## Pokrytí celého aktuálního rozsahu PRD

PASS níže znamená pouze konkrétní uvedený důkaz. Není souhlasem člověka s UX.

| Oblast | Dnešní výsledek a hranice |
|---|---|
| §1–2 problém a podklady | Vizuálně zkontrolován živý seznam, detail, Edit, Publish a výsledky. Bez nové uživatelské studie či přihlášeného srovnání konkurence. |
| §3 pevné hranice | Stejný explorer a vizuální komponenty; List/Map dostupné; anglické časy se zónou; všech pět kalendářních pohledů viditelných. Kontrola 390 px a reduced motion prošla. Není kompletní audit přístupnosti. |
| R1 / §5.1–5.2 | Účel a počet kroků v seznamu, lidské názvy a společný detail výsledku ověřeny. Chyba kroku 15 se zobrazí nahoře a krok se otevře za limitem prvních 12. Dnešní vzorek nejsou všechny rutiny demo workspace; porozumění člověka nezměřeno. |
| R2 / §5.3 | Browser odmítl záporný Count, přijal 7, v historii zachoval 7 a false. API odmítá neplatné presety. Defaulty a typy pokryté testy. Autorizované file/credential pickery jsou výslovně mimo Release 1.0, nikoli hotové. |
| R3 / §5.4–5.5 | Dva skutečné browser kontexty: A uloží, B dostane konflikt a podrží svůj text. Publish vyžaduje náhled a Confirm, také přes Ctrl+Enter; návrat zachová text, publikace 201 a toast. Odklady po nové publikaci provedou původní v1. Zavírání má V2/V3. |
| R4 / §7 testovací režimy | Test routine uvádí statickou kontrolu bez agentů/script/HTTP. Fixture API transformace vrací spočtené 3; HTTP bez replacement odmítne, s explicitním replacement vrátí fixture režim. Browser importoval běh a test kroku vrátil 7. Run vyžaduje potvrzení účinků i bez vstupů, ukazuje host a credential typ bez spuštění externí akce. |
| R5 / §6 | Failed, Cancelled a neověřený výsledek mají odlišná pravdivá shrnutí. Run again upozorňuje na opakování práce. Chyba při uložení exekuce V1 zůstává. Obecné pokračování od libovolného kroku je podle §11 mimo Release 1.0. |
| R6 / §8 | Stejný run/rozhodnutí v Routines i Inbox; skutečné formulářové odeslání v obou místech zachovalo count=0 a enabled=false. Dva protichůdné API verdikty 200/409; opožděná odpověď 409. Issue takeover dnes pouze serverové testy, nikoli celý browser průchod. |
| R7 / §5.5 | Jednorázový start vytvořen formulářem, uložený pin v1, položka v kalendářním API i otevřený kalendář; zrušen přes UI a z pending seznamu zmizel. Recurrence viditelná v Plan se zónou Europe/Prague. DST dnešní test skutečné dispatch cesty s řízenými hodinami. Všechny pohledy kalendáře nebyly podrobeny samostatné interaktivní editaci. |
| R8 | Typované otázky a pojmenované akce fungují na obou rozhodovacích plochách. Autorovací builder pokrytý frontendovou regresní sadou, nikoli dnešním samostatným kompletním průchodem tvorby formuláře. Obecný návrhář větví je mimo rozsah. |
| R9 | Zachycený běh načtený přes UI do vzorku, transformace spočtená; původní běh se tím neobnovuje. Izolace HTTP/agent/script fixture režimu testována kontraktně. Řízené pokračování a schema-aware návrhy podřízených rutin odložené v §11. |
| R10 | Dvě skutečné publikované verze 1/2 spuštěny z Versions, výsledky 1/2, konečný export complete=true a dva completed run IDs. Bez modelové kvality; serverové datasety a sémantické hodnocení jsou mimo Release 1.0. |
| §5.4 New / Code | Tři vstupní cesty viditelné, Write it yourself otevřel stejný editor s Save draft. AI autorování skutečným modelem a celý fork průchod dnes neprovedeny. Zachování pokročilého Code bufferu má testy, ne dnešní úplný browser round-trip všech konstrukcí. |
| §7 backend / DB | Zápis, publikace, rollback, re-enable, activate i wake preset testovány. Žádná nová migrace ani změna kompatibility v této validaci. Celý upgrade/export staré DB dnes znovu neprováděn. |
| §8 výkon / live | Běh 100 top-level kroků / 1 100 exekucí stránkovaný; jedno měření 810 ms, ne srovnatelný benchmark. Výpadky načítání run/executions/artifacts/archivu mají Retry a zachování kontextu. Reconnect mezery, SSE zátěž a všechny focus/scroll aktualizace dnes nepokryty. |
| §10 hotovo | Technické uzavření blokují nálezy, důkaz čistého buildu a neověřené části. Validace sama nevytváří PR ani neuzavírá historické review mezery. |
| §11 lidská přejímka | OTEVŘENO. Interní browser automatizace nenahrazuje pět uživatelských úloh ani cíl 4 z 5 reprezentativních uživatelů bez nápovědy. |
| §12–14 historické dodatky | Historické run IDs, baseline a měření nejsou přepsané. Dnešní protokol je nové pozorování jiného buildu a editoru. |

## Matice §9 — všech 16 scénářů

| Ř. | Scénář | Dnešní důkaz / verdikt |
|---|---|---|
| 1 | Bez vstupů, jednoduchý úspěch | PASS; potvrzení účinků bez prázdného formuláře, skutečný completed run a výsledek. |
| 2 | Typované vstupy a defaulty | PASS v podporovaném Release 1.0 rozsahu; browser a API, historické hodnoty. |
| 3 | Opakovaný request | PASS pro dvě souběžná API volání se stejnou **hlavičkou** Idempotency-Key; další vědomý start jiné ID. UI dvojklik má cílené testy, dnes nebylo živě měřeno každé gesto. |
| 4 | Publish během čekání | PASS pro delay i koalescovaný debounce; oba skutečně dokončeny na v1 proti v2. Restart/publikace viz ř. 9. |
| 5 | Dva editoři | PASS; skutečné dva kontexty a zachovaný text B. |
| 6 | Změna schématu s plány | PASS v testovaných dveřích: save, rollback, enable, activate draft, wake; oprava presetu umožní pokračovat. |
| 7 | Skipped, foreach, pokusy | Skipped má vlastní execution a po rozbalení důvod Condition was false. Foreach má oddělené exekuce. Retry semantika pokryta regresními testy; velký běh má V1. |
| 8 | Nejistý externí zápis | PASS pouze v serverovém recorder testu; nikoli nová živá egress reprodukce na dev1. Žádné exactly-once tvrzení. |
| 9 | Tvrdý restart během práce | Dnes nepřezkoušeno restartem dev1. Prošly aktuální testy obnovy/snapshotů; starší kill -9 důkaz nelze označit za dnešní. |
| 10 | Dvě rozhodnutí / timeout / takeover | Browser odeslání oběma cestami + živý API souběh a expiry. Takeover jen serverové testy. |
| 11 | Výsledek bez completion signalu | PASS; completed/FAILED a zachovaný výstup, browser správně nehlásí potvrzený úspěch. |
| 12 | Chyby načítání / chybějící archiv | PASS pro run, executions, artifacts, verze, archiv a stránku journalu. Skutečný archiv 999 vrací 404 bez náhrady HEAD. Journal lookup dekorace není tímto testem pokryta. |
| 13 | Jednorázový plán / recurrence / DST | Browser + API a dispatch testy. DST bez změny času hostu. |
| 14 | Neoprávněný uživatel / cizí soubor | Dnes živě anonym 401 a cizí workspace 403. Úplná matice se skutečným druhým účtem a cizím souborem nebyla opakována; historický protokol ji nenahrazuje. |
| 15 | Klávesnice / mobil / reduced motion | Částečně: 390 px, potvrzení publikace a reduced motion prošly; obnova focusu V3 neprošla. |
| 16 | Velký běh | SMÍŠENÝ: dokončených 1 100 exekucí/11 stran, zároveň skutečné občasné selhání V1. Jedno měření není baseline benchmark. |

## Identifikátory a regresní kontroly

- Typované vstupy: `run_cmu1oqbap002e6f6ca3fe`, výstup `7`, uložené `enabled=false`.
- Odklady na v1: `run_cmu1oprtd002b99ce1cbd`, `run_cmu1onud7001e7c40c7d2`.
- Zrušení při odpojení volajícího: `run_cmu1opdi5002ad322dbf5`, CANCELLED,
  mezivýsledek zachován, následný krok se neprovedl, browser „Run stopped“.
- Výsledek bez potvrzení: `run_cmu1ox0vw0054f8da5add`.
- Rozhodnutí skutečně odeslané z Routines: `run_cmu1p4ti6005d8de4fe6f`;
  z Inboxu: `run_cmu1p4uj60060be0d8f8d`. Oba completed, data 0/false.
- Jednorázový plán: `pnd_cmu1ozkty0029b3a9a1c1`, 15. 9. 2026 14:30 UTC,
  pin v1; zrušen přes UI.
- Dokončené porovnání: `run_cmu1p2o6h00596f82ccec` (v1 → 1),
  `run_cmu1p2oje005ad8a30697` (v2 → 2).
- Go: cílený výběr API + pipeline, **255 testů/subtestů PASS**, oba balíky PASS;
  včetně presetů, draftů, snapshotů, replay, rozhodnutí, nejistého účinku a DST.
  Nejde o celý `go test ./...` ani novou mutační kontrolu.
- Frontend: **53 souborů, 373 testů PASS**, dva workery, 42,97 s.
- `go vet` pro `internal/api` a `internal/pipeline` prošel. Žádný nový build,
  plný repository lint ani celé CI v tomto auditu neběžely; produkční kód nebyl měněn.

## Přesnost sond a úklid

Zachované pracovní reporty obsahují i neúspěšné sondy. Nejsou všechny vadou
produktu: opraven byl neexistující widget `toggle` na `boolean`, nesprávné
`condition` na `if`, Idempotency-Key přesunut do hlavičky, čtení `rows` místo
`executions` a selektory respektující skutečný text, rozbalení či navigační roli.
Identitní transformace nad textem správně neselže; selhání bylo vyvoláno až
výrazem vyžadujícím JSON. Během delšího měření vypršelo přihlášení a bylo obnoveno
přes login formulář. Žádná z těchto chyb sond není započtena jako produktový nález.

První export porovnání měl `complete=false` a nebyl důkazem dokončení. Následný
průchod výslovně pokračoval přes oba běhy a ověřil `complete=true`, dva výsledky
a dvě správné verze (`comparison-complete-export.json`). Stejně tak pouhá
existence prvku se skrytým důvodem skipped nebyla považována za viditelný důkaz;
finální browser průchod krok rozbalil.

Vlastní dočasné rutiny, opakované plány, uložený draft a budoucí starty byly
uklizeny přes API; auditní historie vlastních běhů zůstává. Žádné cizí relace,
worktrees ani instance nebyly změněny. Tato zpráva je lokální podklad,
nepublikuje nový GitHub komentář, issue ani commit.

## Následná oprava — práce převzata 14. září večer

Tato část popisuje nový stav, nemění výsledky výše. Claim #2473 drží
`crewship_1`, větev `fix/routines-validation-20260914`.

- V1: řízené vyčerpání connection poolu po dobu šesti sekund reprodukovalo
  `context deadline exceeded` při ukládání již dokončeného výsledku.
  `TestExecutionResultSurvivesTemporaryPoolContention` před změnou padá;
  s třicetisekundovým limitem, shodným s produkčním SQLite contention budgetem,
  uloží původní execution ID a výstup. Akce se znovu neprovádí. Konkrétní proces,
  který blokoval původní živý běh, tím není identifikovaný; důkaz se vztahuje
  na reprodukovaný mechanismus a omezené čekání, nikoli na odstranění veškeré
  databázové kontence.
- V2: baseline metadata i textu se posouvá při načtení a úspěšném uložení draftu.
  Nezměněný uložený draft se zavře bez varování; pozdější změny stále vyvolají
  potvrzení, které výslovně říká, že uložený draft zůstane. Dva regresní testy
  nejprve selhaly a po opravě prošly.
- V3: návrat z editovací stránky jednorázově zaměří nově vykreslené Edit.
  Fokus nepřeskakuje při běžném obnovování dat.

V této chvíli se ještě netvrdí merge, nasazení ani dokončení následné přejímky.
Výsledky konečných kontrol budou doplněny po jejich skutečném dokončení.

Kontroly opravy před mergem (#2553): frontend 385 testů / 51 souborů,
plný Go vet, lint (0 chyb, 30 existujících varování) a produkční frontend build
prošly. Prohlížeč nad exportem opravované větve a API dev1 potvrdil uložení,
znovunačtení, pravdivé potvrzení zahození pozdějších změn a návrat fokusu,
včetně šířky 390 px. Nejde o důkaz nasazení nové Go binárky.
Test návratu fokusu po odstranění produkčního `.focus()` zčervenal.
První opakování browser sondy mělo vlastní chyby: očekávalo 200 místo 204
při úklidu a nečekalo na dokončení načtení draftu. Po opravě synchronizace
prošel celý průchod a vlastní fixture byla odstraněna.

CI odhalilo nepovolený `exact` parametr v nových Testing Library selektorech;
běhové testy jej ignorovaly. Testovací zápis je opraven, samostatná typová
kontrola není nahrazena úspěšným frontend buildem. První úplný Go běh
překročil výchozí 10minutový limit balíčku API při průběžném provádění testů;
balíček se opakuje s 15minutovým limitem používaným v CI.

Nezávislé CodeRabbit review hlavy `e735ae40e` (21:51 UTC) požadovalo dvě
změny. Původní oprava V3 pokrývala explicitní zavření, ale ne tlačítko
prohlížeče Zpět: tento odchod nevolá `closeEditor`. Nová sonda zčervenala
v testu i v reálném prohlížeči nad předchozím exportem. Oprava sleduje
přechod `editing=true → false`, vrací fokus a nepřepisuje položku historie;
test současně kontroluje, že se nevolá `pushState` ani `replaceState`.
Obě upravené UI sady prošly (35 testů). Druhá připomínka změnila INSERT
přípravy a závěrečné čtení v databázovém regresním testu na `t.Context()`;
šestisekundový test znovu prošel. Výsledek původního CI se nepovažuje za CI
nové hlavy; ta znovu projde kontrolami i schválením.
