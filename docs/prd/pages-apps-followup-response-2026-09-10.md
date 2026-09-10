# Pages Apps — ověření druhé oponentury, 10. 9. 2026

Podklad: `pages-apps-independent-review-followup-2026-09-10.md` dodaný uživatelem.
Odkazovaný Claude artifact nebyl dostupný; ověřeno proti místnímu plnému textu,
aktuálnímu kódu a GitHub Actions, nikoli pouze proti `gh pr checks`.

## N1: chybějící review ano, chybějící CI ne

Automatický trigger `ci.yml` opravdu omezuje PR na base `main`. To ale není
úplný seznam běhů: stack byl ověřován přes `workflow_dispatch`.

| Vrstva | Commit | Skutečný plný běh | Výsledek |
|---|---|---|---|
| server | `70068341` | [34378372215](https://github.com/crewship-ai/crewship/actions/runs/34378372215) | success |
| UI včetně serveru a CLI | `01c0e9fa` | [34378375514](https://github.com/crewship-ai/crewship/actions/runs/34378375514) | success |
| source po opravě nestabilních testů | `04bf425e` | [34459514236](https://github.com/crewship-ai/crewship/actions/runs/34459514236) | success |
| celý UI stack po opravě testů | `6af120b2` | [34459629052](https://github.com/crewship-ai/crewship/actions/runs/34459629052) | success |

Poslední dva běhy jsou nyní dokončené včetně Go/race. Tvrzení, že 203 souborů
nemá žádné CI a širší API/MCP/SDK lane se nespustila, tedy neplatí.
Zelený běh ovšem není důkaz pokrytí každého jednotlivého souboru.
Tyto běhy předcházejí níže popsaným novým opravám N2/N3.

Review zůstává skutečným blokátorem. Zdrojový PR má starší schválení před
posledními změnami; čtyři další PR nemají skutečné review. Žádný PR nebyl mergnut.
Předchozí předání tuto neúplnost uvádí výslovně. Správný další postup zůstává
review a CI konkrétního commitu, merge od základu a explicitní ověření/změna base
následujícího PR na main. Nelze spoléhat na automatické přebázování ani na zelené
label/surface checky. Zachovat ancestry, kontrolovat rozsah diffu a changelog.

## N2: potvrzeno a opraveno

Setter nyní před zápisem jediným dávkovým `git cat-file --batch-check` ověří,
že všechny deduplikované identifikátory existují a mají typ `commit`. Validace
proběhne před změnou souboru `shallow` nebo jeho locku. Nevzniká proces pro každý
checkpoint. Regrese ověřuje chybějící objekt i existující tree, zachování původní
hranice, čitelnost checkpointu a úspěšný `git fsck` po odmítnutí.

Tvrzení, že každá neexistující shallow hranice znemožní každý Git příkaz, zde
není potřebné ani samostatně prokázané; chybějící validace je chyba sama o sobě.

## N3: potvrzeno a opraveno

Při explicitním nesouladu verzí a dosud neotevřené aplikaci se okamžitě zobrazí
panely s vysvětlením. Spinner tedy nečeká neomezeně a starý artefakt se nespustí.
Když následná metadata souhlasí, aplikace se může otevřít. Regrese ověřuje celý
přechod nesoulad → panely → souhlas → správná aplikace, včetně absence spinneru.

## N4 / R8: konkrétní tvrzení vyvráceno

Obecný přenositelný source codec není jedinou validací save endpointu.
`PutProject` volá `pageprofile.ValidateSourcePaths` ještě před změnou draftu;
profil odmítá ne-ASCII názvy. Přímý API regresní test nyní doložil, že uložení
`src/čísla.tsx` vrátí 422 s názvem souboru a nezmění revizi. Následující platné
uložení s očekávanou revizí 0 projde. Test nevyžaduje spuštění compileru.
Toto vyvrací konkrétní N4, nikoli tvrzení, že libovolný zdroj přijatý codec-em
musí být automaticky kompilovatelný.

## Safari je otevřené produktové rozhodnutí

Současná politika zůstává desktop Chromium pro aplikace, ostatní enginy pro
panely. Doporučuji ji pro v1 potvrdit. Dřívější vykreslení aplikace v Safari není
měřením stejné procesové izolace nebo schopnosti zastavit smyčku. Uživateli byla
předložena volba; politika se bez odpovědi nerozšiřuje.

Nové ověření: API test N4 prošel; test N2 prošel; komponentové testy 7/7 a lint
prošly. Širší výběr Pages UI: 46 souborů / 581 testů prošlo (jiný filtr než
předchozí souhrn 598). Celé Go balíčky `pages` (2,745 s), `pagebuild` (0,084 s) a `backup` (145,498 s)
prošly, stejně jako `go vet` pro změněný core a backup. Produkční static build
prošel. Na dev3 je nasazen čistý commit `078f3381`, SHA256
`e2e9ec33735e89002038918191a66922f54b2e1aa94e480e897f0385d85fcc7b`.
Živý Chromium test s podvrženou verzí pouze v odpovědi testovacího prohlížeče
potvrdil viditelné panely, nulový počet aplikačních iframe a absenci spinneru.
`fsck`: 3 zdroje, 3 checkpointy, 3 artefakty, 26 Git objektů, bez chyb.

Nové CI: [source 34469301690](https://github.com/crewship-ai/crewship/actions/runs/34469301690)
a ručně spuštěný [UI stack 34469677510](https://github.com/crewship-ai/crewship/actions/runs/34469677510).
Běhy zatím probíhají. Automatické review source bylo opět odmítnuto limitem;
bot v 11:04 UTC uvedl další slot za 7 minut. Cílený `scripts/review-status.sh --retrigger 2475` byl odeslán v 11:12:28 UTC;
služba jej v 11:12:34 znovu odmítla (`Review rate limited`) a uvedla dalších
59 minut. Nové skutečné review je stále nutné. Žádný merge neproběhl.
