# Pages editor — oponentura implementace PR #2492

Datum: 11. září 2026. Autor: Codex.
Předmět: https://github.com/crewship-ai/crewship/pull/2492
Ověřená hlava: `93246b89264aa941733e1b7f5c6107ad3f615b96`.
Pracovní kopie: `.claude/worktrees/pages-editor`; produkční soubory beze změn.

## Verdikt

Výrazně dobrá implementační práce, ale zatím požaduji změny před merge.
Dvě reprodukované chyby zasahují přímo hlavní slib: člověk publikuje až po
zobrazení a kontrole správných podkladů. Třetí nález z kontroly volací cesty se
týká ztráty neuložených změn při odchodu globální navigací.
Nejde o zpochybnění serverové transakční kontroly; chybí dokončení vazby mezi
zobrazenou evidencí a kontrolovanými serverovými hodnotami.

## R1 — vysoká: nový otisk lze schválit nad starým detailem Page

Místa: `components/features/pages/editor/application-review.tsx:101–190,244–250`,
`hooks/use-page-review.ts:305–340`, `internal/api/pages_project_publish.go:374–383`.

Definition diff se počítá z `props.page`, tedy samostatného detailového dotazu.
Publish odesílá `snapshot.baseline.definition_digest` z review endpointu.
`ConsentBasis.live` hlídá změnu vykresleného detailu, ale neprokazuje jeho shodu
s otiskem snapshotu. Komentář tvrdící, že tím okno nesouladu uzavírá, je silnější
než skutečná ochrana.

Scénář:
1. Detail i review odpovídají živé definici A.
2. Jiný autor uloží B. Review dotaz získá digest B; detail zatím zůstává A.
3. Checkbox se správně vynuluje, ale uživatel jej může ihned znovu zaškrtnout.
4. Stále čte diff A → kandidát. Request už posílá digest B.
5. Je-li B stále aktuální, serverová kontrola jej přijme: neumí zjistit, že UI
   ukazovalo jinou definici.

Reprodukce komponentovým testem: změnit pouze digest snapshotu, ponechat starý
page prop, znovu souhlasit. Očekávané zablokování Publish neplatí.
Úspěšnou HTTP publikaci v tomto scénáři jsem živě nespouštěl; její možnost
vyplývá z ověřené konstrukce requestu a serverové porovnávací podmínky.

Oprava: přenášet autorizovaný podklad definice společně s odpovídajícím otiskem
v review snapshotu, případně detail opatřit serverovým identifikátorem téže
verze a před souhlasem vyžadovat shodu. Nepočítat hash z dnešního ztrátového
wire DTO: server hash počítá z přesných uložených bajtů. Samotné další resetování
checkboxu nebo současný refetch dvou dotazů nestačí.

## R2 — vysoká: souhlas funguje před načtením zdrojů i po chybě načtení

Místa: `application-review.tsx:213–239,244–266,580–594`;
`hooks/use-page-review.ts:255–293,367`.

Blockers nezahrnují nepřítomný kandidátův zdroj, jeho pending/error stav ani
čekání na zdrojový baseline. `candidateMoved` je při chybějících datech false.
Diff může zůstat na „Reading…“, ale souhlas i Publish jsou dostupné, protože
snapshot už obsahuje připravený build a serverové blockers jsou prázdné.

Dva samostatné testy s `candidate.data = undefined` (pending a error) potvrdily,
že po zaškrtnutí souhlasu je Publish povolený. Testují reálnou komponentu
s řízenými odpověďmi hooků; nejde o živý browser/API průchod.

Oprava: definovat stav kompletně dostupných a navzájem odpovídajících podkladů.
Bez něj souhlas i publikaci blokovat. Chybu zdrojového dotazu ukázat jako chybu
s retry, nikoli jako nekonečné načítání. Pokrýt kandidáta i baseline; počáteční
publikace je explicitní výjimka pouze pro neexistující předchozí zdroje.

## R3 — střední: globální navigace obchází ochranu neuložených změn

Místa: `components/layout/app-sidebar.tsx:100`,
`components/features/pages/editor/use-editor-route.ts:101–197`,
`hooks/use-navigation-guard.ts:39`, `hooks/use-workspace.ts:163`.

