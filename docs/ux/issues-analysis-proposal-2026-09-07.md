# Issues pro Release 1.0 — analýza a návrh

Datum: 2026-09-07. Podklad: pět screenshotů uživatele, aktuální checkout
`feat/inbox-client-overview` @ `6d8ef23b3`, čtení kódu a read-only CLI dotazy
na běžící dev1 (`localhost:8081`). Návrh; produktové UI se tímto dokumentem nemění.
Routines jsou mimo tento audit, kromě vazby na issue. Nové agentní běhy nebyly
spouštěny, úplná integrační funkčnost ani mobilní chování nebyly znovu testovány.

## Závěr

Tabule má dobrý základ. Největší problém je detail: přednost dostávají zadání
a metadata, zatímco výsledek, blokace a spolupráce agentů jsou rozptýlené.
Backend už obsahuje významnou část koordinace, ale „běh skončil“ není důkaz
úspěchu a „agent uvedl hotovo“ není důkaz dostupného, správného výstupu.

Cílový první pohled odpoví: **Co se děje? Kdo to řeší? Potřebuje mě?
Kde je výsledek?** Další podrobnosti se odkrývají podle potřeby.

## Ověřené nálezy

| Priorita | Zjištění | Důkaz a dopad |
|---|---|---|
| P0 | Stav běhu a výsledek práce se rozcházejí. | Živé `issue runs OPS-7 -f json`: `COMPLETED`, ale `outcome: FAILED`, `error_message: no outcome reported`. Komentář začíná „Morgan completed their work“, následně popisuje blokaci. `issue-runs-card.tsx:94–125` ukazuje status a chybu, outcome vůbec nepoužívá. Člověk dostává protichůdné signály. |
| P0 | Dokončená práce nemusí mít výstup dostupný v issue. | Živé QUA-4: `SUCCEEDED`; `result_summary` je úvodní „Rozumím úkolu…“. Komentář pouze tvrdí, že kontrolní seznam vznikl, uvádí `Artifacts: none`; seznam příloh je prázdný. To neprokazuje ztrátu celého výstupu v úložišti, ale v kontrolovaných issue datech jej klient nenajde. |
| P1 | Detail neukazuje relace agentů a potvrzení předání. | `issue-detail-surface.tsx:189` načítá comments/activity/relations/runs/subtasks/code-links; nenačítá sessions/checkpoints. API relací existuje v `router_orchestration.go:108`. Chybí čitelná odpověď, zda zpráva čeká na zpracování a kdo naváže. |
| P1 | Dva odlišné způsoby práce nelze vydávat za jednu ověřenou cestu. | `Start` v `issue_handler_workflow.go:211` používá mission engine a plán/tasky; mention dispatch používá issue-agent sessions. Živé QUA-4 má task run, ale `issue sessions QUA-4` vrací `[]`. Prázdný seznam relací tedy neznamená, že agent nepracoval. |
| P1 | Kontext pro navazování má skutečné limity. | `issue_context_pack.go:3–24`: snapshot issue, checkpoint a delta událostí; artefaktový manifest není součástí tohoto packu. Snapshot má rozpočet 800 tokenů, checkpoint 600, delta 1200; ořez se označuje. Nelze slibovat automatické předání celého popisu, všech příloh a všech komentářů. |
| P1 | Podstatná komunikace je příliš nízko. | Screenshoty 2–3, `issue-card-detail.tsx:736`: Comments/History až pod hlavními sekcemi; runs, related/journal a spodní nástroje rozdělují sledování práce do více míst. |
| P2 | Nadbytek prázdných a duplicitních informací. | Screenshoty 2–3: první odstavec zadání dvakrát, otevřeno/aktualizováno nahoře i metadata dole, agent/tým ve více kartách, prázdné PR, labels a routine. |
| P2 | New project vysvětluje klientovi implementační omezení. | `create-project-modal.tsx:502–513`: text o odmítnutí endpointu a použití CLI. Skutečně chybějící možnost milníků má mít srozumitelnou produktovou cestu. Pouhá změna textu tuto funkční mezeru neopraví. |

