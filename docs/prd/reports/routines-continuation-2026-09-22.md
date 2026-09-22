# Routines — navazující kontrola PRD a otevřených PR (22. září 2026)

## Čas a rozsah

Kontrola navazuje na nasazený main `c2f8fa054` (ověřený 16:23 UTC) a
[závěrečný protokol #2651](https://github.com/crewship-ai/crewship/pull/2651#issuecomment-5780095713).
Tento dokument zaznamenává převzetí dalších oprav, ne jejich merge nebo nové
nasazení. Přesné pozdější výsledky patří do protokolu integračního PR #2646.

## Work v Activity: co bylo a nebylo požadováno

Issue #2636 výslovně požadovalo přesunout durable work a deliveries pod Activity,
odstranit samostatné Work z hlavní navigace a zachovat staré odkazy. To je
implementované. Dnešní autentizovaný browser ověřil tři podzáložky Overview,
Work, Deliveries a přesměrování `/work` na `/activity?section=work`, bez JS chyb.
Desktopový i mobilní screenshot jsou v lokální evidenci.

Sloučení podzáložek do jednoho filtrovatelného přehledu je další změna
informační architektury. Dosavadní oprava netvrdí, že sjednotila journal události
a přijaté work items do jednoho seznamu. Při takové změně je nutné zachovat
identity, stav čekající práce, stránkování, detail a oprávněné akce; pouhé
odstranění tlačítka by část funkcí skrylo. Preference byla uživateli položena.

## Dependabot: širší kontrola uzavření

Z posledních 100 Dependabot PR je 60 MERGED a 40 CLOSED, žádné OPEN. To není
potvrzení obsahu všech 100 historických změn. Pro období od 1. září byla
provedena konkrétní kontrola všech 14 PR: 10 MERGED a 4 CLOSED.

| Uzavřené PR | Ověřená náhrada v aktuálním main |
| --- | --- |
| #2441 — 10 Go aktualizací | Integrační commit `cac749f1a` z 7. září je předkem main. Všech 10 požadovaných přímých modulů a obě změněné nepřímé závislosti jsou na požadované nebo novější verzi. |
| #2547 — TypeScript typy | Integrace #2546; aktuálně Node 26.6.1, React a React DOM 19.3.0. Původní požadavek Node 26.5.1 je překonaný novější aktualizací. |
| #2633 — frontend | Nahrazeno #2649, deklarováno 15 přímých aktualizací, zbývajících 179 beze změny. Dependabot PR uzavřel po merge automaticky. |
| #2634 — Node typy | Node 26.6.1 zahrnuto v #2649; PR uzavřeno explicitně po jeho merge. |

Samostatně sloučené aktuální bot PR jsou #2632 (Go) a #2635 (Actions).
Tímto se neignorují další aktualizace a nemění konfigurace Dependabotu.
Automatické review vylučuje lockfile; jeho důkazy jsou frozen install,
kontrola přímých verzí, CI/build a bezpečnostní kontroly, ne domnělé čtení botem.

## Naplnění PRD: nejde o uzavřený celek

| Rozsah | Doložený stav a další práce |
| --- | --- |
| R1–R3 — recept, vstupy, draft/publish | Funkční implementace a browserové důkazy. Dva editoři, 409 bez ztráty textu, Copy/draft/Publish ověřeny i na aktuálním main. Srozumitelnost bez nápovědy stále vyžaduje §11. |
| R4/R9 — Test a vzorky | Aktuální veřejný browser ověřil import reálného běhu, výpočet transformace a explicitní náhradu HTTP. Test není obnovení běhu ani důkaz reálných externích účinků. |
| R5 — selhání a recovery | Potvrzený stop je oddělený od neověřeného konce, zachovává výstup a usage. Nově opravené čekání na člověka není červená chyba. Obecné pokračování od libovolného kroku není součást Release 1.0. |
| R6/R8 — rozhodnutí a formuláře | Doložené formuláře oběma cestami a souběh 200/409; nové autorování R8 bylo zopakované na desktopu/mobilu a aktuálním main. |
| R7 — plány | Verze, plánování, rušení a DST mají technické důkazy. Nová integrace #2646/#2648 řeší bezpečnost startu agentního scheduleru; není dokončením durable I7. |
| R10 — porovnání | Na aktuálním main dokončený export dvou publikovaných verzí (42/84). Nejde o sémantické hodnocení modelů. |
| §8 / §9 velká data | Existují omezená měření handleru a střídavý browserový baseline z 21. září, včetně requestů/bytů. Malý vzorek a sdílený host neopravňují ke garanci výkonu; nejde o celý produktový benchmark ani tisíc reálných retry. |
| §9 restart / nejistý zápis | Testové a historické důkazy existují. Nový úplný živý hard-crash průchod a matice nejistých externích zápisů nejsou tímto doložené. |
| §9 oprávnění | Selektivní živá matice 60 kontrol prošla v předchozí integraci; není to matice všech endpointů. |
| §11 lidská přejímka | NOT VERIFIED. Žádný nový výsledek pěti reprezentativních uživatelů nebyl dodán; cíl 4/5 pro každou úlohu nelze odškrtnout automaticky. |

Původní explicitní odklady Release 1.0 zůstávají odklady, nikoli chybějící
slíbená implementace. Připravený [formulář přejímky](../wireframes/routines-acceptance-recorder.html)
se má použít až při skutečných průchodech; fixture odkazy musí moderátor ověřit
v účtu účastníka.

## Převzetí runtime draftů #2646 a #2648

Původní vlastník #2643 práci výslovně uvolnil 22. září ve 13:49 UTC. Nová
relace crewship_1 issue převzala claimem. Integrační větev obsahuje zdrojové
hlavy `f2275809f` (#2646) a `559d9a19d` (#2648) i současný main.

Osm konfliktů bylo řešeno tak, aby zůstaly obě sady záruk:

- společný per-agent serial admission, čekání před odebráním serverové kapacity,
  odmítnutí synchronního dítěte 409, zachování rezervace po neověřeném konci;
- z main zachovaný `ErrDetachedExecStopped`, částečné odpovědi/usage, konec
  chatového streamu a regrese souběžného uzavření holdů;
- scheduler po chybě čtení occurrence ani po nejistém CreateRun nezačne vykonávat
  agenta; nevyrobí novou identitu a neuvolní nejistou rezervaci;
- potvrzené zrušení před vytvořením procesu zapisuje CANCELLED bez smyšleného
  exit code, s vlastním omezeným kontextem pro uložení výsledku.

Na kombinovaném stromu prošly celé Race sady orchestrator (75,557 s),
chatbridge (1,953 s), pipeline (97,685 s), dispatch (65,658 s), scheduler
(3,889 s). Cílená API regrese, plný Go, vet a finální CI/review při psaní
tohoto snapshotu ještě běží; nejsou označené za zelené.

Integrace je procesová bezpečnostní oprava. Trvalý mailbox, migrace všech
producentů do durable admission, obnova po pádu procesu a release paralelismus
nad reálnými adaptéry zůstávají v otevřeném #2643. Merge této dílčí opravy
issue ani příslušné širší PRD automaticky neuzavírá.

## Oddělený pilot #2630

Jev pilot zůstává bez doložené živé inference a kalibrace. Původní vlastník
uvolnil práci; v prostředí této relace ani `.env.local` nejsou pojmenované
OpenRouter/TypeSafe/Jev testovací credentials. To nevylučuje jejich existenci
v jiném autorizovaném úložišti. Bez ověřeného přístupu se pilot nepovažuje za
hotovou produkční integraci a jeho uzavření jen kvůli počtu PR není správné.

## Evidence

Dnešní UI kontrola a dependency inventura:
`/srv/crewship/backups/crewship_1/continuation-20260922/`.
Regresní logy integrace jsou při práci pod `/tmp/admission-integration-*-20260922.log`;
před předáním budou archivované u závěrečného protokolu PR. DEV2/DEV3 nebyly
měněné; původních 17 WIP souborů v hlavním checkoutu zůstává zachovaných.

## Následné nezávislé nálezy

- Review hlavy `848faa5e1` odhalilo, že synchronní peer query může vstoupit
  do approval/hook ještě před odmítnutím obsazené kapacity. Doplněna časná
  nezablokující kontrola agenta i serveru; rezervace ihned uvolní a autoritativní
  získání zůstává před vytvořením procesu. Nový test před opravou selhal v obou
  případech na zavolání approval gate. DB operace v scheduler testech mají kontext.
- Rozšířená zkouška zrušení běžícího webhooku odhalila rozpor: work je cancelled,
  journal běhu je failed. Reprodukováno také na čistém main `c2f8fa054`
  (51,813 s, jeden run.failed, žádný run.cancelled). Není to nová regrese této
  integrace; oprava před vytvořením procesu ji neřeší. Sledováno jako #2652.
  R5/R6 proto nelze vydávat za bezvýhradně uzavřené napříč všemi producenty.
- GitHub automaticky označil stacked #2648 za MERGED do jeho základní větve
  při pushi integračního `848faa5e1` v 16:43:07 UTC. To není merge do main ani
  nasazení; obojí stále závisí na schválení a merge #2646.
