# Lidská brána §11 — pět úloh pro uživatele

Technická přejímka §9 je doložená v
[routines-acceptance-2026-09-11](routines-acceptance-2026-09-11.md).
**Tohle je ta část, kterou nesmí odškrtnout agent.** PRD §11 a §9 žádají, aby
uživatel prošel pět úloh **bez výkladu** a sám řekl, jestli je Edit/Test/Run
srozumitelný.

Nečtěte prosím předem protokol ani popisy níže dál, než kam potřebujete.
Smyslem je zjistit, co produkt sdělí sám.

## Kde

dev1: [crewship-dev1.unifylab.cz/routines](https://crewship-dev1.unifylab.cz/routines) — přihlášený,
workspace „Demo User's Workspace“.

Každý odkaz otevře rutinu rovnou v detailu.

## Úlohy

### 1. Vysvětlete, co rutina dělá

[crewship-dev1.unifylab.cz/routines?slug=workspace-digest](https://crewship-dev1.unifylab.cz/routines?slug=workspace-digest)

Bez spouštění: **co tahle rutina dělá, z čeho to poznáte a kolik má kroků?**
Popište to vlastními slovy.

### 2. Spusťte ji se změněným vstupem

[crewship-dev1.unifylab.cz/routines?slug=work-order-inputs-mtvp2jz6](https://crewship-dev1.unifylab.cz/routines?slug=work-order-inputs-mtvp2jz6)

Spusťte ji s **jinou hodnotou, než nabízí**. Pak najděte v historii, **jakou
hodnotu jste skutečně odeslali** — ne jakou má rutina ve výchozím stavu.

### 3. Najděte důvod selhání

[crewship-dev1.unifylab.cz/routines?slug=work-order-failure-mtvp2jz6](https://crewship-dev1.unifylab.cz/routines?slug=work-order-failure-mtvp2jz6)

Tahle rutina selže záměrně. Spusťte ji a odpovězte: **který krok selhal, proč,
a co byste udělali dál?**

### 4. Vyřešte čekající rozhodnutí

[crewship-dev1.unifylab.cz/routines?slug=work-order-decision-mtvp2jz6](https://crewship-dev1.unifylab.cz/routines?slug=work-order-decision-mtvp2jz6)

Spusťte ji. Zastaví se a bude něco chtít. **Najděte to, rozhodněte a ukažte,
kde je vidět, co jste rozhodli.** Zkuste obě cesty, kterými se k témuž
rozhodnutí dá dostat.

### 5. Změňte plán

[crewship-dev1.unifylab.cz/routines?slug=routine-playground](https://crewship-dev1.unifylab.cz/routines?slug=routine-playground)

Nastavte, aby se rutina spouštěla **každý den ve 2:30 ráno pražského času**.
Pak ukažte, **kdy poběží příště** — a jestli se to, co jste nastavili, shoduje
na obou místech, kde je plán vidět.

Až budete hotovi: plán zase zrušte, ať po sobě neuklízíme.

## Na co se ptáme

U každé úlohy: **šlo to bez ptaní?** Kde jste zaváhali? Co jste čekali jinde,
než to bylo?

A závěrem, což je vlastní obsah brány:

> **Je Edit / Test / Run srozumitelný?** Poznáte, kdy měníte návrh a kdy
> spouštíte skutečnou věc? Poznáte před spuštěním, jestli to bude mít reálný
> účinek?

## Proč to nemůže odškrtnout agent

§9 chce, aby alespoň 4 z 5 reprezentativních uživatelů dokončili každou úlohu
bez nápovědy — a poznamenává, že jde o navržený cíl, ne naměřený výsledek.
Interní walkthrough (§14, 10. 9.) proběhl a je doložený, ale je to procházka
člověka, který produkt psal. Ta otázka, kterou má brána zodpovědět, zní
„pochopí to někdo, kdo tu nebyl“, a na tu se nedá odpovědět zevnitř.

Technické dokončení §9 a přijetí celého PRD jsou proto v protokolu vedené
odděleně a tenhle dokument zůstává nezaškrtnutý, dokud na něj neodpoví
uživatel.
