# Společný brief pro oponentní agenty (PRD AWX+Omarchy)

Repozitář: /srv/crewship/crewship_3, větev `main`, HEAD `5a0ad11f` (= origin/main k 2026-09-15).
Git status: pouze untracked dokumenty v docs/prd (cizí WIP) — NIKDY je nemaž, nepřepisuj, necommituj.
Jiná session (crewship-1) pracuje v clone 1; clone 3 sdílíš s dalšími třemi oponentními agenty.

## Co NEDĚLAT (tvrdé zákazy)
- Neimplementuj produktové změny. Neuprav žádný tracked soubor repozitáře.
- Nezakládej účty, nespouštěj `dev.sh seed`, neměň ACL živých dat, nenasazuj, nemerguj, nerestartuj systemd jednotky.
- Nepiš do docs/prd — výstup jde POUZE do svého souboru ve scratchpadu (níže).
- Necommituj nic. Nepoužívej `git stash`, `git checkout .`, `git add`.
- Nespouštěj `go test ./...` ani celý vitest — jen cílené balíčky/soubory, VŽDY v popředí (ne run_in_background), s `-count=1` a rozumným `-timeout`. Cap 1 paralelní go test najednou na agenta (12 jader sdílí 4 agenti + 3 živé služby).
- Cílené reprodukce/testy: pokud potřebuješ dočasný Go test, pojmenuj ho `zz_opp_<klastr>_test.go`, po spuštění ho SMAŽ a zdrojový kód testu + výstup ulož do svého reportu. Pro TS dočasný test použij `__tests__/zz-opp-<klastr>.test.tsx` a stejně smaž. Nikdy nenech dočasný soubor v repu.
- Izolovaná instance: pokud opravdu potřebuješ živý server, použij `CREWSHIP_DATA_DIR=<scratchpad>/data` a port 127.0.0.1:81xx mimo 8080–8097 a po skončení proces zabij (nepoužívej `pkill -f` s patternem, který zasáhne tvůj vlastní shell; kill podle PID). Preferuj Go httptest / fixture testy.

## Metoda (platí pro každou funkci ve tvém klastru)
1. Co již existuje na HEAD (soubor:řádek), co vzniká v otevřeném PR (#2562 routines operator console, #2556 routines clarity, #2558 webhooks — `gh pr diff <n>` / `gh pr view <n> --json files`), co skutečně chybí.
2. Producenti dat, API endpointy (`internal/api/*.go` — vzory `"METHOD /path"`), autorizační kontroly (middleware, `internal/policy`, CASL `lib/permissions/`), UI spotřebitelé, existující testy (Go `*_test.go`, Vitest `__tests__`, Playwright `e2e/`).
3. Rozliš „malá změna rozhraní“ (čistá funkce nad autorizovanými DTO) vs „nová backendová schopnost“ (nový endpoint, migrace, autorizační cesta).
4. Přínos pro release 1.0, závislosti na jiných funkcích/PR, realistický odhad (člověkodny, s cílenými testy).
5. Nejmenší samostatně dokončitelná verze.
6. Měřitelná akceptace + nutné regresní testy (existující, které se musí držet zelené; nové, které musí vzniknout).

Označuj každé tvrzení jedním ze tří štítků: **[POTVRZENO]** (viděl jsi kód/spustil test, uveď soubor:řádek nebo výstup), **[NÁVRHOVÉ RIZIKO]** (plyne z designu, neověřeno během), **[NEOVĚŘENO]** (předpoklad). Starý report v docs/prd/reports nebo komentář v kódu NENÍ důkaz aktuálního chování — ověř na HEAD.

Připomeň si gate-y repozitáře, které implementaci ovlivní (uveď v odhadu): nový endpoint = gen-openapi + docs/openapi.mdx totals + CLI příkaz + docs-inventory; nový flag = docs/cli tabulka; CHANGELOG; CodeQL.

## Formát výstupu (Markdown, česky, věcně, bez omáčky)
Zapiš do souboru: `/tmp/claude-1000/-srv-crewship-crewship-3/a0261745-a8be-4057-9c64-1286f3bee302/scratchpad/opp-<klastr>.md`

Struktura:
```text
# Klastr <X>: <funkce>
## 0. Ověření prostředí (branch, HEAD, git status – jednou větou)
## 1. Verdikt per funkce (implementovat / upravit / odložit + proč, 2–4 věty)
## 2. Tabulka funkcí
| ID | Současný stav (HEAD) | Souběžné PR | Doporučený rozsah (min. verze) | Závislosti | Odhad | Důkaz v kódu (soubor:řádek) |
## 3. Nálezy podle závažnosti (Kritické / Vysoké / Střední / Nízké)
   každý: štítek, soubor:řádek, popis, reprodukce (příkaz + výstup nebo test), dopad na PRD
## 4. Navržené přesné změny PRD (cituj sekci PRD a navrhni znění; PRD sám NEPŘEPISUJ)
## 5. Návrh iterací pro tento klastr (vstupy, rozsah, co do ní NEPATŘÍ, testy, podmínky dokončení, handoff pro další relaci)
## 6. Rozhodnutí vyžadující produktový vstup (jen skutečná; technická rozhodni sám a zdůvodni)
## 7. Příloha: dočasné testy (zdroj + výstup), spuštěné příkazy
```

Na konci vrať v odpovědi jen: cestu k souboru + 3–5 nejdůležitějších zjištění v odrážkách.