## Jak dnes probíhá agentní práce

- **Spuštění issue:** kontrola stavu a delegovaného agenta, následně mission engine.
  U lead agenta se plán tvoří jinak než u pracovníka. Workflow nesmí UI zjednodušit
  na nepravdivé „kliknutí vždy založí jednu issue session“.
- **Zpráva agentovi:** mention picker zapisuje identifikátor agenta, nikoli jen
  text jeho jména (`comment-composer.tsx:9`). Human comment handler uloží komentář
  a poté zpracuje mentions (`issue_handler_comments.go:65`). Samotné uložení komentáře
  není potvrzení úspěšného dispatchování. Obyčejný komentář bez adresáta není
  automatický pokyn právě pracujícímu agentovi.
- **Zaneprázdněná session:** existuje fronta follow-upů, ochrana proti druhému
  souběžnému běhu a priorita korekcí (`issue_mentions.go:395`,
  `issue_session_followups.go`). Korekce se uplatňuje v navazujícím běhu;
  neslibovat okamžité přerušení nebo změnu právě generované odpovědi.
- **Předání v plánu:** `mission_tasks.go:191` zahrnuje do zadání závislého tasku
  strukturované shrnutí, artefakty a confidence, případně omezený původní výstup.
  To je jiná cesta než checkpoint vlastní issue session.
- **Výsledek:** běhy mají samostatné `outcome` (`issue_handler_runs.go:58`).
  Handoff/checkpoint je strukturované hlášení agenta, nikoli automatický
  důkaz správnosti souboru či splnění akceptačních podmínek.

Odpověď na „předávají si validní zprávy?“: **mechanismy pro předávání existují;
úplnost, srozumitelnost a věcnou správnost každého předání zatím nelze potvrdit.**
Zejména nesjednocovat uložení zprávy, přidání do vstupu běhu, odpověď a ověření výsledku
do jediného zeleného stavu „doručeno“.

Historický report `docs/prd/reports/track-a-live-validation-2026-09-03.md`
obsahuje zkoušky mentions, deduplikace, follow-upů, restartu a lidské odpovědi.
Jeho část z 5. září uvádí jen 8 checkpointů z 21 dokončených session běhů
a omezení živého ověření. Jde o historická měření, ne dnešní statistiku.
Hlavička staršího PRD a součtové počty v reportu nejsou konzistentní s pozdějšími
dodatky; nelze z nich převzít prosté „hotovo / nehotovo“ ani release procento.

## Návrh detailu

Zachovat současnou typografii, tmavý vzhled, ikony a kompaktní levý explorer.
Zmenšit počet samostatných karet. Neskrývat důležitý obsah pouhým zkrácením textu.

```text
QUA-1  Audit dokumentace                     [Spustit práci] [⋯]
Docs Drift · Řeší Jordan · K převzetí

[Aktuální situace / konkrétní rozhodnutí, pokud existuje]
 Čeká na … / Pracuje … / Výsledek připravený k posouzení
 Poslední doložená změna · další krok

 Práce a komunikace                       Vlastnosti
 Jordan → Casey: Ověř kandidáty            Stav · priorita · termín
 Čeká ve frontě / předáno do běhu          Vlastník · řeší agent
 Casey: Výsledek ověření [otevřít]         Projekt
 Jordan: Závěrečný výstup [otevřít]
 [Zpráva…] [adresát] [Odeslat]

 Zadání [Upravit]                         Další vlastnosti ▸
 Cíl · podklady · co má vzniknout
 Podmínky dokončení · omezení

 Historie běhů a technické údaje ▸
```

Příklad komunikace výše je ilustrace navržené hierarchie, nikoli skutečná
historie QUA-1. Viditelný obsah se musí vždy odvozovat od uložených událostí.

