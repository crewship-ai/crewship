# Pages editor a Settings — nezávislá oponentura

Datum: 10. září 2026. Oponent: Claude Opus 5.
Předmět: [návrh editoru a nastavení](pages-settings-editor-review-proposal-2026-09-10.md).
Základ kódu použitý pro ověření: `01d4849c`.

Autor návrhu na tuto oponenturu odpověděl a rozhodl tři otevřené otázky; jeho
rozhodnutí jsou zapracovaná v sekcích 4–7 a označená jako **přijato autorem**.

## 1. Verdikt

**Přijmout s podmínkami.**

Pravidla jsou profesionální. UI zatím není navržené. Návrh je nadprůměrná
specifikace *chování* a téměř mlčí o *formě*: dvě ASCII kostry na celou funkci.
Otázka „je to profi?" se rozhoduje věcmi, které v dokumentu nejsou — jak vypadá
plocha při editaci, kde je náhled vůči formuláři, co je prázdný stav, jaká je
hustota a rytmus. Text v tomto stavu nelze označit za hotový návrh rozhraní.

Podmínky přijetí jsou dvě: dotáhnout hlavní pracovní obrazovku (sekce 5) a
navrhnout běžnou panelovou Page jako plnohodnotný případ, ne jako ochuzenou
verzi Operations Lab (sekce 6).

## 2. Co jsem ověřil a co ne

**Ověřeno proti `01d4849c`:**

| Tvrzení návrhu | Výsledek |
|---|---|
| Horní lišta nabízí více samostatných vstupů současně | Potvrzeno: `App preview`, `Source history`, `Publications` jsou tři samostatná tlačítka vedle Settings a Edit |
| Dnešní `canEdit` je dokumentový gate | Potvrzeno: `canEdit = Boolean(selectedSlug) && detail.page != null && sealed === 0` — mizí při jediném zapečetěném panelu |
| Settings záměrně není omezené stejně jako editor | Potvrzeno; je to explicitně okomentované v `pages-layout.tsx` |

**Neověřeno.** `page-settings.tsx`, `use-page-sharing.ts`, `use-page-publications.ts`,
`pages_public.go` — o nich mluvím pouze podle popisu v návrhu. Neviděl jsem
uživatelův screenshot. Neposuzoval jsem přístupnost jinak než z textu F11.
Neproběhlo žádné měření použitelnosti, žádný živý průchod a žádná implementace.

## 3. Nálezy

Závažnost podle vyžádaného rozdělení: **blokátor** brání přijetí, **doporučení**
je věcná změna, **preference** je vizuální názor bez důkazu.

| ID | Závažnost | Nález | Stav |
|---|---|---|---|
| U01 | Blokátor | Editor je navržený pro člověka jako autora, ale autorem má být agent. Hlavní práce člověka je kontrola a schválení agentovy změny; F13 (porovnání) je proto chybně v P1 | **Přijato autorem**, povýšeno do P0 |
| U02 | Blokátor | Chybí návrh běžné panelové Page. Návrh je psaný z Operations Lab, tedy z výjimky; pro panelovou stránku vzniknou prázdné aplikační sekce a nikdy nedostupné publikační tlačítko | **Přijato autorem**, panelová Page dostane vlastní plnohodnotný návrh |
| U03 | Blokátor | Vnitřní rozpor: F05 řadí webhooky do Data a akce, ale nápověda CLI je definuje jako „produce grant in a different coat" — mintování credentialu patří k oprávněním | **Přijato autorem**: Access → Producer tokens; Data & actions ukáže zdroj a poslední příjem s odkazem na správu |
| U04 | Doporučení | Pět sekcí je o jednu moc. „Obecné" má čtyři pole, z toho slug jen ke kopírování — bude to poloprázdná obrazovka, což čte jako nedodělek | **Přijato autorem**: sloučeno do Content |
| U05 | Doporučení | Nahrazení levého seznamu Pages sekcemi editoru je nejrizikovější rozhodnutí návrhu a je odbyté jednou větou; slib obnovy pozice je přesně to, co se rozbije | **Přijato autorem**: seznam na desktopu zůstává, editor nahradí jen obsah stránky |
| U06 | Doporučení | Dva ambientní stavové sloty plus stav ukládání u každého formuláře. Návrh sám správně říká, že čerstvost dat, běh aplikace a existence konceptu jsou tři různé věci, a pak si otevírá prostor je zase slít | **Přijato autorem**: stavy se přesouvají k příslušným akcím |
| U07 | Doporučení | Deset z jedenácti kritérií měří korektnost, jedno použitelnost. Kritérium 1 navíc měří *odstranění* tlačítek — dá se splnit a vyrobit horší produkt | Autor nepřijal poměr půl na půl; **shodli jsme se**, že chybí konkrétní úkoly a jejich měření (sekce 8) |
| U08 | Preference | `/pages?slug=…&mode=edit&section=access` funguje, ale `/pages/operations-lab/edit/access` je to, co člověk čeká, že vloží do Slacku | Autor nepřijal jako nutnou migraci; **souhlasím** — rozhodující je spolehlivé sdílení odkazu, reload a Back/Forward |
| U09 | Preference | Dokument specifikuje české popisky; „Obsah a vzhled" → „Content & appearance" se sráží s existujícím „Pages appearance" v nastavení workspace | **Přijato autorem**: Content / Data & actions / Access / History |
| U10 | Doporučení | Obrazovka kontroly implikuje verdikt, ale neexistuje cesta „zamítnout / vrátit autorovi". V v1 je legitimní odpovědí „publikuj, nebo nepublikuj", UI ale nesmí naznačovat workflow, který nemá | **Nový**, viz sekce 5 |