Editor chrání své přechody, Back/Forward, reload a přepnutí workspace.
Jediný produkční spotřebitel obecného `navigationAllowed` je ale přepínač
workspace. Odkazy v globálním sidebaru jsou obyčejné Next Link a tuto kontrolu
nevolají. Client-side odchod například do Routines není reload ani popstate;
editor se odmountuje a neuložený formulář zanikne.

Nález je z kontroly volací cesty, nikoli z opakovaného živého browserového testu.
Doplnit ochranu odchodu napříč aplikační navigací a integrační test:
změnit název → Routines → Keep editing / Discard. Ověřit desktop i mobil.

## Co nezávislé ověření potvrdilo

- Aktuální frontendová sada: **53 souborů / 822 testů prošlo**, 7,93 s.
- Cílený backend: review, publish fences, nedostupný baseline, zastaralá revize,
  first-project flag: **prošel**, balíček internal/api 4,532 s.
- Tři nové adversariální sondy výše: **3 selhání očekávané ochrany**.
- Aktuální PR má plné dokončené CI včetně Go Race a Pages isolation;
  nejde o starý problém stacku s label/surface místo testů.
- Kontroly obsahují šest SKIPPED. Údaj 31 zelených zahrnuje CodeRabbit status;
  nelze jej číst jako 31 dokončených testovacích jobů.
- GitHub při kontrole hlásil CLEAN; aktuální main `ecab95ba` je předkem hlavy PR
  (compare head...main: ahead_by 0, behind_by 27).
- CodeRabbit skutečně reviewoval `4ddadf04` a `8d9317f2`. Obě formální review
  mají CHANGES_REQUESTED, poslední stav throttling; z toho samotného nevyvozuji
  neopravené chyby. Závěrečný diff je komentář a test, nikoli změna runtime.
- Jediné otevřené review vlákno žádá HTTP 400 v OpenAPI. V aktuální routě i
  generovaném JSON již 400 je: zbývá procesní uzavření vlákna, ne nová oprava.
- Nové serverové fences jsou uvnitř publikační transakce, včetně rutin.
  Zachovaná idempotentní cesta vrací historický receipt bez opakované publikace.

## Další hodnocení a omezení

Serverový explicitní bypass `acknowledged_unavailable_baseline` je dokumentovaný
s auditním záznamem, není to původní tichá klientská mezera. Je ale produktovou
výjimkou pro CLI/API; UI ji nenabízí. Před uzavřením kontraktu potvrdit, že tato
odlišnost cest je zamýšlená. Nepovažuji samotnou existenci explicitního příznaku
u oprávněného vydavatele za prokázané obejití autorizace.

Známý falešný diff zlomkového SLA stojí za opravu: právě review obrazovka má
omezovat plané poplachy. Není stejně závažný jako R1/R2.

PR body, evidence a chat používají různá počítadla (816 / 821 / 822) a body stále
popisuje nedostupný baseline jako klientskou kontrolu, přestože kód už serverovou
kontrolu má. Sjednotit aktuální předání na jednu hlavu a jeden rozsah ověření.

Přiznané spuštění seed proti dev3 nepřezkoumáno: nedělal jsem žádné zápisy do
živé instance a neověřoval auditní stopu vytvořených dat. Přiznání chyby je dobré,
pro uzavření incidentu ale chybí konkrétní inventář přidaných objektů a jejich
případných aktivních triggerů. Neprovádět plošný cleanup ani reset.

Neopakoval jsem celé repo-wide Go testy, Docker/CLI akceptaci, mutační experimenty,
21 browserových záběrů, kontrast ani uživatelské měření. Nesoudím vizuální kvalitu
produkčního editoru jen podle počtu screenshotů. Tato oponentura je zaměřená na
kritické review/publish cesty a ochranu rozpracované práce, ne audit všech 60 souborů.

Lokální důkazy:
- `/tmp/pages-editor-independent-frontend.log`
- `/tmp/pages-editor-independent-go.log`
- `/tmp/pages-editor-independent-probes.log`
- `/tmp/pages-editor-independent-probe.test.tsx`

Sondy byly spuštěny dočasně vedle stávajících testů a poté z pracovního stromu
odstraněny. Zdroj sond je zachován v /tmp; lze jej pro reprodukci zkopírovat do
`components/features/pages/editor/__tests__/` a spustit přes pnpm exec vitest run.
Žádný produkční soubor nebyl změněn, PR komentář nebyl odeslán, merge ani nasazení
neproběhly.