### Obsah podle fáze

| Fáze | Hlavní obsah | Hlavní akce |
|---|---|---|
| Nezahájeno | Zadání, řešitel, známé překážky a podmínky dokončení | Spustit práci; pokud chybí agent, nejprve Vybrat agenta |
| Práce běží | Poslední doložená činnost, účastníci a navazující práce | Poslat upřesnění; zastavení stále snadno dostupné |
| Čeká ve frontě | Kdo čeká a známý důvod | Otevřít související práci; nevymýšlet odhad čekání |
| Potřebuje člověka | Konkrétní otázka a dopad odpovědi | Odpovědět / rozhodnout podle skutečného action contractu |
| K posouzení | Celý výstup nebo funkční odkaz, co bylo ověřeno, co zbývá | Přijmout výsledek / Vrátit s připomínkou podle oprávnění |
| Neúspěch či neúplný výsledek | Důvod, zachované dílčí výstupy a další možnost | Odpovídající náprava; retry jen tam, kde je podporovaný |
| Hotovo | Přijatý výsledek a kdo jej přijal | Otevřít výsledek |

„Běh skončil“ a „čeká na člověka“ mohou platit současně. REVIEW bez dostupného
výstupu nesmí dostat falešný zelený panel. `SUCCEEDED` označuje hlášení běhu;
ověření a přijetí člověkem musí mít vlastní důkaz. Průběh zobrazovat jako
skutečné kroky, případně „2 ze 3 dokončeny“, pouze pokud existuje plán;
nevymýšlet procenta podle uběhlého času či počtu zpráv.

### Společná komunikace

Jeden chronologický proud: lidské zprávy, agentní zprávy, předání, výsledky
a rozhodnutí. U předání vidět odesílatele, adresáta, krátké zadání a vazbu na běh.
Logy nástrojů, shell a tokeny ponechat pod rozbalením. Sloučit přes stabilní
identifikátory a pořadí událostí, ne heuristicky podle podobného textu; komentář
a událost o jeho vzniku se nesmí zobrazit dvakrát jako dvě různé zprávy.

Composer má rozlišovat „Přidat poznámku“ a zprávu konkrétnímu agentovi.
Při odeslání do zaneprázdněné session zobrazit „Zařazeno pro další běh“.
Uložený komentář s odmítnutým dispatchováním zůstává uložený a dostane vlastní
vysvětlení; nestačí toast „Odesláno“. Chyba načítání nesmí vypadat jako prázdná historie.

Sbalit nevyplněné vlastnosti, metadata, nepoužívanou rutinu a prázdné integrace.
Příslušné akce Připojit rutinu / Přidat odkaz zůstanou v Dalších možnostech.
Zadání standardně číst jako dokument; panel editoru zobrazit až po Upravit.
Na mobilu situace → výsledek/komunikace → zadání → vlastnosti, bez trvale
přilepeného panelu překrývajícího obsah. Animace jen jemně při nové události,
bez přesunu rozečteného textu a s respektováním reduced motion.

## Tabule Issues

Zachovat sloupce, přetahování, přepínání list/board, hledání i filtry.
Na kartě ponechat ID, název, agenta a prioritu. Řádek Created/Updated nahradit
užitečnějším signálem, pokud je dostupný: „Čeká na vás“, „Ve frontě“,
„Výsledek k posouzení“, „Blokováno“. Běžný klidný úkol nepotřebuje další badge.
Relevantní filtr „Potřebuje mě“ musí vycházet z oprávnění a nevyřešené akce,
nikoli ze všech issues ve stavu REVIEW.

Stav tabule zůstává workflow stav; aktivita a výsledek jsou doplňkové údaje.
Přesun do Done musí respektovat pravidla závislostí/podúkolů i schvalování.
U vazby BLOCKED BY se nesmí pouhé pořadí v textu zaměnit za vykonávaný plán:
kontrolovaný handler Start vztahy explicitně nekontroluje, případné další
vynucení v enginu je potřeba ověřit samostatným scénářem.