## 4. Opravená navigace a primární akce

Zapracovaná rozhodnutí autora. Levý seznam Pages zůstává; editor obsazuje pouze
obsahovou plochu.

```text
┌ Pages ──────────┬─────────────────────────────────────────────────────┐
│ Operations Lab  │ ← Zpět na stránku   Operations Lab                  │
│ Fleet 201       │ ─────────────────────────────────────────────────── │
│ Uzávěrka        │ [Content] [Data & actions] [Access] [History]       │
│ …               │ ─────────────────────────────────────────────────── │
│                 │  obsah sekce                                        │
│                 │                                                     │
│                 │                        [primární akce této sekce]   │
└─────────────────┴─────────────────────────────────────────────────────┘
```

Primární akce se mění podle sekce a odpovídá skutečnému životnímu cyklu:

| Sekce | Primární akce | Co se stane |
|---|---|---|
| Content — panely | Uložit změny | Zapíše definici; ostatní ji uvidí po zápisu |
| Content — vlastní aplikace | Uložit koncept | Nemění publikovanou aplikaci |
| Data & actions | Uložit změny | Zapíše definici panelu/akce |
| Access | Přidat přístup / Vytvořit token / Odvolat | Samostatná změna, není součástí publikace |
| History | Obnovit do konceptu | Vytvoří nový koncept, nepublikuje |
| Kontrola změn (sekce 5) | Publikovat aplikaci | Přepne živou verzi po serverové kontrole |

Na mobilu se seznam Pages schová standardním způsobem; sekce jsou pojmenovaný
výběr, ne vodorovná řada čtyř tabů. Hlavička vždy nese název stránky a návrat.

## 5. Hlavní pracovní obrazovka: kontrola agentovy změny

Tohle je obrazovka, kterou návrh nemá, a je to ta, u které člověk stráví nejvíc
času. Otevře se jako první, když má Page vlastní aplikaci a existuje koncept
novější než živá publikace.

```text
← Zpět     Operations Lab · kontrola změn                    koncept 7
─────────────────────────────────────────────────────────────────────────
Uložil agent ops-writer (crew Ops) · 12:03 · proti živé verzi 3
─────────────────────────────────────────────────────────────────────────
CO SE ZMĚNÍ PRO UŽIVATELE
  • Přibude tlačítko „Restartovat kolektor" (routine ops-restart)
  • Panel „Memory" zmizí ze stránky
  ⚠ Definice routine ops-restart se od verze 3 změnila
─────────────────────────────────────────────────────────────────────────
ZMĚNĚNÉ SOUBORY            │  src/App.tsx
  M src/App.tsx     +42 −8 │  ── zdrojový diff vybraného souboru ──
  A src/Restart.tsx    +61 │
  D src/Memory.tsx     −34 │
                           │
DEFINICE                   │
  + akce restart (panel ops)
  − panel memory
─────────────────────────────────────────────────────────────────────────
[Sestavit náhled]  [Otevřít náhled]  ☐ Zkontroloval jsem kód a chování
                                              [Publikovat aplikaci]
```

Závazná pravidla té obrazovky:

1. **„Co se změní pro uživatele" je odvozené, ne psané autorem.** Vzniká
   porovnáním definic: přidané a odebrané panely, přidané a odebrané akce,
   změna cílové routine. Agent do toho textu nesmí psát; jinak je to marketing,
   ne kontrola. Souhlasím s autorem, že samotný textový diff nestačí — ale
   opačný extrém, tedy shrnutí od téhož agenta, je horší než žádné.
