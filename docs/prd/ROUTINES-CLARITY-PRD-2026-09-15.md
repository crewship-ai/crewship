# Routines: vstupy, pravidla práce a řešení problémů

Datum: 2026-09-15. Stav: implementace, nepřijato člověkem.
Issue: #2555. Jedna větev `feat/routines-clarity-release-1`, jeden PR, testování na dev1.
Navazuje na [hlavní PRD](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md).
Historické opravy #2553 jsou uzavřené podle [protokolu po mergi](https://github.com/crewship-ai/crewship/pull/2553#issuecomment-5672592832).
Tato práce neuzavírá původní lidskou přejímku §11.

## Problém a výsledek

Uživatel má před Run rozumět požadovaným vstupům, vykonavatelům, povinným
kontrolám a pravidlům při problému. Po spuštění má najít překážku, zachované
výsledky a časově náročné nebo opakované provedení. Zachovat stávající explorer,
barvy, navigaci a společný seznam kroků; nepřidávat další editor nebo dashboard.

## Rozsah a akceptace

| ID | Změna | Ověřitelná podmínka |
|---|---|---|
| C1 | Formulář ukáže typ, meze a příklad formátu; chyby u polí | 0.7 projde jako number, desetinné integer a překročené meze neodešlou Run. Chyba je dostupná klávesnicí a čtečkou. |
| C2 | Výchozí versus změněné hodnoty, obnova defaults | 0/false zůstanou hodnotami. Reset obnoví skutečný default receptu, nikoli historický vstup nebo preset. |
| C3 | Explicitní formát absolute_path | Nově deklarovaný formát se validuje na klientu i serveru; relativní cesta a traversal jsou odmítnuté. Kontrola formátu neslibuje existenci ani oprávnění k souboru. |
| C4 | Přehled pravidel a kontrol u receptu | Server odvodí pravidla ze stejné definice a helperů jako engine. UI pojmenuje povinné/advisory/chybějící kontroly, čekání a deklarované limity. Žádné AI generování ani odvozování kontrol z názvu. |
| C5 | Srozumitelná obsluha problému | Běh ukáže konkrétní překážku, navigaci k zaznamenaným výsledkům a pravdivý další krok. Cancelled, waiting a failed se nezamění; nové spuštění není resume. |
| C6 | Čas a další pokusy | Nejdelší dokončené provedení a opakované pokusy z již načtených dat. Částečná historie se výslovně označí; nesčítat rodiče a děti jako celkové trvání. |
| C7 | max_iterations | Explicitní kladná hodnota omezí počet worker/grader stupňů. Nula/absence zachová dosavadní fallback řetězec. Žádný nový nekonečný či skrytý opakovací mechanismus. Chování je popsáno i regresně ověřeno. |

## Kontrakty a kompatibilita

- Schéma je zdroj pravdy. Min/Max a explicitní formát optují vstup do serverové
  validace i bez widget. Staré type-only vstupy zachovávají legacy kontrakt;
  formulář nepředstírá, že starší definice vynucuje všechna pravidla na serveru.
- Podporovat stávající string/integer/number/boolean/object/array a výběry.
  Desetinná tečka je výslovná; nezavádět implicitní převod procent nebo čárky.
- Absolute path je pouze lexikální kontrakt. Živá existence souborů a autorizované
  pickery potřebují vlastní zdrojový kontrakt a nejsou výsledkem této validace.
- U běhu číst zachycenou definici, nikdy nenahrazovat nečitelný archiv aktuálním
  receptem. Popis pravidel archivované definice není audit tehdejší politiky crew.
- Pravidla autonomie se touto změnou nemění. Souhrn neslibuje univerzální účinky,
  exactly-once ani věcnou správnost modelových odpovědí.
- Žádná DB migrace: aditivní pole definice a odvozené odpovědi existujících API.

## Ověření a dodání

Cílené frontend/Go regrese, skutečné API odmítnutí před spuštěním, browser na
1440px a 390px, plný Go test/vet, frontend lint/build/test types a CI.
Jeden PR s konkrétním review, identitou nasazení dev1 a stavem každé podmínky.
Původní WIP se zachová. Nevytvářet reálné externí účinky při browser přejímce.

## Mimo tento inkrement

Nové profily autonomie/vytrvalosti, libovolný breakpoint/resume, nové integrační
platformy, automatické cachování výsledků agentů a změny paralelismu. Měření
nejpomalejšího kroku není příslib rychlejší exekuce; čas šetří především včasné
odmítnutí neplatného vstupu a snazší diagnostika.