## New issue a New project

**New issue:** zachovat rychlý kompaktní dialog. Název a volné zadání, viditelný
projekt a řešitel; datum, odhad, milestone, parent, labels a rutina v „Další
možnosti“, vybrané hodnoty zůstanou viditelné. Nabídnout volitelnou šablonu
„Zadání pro agenta“: Cíl / Podklady / Požadovaný výstup / Podmínky dokončení /
Omezení. Jde nejprve o strukturu Markdownu, ne pět nových povinných polí v DB.
Vytvoření issue a spuštění práce jsou odlišné akce; případné „Vytvořit a spustit“
musí zvládnout i stav „vytvořeno, spuštění selhalo“ bez založení duplikátu.

**New project:** název, krátký cíl, odpovědná osoba; termín a plánování volitelné.
Formátování briefu až při psaní/editaci. Všech šest statusů a všechny priority
nemusí zabírat základní pohled. Milníky řešit po vytvoření projektu navazující
skutečnou akcí; současné chybějící UI vyžaduje implementaci, ne slib v textu.
Do té doby nevystavovat dominantní technickou výstrahu o endpointu.

Tlačítka New issue / New project ponechat s textem a současnými ikonami;
primární zůstane New issue. Ikona sama nevysvětlí účel lépe než krátký popisek.

## Podmínky spolehlivého předání

Předání musí doložit: kdo → komu, konkrétní úkol, vstupy, podmínky hotovo,
výsledek předchůdce, odkazy na dostupné artefakty a zbývající překážky.
Část už existuje v handoffu, část v doručení/session; jejich propojení a
kontrola dostupnosti výstupů jsou další práce. Není nutné stavět nový orchestrátor.

Pro QUA-1 se nabízí ověřovací scénář: Jordan spustí scan → Casey dostane
skutečný scan a ověří kandidáty → Jordan dostane ověřený report a odevzdá
fix-list → klient otevře celý soubor a může zkontrolovat počty. Předem ověřit
existenci přístupů a skriptu; text zadání s tvrzením „GH_TOKEN je v prostředí“
sám existenci credential neprokazuje. Tento scénář zde nebyl spuštěn.

## Doporučené pořadí implementace a přijetí

1. **Pravdivý stav a výsledek.** Zohlednit outcome, odstranit zavádějící
   automatická hlášení, zpřístupnit úplné odevzdání a původ výsledku.
   Regresní příklady OPS-7 a QUA-4: žádné falešné „hotovo“, přímá cesta
   k výsledku nebo jasně uvedená absence. Nepřepisovat historické zprávy potichu.
2. **Detail a komunikace.** Jedna pracovní plocha, účastníci a stav předání;
   připojit existující data relací/událostí, ověřit případnou potřebu nového
   čtecího kontraktu pro doručení. Zachovat task runs i issues bez sessions.
3. **Zadání a formuláře.** Volitelná šablona, méně viditelných voleb, editace
   na vyžádání a funkční pokračování s milníky.
4. **Drobné úpravy tabule.** Signály pozornosti, bez celkového redesignu.

Při implementaci projít scénáře: idle mention; follow-up za běhu; odmítnuté
doručení po úspěšném uložení komentáře; předání mezi dvěma agenty s otevřitelným
výstupem; NEEDS_HUMAN a odpověď do správné session; COMPLETED+FAILED; chybějící
výstup; restart/reload bez duplikace; nedostupný dílčí endpoint; mobil a klávesnice.
Spustit povinné repo testy/lint/build podle měněných částí. V tomto read-only
auditu se testy nespouštěly; doložené dnešní ověření jsou výše uvedené CLI čtecí
dotazy a kontrola kódu, nikoli nová end-to-end certifikace.