2. **Varování o změně routine patří sem**, ne až do potvrzovacího dialogu akce.
   Publikace připíná UI artefakt a deklaraci, ne implementaci routine; člověk to
   musí vidět ve chvíli, kdy schvaluje, a ne až když někdo zmáčkne tlačítko.
3. **Identita autora je skutečná data**, ne dekorace: revize nesou crew/agent
   snapshot. Zobrazit ho.
4. **Souhlas platí pro jeden konkrétní build.** Nové zdroje nebo nový build
   odškrtnou checkbox a schování „Publikovat" musí být viditelné, ne tiché.
5. **U10 — chybějící zamítnutí.** Dnes existuje jen „publikuj / nepublikuj";
   kanál zpět k agentovi je chat. UI to musí přiznat, ne předstírat review
   workflow. Minimum pro v1: vedle checkboxu věta „Nesouhlasíš? Napiš agentovi
   v chatu — tato obrazovka mu zprávu neposílá." Tlačítko „Zamítnout" bez
   příjemce nedělat.
6. Pokud koncept neexistuje nebo je shodný s živou verzí, obrazovka se
   neotevírá — jde se rovnou do Content.

## 6. Běžná panelová Page

Většina stránek nemá vlastní aplikaci. Pro ně platí:

- Sekce jsou tytéž čtyři, ale **nikde se neobjeví aplikační obsah** — žádná
  prázdná záložka, žádné trvale zašedlé „Publikovat aplikaci", žádná polovina
  Historie bez obsahu.
- V Content je na konci **jedna explicitní nabídka** „Přidat vlastní aplikaci"
  s popisem, co to znamená (agent, build profil, publikace). Volitelná schopnost
  navíc, ne chybějící část.
- Historie panelové Page ukazuje jen historii definice. Zdrojová historie a
  publikace se objeví teprve tehdy, když projekt existuje.

Tohle je test profesionality celého návrhu: běžný případ musí vypadat celistvě,
ne jako ukázka s vypnutými funkcemi.

## 7. Access a Producer tokens

Podle rozhodnutí autora. Access obsahuje tři podsekce: kdo čte, kdo dodává data
(**Producer tokens** včetně webhooků), a veřejné odkazy. Data & actions ukazuje
u panelu zdroj dat, čas posledního přijetí a odkaz „spravovat přístup", nikoli
formulář na vydání tokenu.

Dvě vlastnosti, které UI nesmí zamlčet, protože je backend skutečně má: token je
vázaný na **jeden panel**, a **nenese vlastní oprávnění** — server při každém
požadavku znovu odvodí, co smí ten člověk právě teď. Druhá věta patří k seznamu
tokenů, protože mění to, co si správce myslí, že musí hlídat.

Nový secret se ukazuje jednou; formulář ho nesmí umět zobrazit znovu ani ho
obnovit z historie formuláře.

## 8. Doplnění kritérií přijetí

Nepožaduji poměr půl na půl. Požaduji, aby existoval **měřený úkol** ke každé ze
tří hlavních cest. Návrh má dnes jen kritérium 11.

| Úkol | Měření |
|---|---|
| „Agent ti upravil stránku. Rozhodni, jestli to pustíš živě." | Čas do rozhodnutí; kolik lidí správně pojmenuje, co se změní pro uživatele; kolik si všimne změněné routine |
| „Dej Petrovi právo posílat data do panelu Services." | Čas do dokončení; kolik lidí omylem vytvoří veřejný odkaz místo produce grantu |
| „Vrať stránku do stavu z včerejška." | Kolik lidí správně rozliší obnovení konceptu od publikace staršího artefaktu |

Ke kritériu 1 návrhu: doplnit, že každá odstraněná cesta má ověřenou náhradu —
měřit náhradu, ne zmizení tlačítka.

## 9. Co musí obsahovat další revize

Souhlasím s autorem, že jinak budeme znovu posuzovat text místo rozhraní.
Další revize je posouditelná teprve tehdy, když obsahuje konkrétní obrazovky:

1. Běžná panelová Page v režimu úprav (sekce 6).
2. Kontrola agentovy změny (sekce 5), včetně stavu „build neproběhl" a
   „kompilace selhala".
3. Access s Producer tokens a jednorázovým secretem.
4. Mobilní varianta obou hlavních obrazovek při 360 px.
5. Prázdné a chybové stavy pro F10 — ne jako seznam, ale jako plochy.

Bez nich nelze říct, jestli je výsledek profesionální; lze říct jen to, co říká
tato oponentura dnes: pravidla ano, rozhraní zatím nevíme.
