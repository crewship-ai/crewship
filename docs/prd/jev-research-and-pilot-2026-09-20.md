# Jev / TypeSafe: rešerše a pilot pro Crewship

Stav k 20. 9. 2026. Issue [#2629](https://github.com/crewship-ai/crewship/issues/2629).
Výchozí revize Crewshipu: `8a1ca5fc3`. Implementace: `feat/jev-decisions`.

## Doporučení

**Jev má pro Crewship smysl jako levná vrstva sémantických rozhodnutí před dražším agentem.** Nejzajímavější jsou třídění příchozí práce, výběr relevantních pasáží a výběr skillů. Velké úspory vzniknou, když zabrání zbytečnému spuštění drahého agenta nebo omezí jeho kontext. Samotná výměna jednoho malého klasifikátoru pravděpodobně přinese menší absolutní úsporu.

Doporučuji nasazovat postupně: měření na vlastních případech → doporučení souběžně s dosavadním rozhodováním → automatizace ověřeného, vratného podmnožinového případu. **Nynější výstup je implementovaný CLI pilot, nikoli zapnutá produkční funkce.** Žádné naměřené zlepšení kvality nebo úsporu v Crewshipu zatím netvrdím.

## Co to vlastně je

TypeSafe uvedl Jev 15. září. Výrobce popisuje jinou architekturu, paralelní vyhodnocování a trénink RLCD; slibuje rychlá typovaná rozhodnutí místo generování textu. Startovní marketing uvádí až 193,6× rychlejší a 444,6× levnější workflow. Čísla pocházejí z jeho vlastních příkladů, nikoli z obecného benchmarku všech AI úloh. Samotný výrobce přiznává výhodný krátký vstup v demonstraci a to, že nejvyšší násobky jsou horní okraj očekávaných přínosů. [Úvodní článek](https://typesafe.ai/blog/introducing-system-one-models-and-jev).

API přijímá společný `state` a několik nezávislých otázek. `Noul` vrací pravděpodobnost ano, `Choice` výběr z konečné množiny a celé rozdělení, `Score` očekávanou hodnotu na zadané škále. Jev nepíše odpovědi uživateli, nové SQL, skripty ani volný text. Nahradit coding model v agentovi prostou změnou názvu tedy nelze. Otázky je nutné skládat a jejich výsledky kombinovat v kódu. [Úvod dokumentace](https://docs.typesafe.ai/introduction).

Přesná smlouva: TypeSafe `POST /v1/systemone`; Choice maximálně 255 možností; Score 2–10 úrovní, indexovaných od nuly. ID otázky není součástí jejího významu pro model: odkaz na konkrétní pasáž musí být v instrukci, nestačí nazvat otázku `passage_7`. [API reference](https://docs.typesafe.ai/api).

## Aktuální provozní parametry

| Vlastnost | Ověřený veřejný stav |
|---|---|
| Přímé API | `https://api.typesafe.ai/v1/systemone` |
| Pevný model TypeSafe | `jev-1.13.0` |
| Alias | `jev-latest`, může změnit cílovou verzi |
| Cena TypeSafe | $0,042 / milion vstupních tokenů; výstup zdarma |
| Kontext TypeSafe | 64k celý požadavek; 32k stav + nejdelší otázka |
| Vstupy | Text/JSON, bez přímého obrazu, audia či videa |
| Veřejné limity | 1 200 požadavků/min a 250 000 tokenů/s; výrobce upozorňuje na proměnlivost |
| Jazyky | Nejsilnější angličtina; češtinu musíme změřit samostatně |
| Přizpůsobení | Instrukce, kritéria, rozklad; bez zákaznického fine-tuningu/LoRA |

Zdroj: [modelová dokumentace](https://docs.typesafe.ai/models). Jde o momentální ceník a limity, nikoli SLA.

OpenRouter používá `typesafe/jev-1.13`, cenu $0,042 / milion vstupních tokenů a uvádí kontext 32k. Karta při kontrole ukazovala p50 přibližně 0,25 s; je to jeho průběžná telemetrie, nikoli naše měření ze serveru Crewshipu. Uživatelský odkaz `~typesafe/jev-latest` je přesměrovávající alias; pro experiment používám verzi. [Karta modelu](https://openrouter.ai/typesafe/jev-1.13), [alias](https://openrouter.ai/~typesafe/jev-latest).

Důležitá integrační odlišnost: OpenRouter nabízí **`POST /api/alpha/decisions`**, ne běžné chat completions. Ověřeno přímo v [oficiálním SDK](https://github.com/OpenRouterTeam/typescript-sdk/blob/1f6a0db9e21d77b75c21d820afb43591443f4328/src/funcs/alphaDecisionsCreate.ts). Tělo obsahuje `model`, `state`, `questions`; odpověď `answers`, `model`, `usage`. Cena v `usage.cost` je volitelná. Chybějící cena není nula. Při průzkumu běžného `/api/v1/models` se Jev neobjevil; běžné objevování chatovacích modelů proto není spolehlivým důkazem dostupnosti rozhodovacího endpointu.

Popularita je doložitelná: [OpenRouter rankings](https://openrouter.ai/rankings) jej při kontrole uváděly mezi novými trendujícími modely s 245 miliardami tokenů za zobrazené týdenní okno. Objem tokenů však nic neříká o správnosti rozhodnutí ani o počtu zákazníků.

## Benchmarky: co je skutečně doložené

Rozlišoval jsem originální experimenty od přebírání čísel. Následující výsledky publikovali jejich autoři; v této relaci jsem jejich placené běhy nereprodukoval. Konkrétní Git revize a SHA-256 vybraných podkladů jsou v [source-provenance.json](reports/jev-2026-09-20/source-provenance.json). U tak mladého ekosystému se přehledy mění i během jednoho dne.

### 1. Firemní workflow evaly: zajímavý směr, omezený důkaz

Čtyři workflow: bezpečnostní incidenty, hodnocení agentních stop, faktury a zákaznický servis. Referenční odpovědi vznikají konsenzem GPT-6 Astra a Claude Fable 5.1 při vysokém reasoning. Ostatní konfigurace běží s výchozím reasoning poskytovatele. Měří se shoda s tímto konsenzem za předpokladu správného workflow. Není to totéž jako správnost proti nezávisle anotované realitě. Je užitečné, že porovnávají rozložené workflow s jedním velkým promptem; násobky nelze převést na slib stejného zrychlení Crewshipu. [Původní metodika](https://evals.typesafe.ai/).

### 2. JevBench: dobré jednoduché úsudky, slabší těžké úlohy

Veřejný komunitní benchmark má v aktuální řadě 534 rozhodnutí, včetně 220 těžkých. Výsledky Jev: easy 100 %, standard 99,0 %, judge 94,5 %, hard 74,1 %. Latence p50 0,65 s / p95 0,72 s v příslušném sériovém běhu; odvozená cena $0,0399 / 1 000 rozhodnutí. Pro srovnání GPT-5.6 Luna low dosáhl hard 94,5 %, p50 0,97 s; Gemini 3.1 Flash-Lite hard 75,0 %, p50 0,76 s.

To vyvrací jednoduché „levnější frontier model na všechno“. Celkové skóre kombinuje kvalitu, kalibraci, rychlost a cenu; vysoké pořadí neznamená nejvyšší přesnost. Podstatná omezení: částečně syntetické úlohy, část neveřejná, měření z jednoho místa, u některých lokálních konkurentů uměle upravená latence. [Původní výsledky v1.2.3](https://github.com/fstandhartinger/jevbench/blob/9194081409fd129282123b3990a6f61b6bdf318c/RESULTS-v1.2.md).

### 3. Veřejná klasifikační data: záleží na počtu a jednoznačnosti kategorií

Aman Kumar testoval čtyři veřejné sady po 300 vzorcích a vlastní pipeline. Jev: Enron spam 98,7 %, SST-2 95,7 %, AG News 91,3 %, Banking77 76,0 %. GPT-5.6 Luna na Banking77 81,7 %. Latence Jev p50 0,8–0,9 s při 20 souběžných voláních. Na mnoha jednoduchých úlohách obstál, ale u 77 podobných kategorií ztrácel. Autor zveřejnil i per-item výsledky.

Pro Crewship: vhodnější je výběr mezi několika jasnými rolemi než jeden obří seznam všech agentů, skillů a akcí. Výsledky při vysoké důvěře vždy číst společně s pokrytím; vyřazením těžkých případů zvedneme přesnost téměř každému klasifikátoru. [Původní experiment](https://amankumar.ai/blogs/jev-measured).

### 4. Phishing: nejdůležitější negativní výsledek

Na 2 000 e-mailech měl přímý verdikt Jev 62,6 % proti Haiku 4.5 s 81,3 %. Jev byl rychlejší: p50 239 ms proti 687 ms z Francie. Rozklad na pět signálů a následná regrese zlepšil Jev výrazně: na oddělené testovací polovině 95,0 %. Stejně rozložený Haiku 93,2 %, rozdíl nebyl statisticky průkazný; jednoduchá regex baseline dosáhla 91,8 %. Pro rozložené signály autor uvádí přibližně 27× nižší cenu a 5× vyšší rychlost Jev.

Význam: nechat model pozorovat jednotlivé rysy a rozhodnutí skládat v kódu. Ani vysoká přesnost na této sadě není důvod nahradit Keeper nebo Harbormaster; dataset je zčásti snadno oddělitelný už podle konstrukce URL. [Experiment včetně dodatečných kontrol](https://github.com/anisselbd/jev-phishing-bench/blob/8093a0ee219972cda1aa6e568d408f3531b1ceb3/README.md).

### 5. Reranking: velmi konkrétní příležitost pro paměť

Na osmi anglických datasetech, 1 617 hodnocených dotazech a stejných 30 BM25 kandidátech: Jev se čtyřstupňovou škálou nDCG@10 0,692 proti Cohere Pro 0,691; rozdíl nemá prokázaného vítěze. Průměr datasetových mediánů latence 422 ms proti 844 ms, odvozená cena $0,45 proti $2,51 za 1 000 dotazů. Při vážení podle dotazů místo datasetů už vede Cohere. Samostatné skórování každé dvojice bylo výrazně pomalejší než společný stav s více otázkami.

Pro Crewship je to slibnější než nahrazování plánovače. Zůstává nutné ověřit dlouhé pasáže, češtinu a konkrétní paměť. [Původní rerank benchmark](https://github.com/anessbelbati/jev-rerank-bench/blob/cd9a35b22aeb4187334f7018a0ee1960a7470586/README.md).

### 6. Kalibrace: jedno číslo nestačí

PrimeLine uvádí výrazný rozdíl podle typu otázky. Na stejných 600 sentimentových případech mělo ano/ne ECE 0,033, zatímco Score 0,254. Na třídění commitů Jev 65,7 % proti Haiku 54,8 %, na ukládání znalostí naopak prohrál. Autor výslovně opravuje původně nespravedlivé srovnání Jev po abstenci s Haiku na celé sadě. [Původní předregistrované testy](https://primeline.cc/blog/typesafe-jev-pre-registered-test).

`confidence` u Choice/Score je podle TypeSafe statistika odvozená z rozdělení, nikoli univerzální pravděpodobnost správnosti. Noul samostatné confidence vůbec nemá. Prahy je třeba ověřit pro konkrétní rubriku a verzi; 0,9 v pilotu je pracovní nastavení, ne 90% garance. [Dokumentace confidence](https://docs.typesafe.ai/confidence).

## Kde jsou hranice

Ve zkoumaných oficiálních podkladech jsem nenašel veřejné váhy ani reprodukovatelný trénovací postup; pilot proto používá vzdálené API. Dostupné ukázky a komunitní napodobeniny nelze považovat za otevřenou verzi Jev. Běžný coding benchmark také neodpovídá jeho rozhraní: model neumí generovat opravu programu. Pro Crewship jsou rozhodovací a retrieval benchmarky přímo relevantnější.

„Nemůže halucinovat“ je zavádějící, pokud tím myslíme pravdivost. I dokonale typově správná odpověď může vybrat špatného agenta nebo nesprávně označit nebezpečnou operaci za bezpečnou. Výrobce veřejně popisuje slabiny: počítání, přesná čísla, porovnávání dat, víceúrovňové odvozování, dlouhý nerelevantní kontext, doslovné čtení a adversariální obsah. Upozorňuje i na rozpory mezi nezávislými otázkami. [Seznam omezení Jev 1.13](https://docs.typesafe.ai/model-jaggedness/jev-1.13).

Pro náš návrh z toho plyne: peníze, datumy, závislosti, scope workspace a oprávnění ověřuje kód; z modelu přichází pouze sémantické signály. Ztracené nebo nevalidní odpovědi nesmějí v Go spadnout na nulové hodnoty a být vyhodnoceny jako jisté „ne“.

## Mapa přínosů pro skutečný Crewship

Současná triage používá `contains`, `regex`, `exact` a aplikuje pravidla nad titulem issue: `internal/api/triage_handler.go:143`, `:421`, `:517`. Paměť má FTS5/BM25 (`internal/memory/search.go:34`) a hybridní RRF (`internal/memory/hybrid.go:97`). Auxiliary modely očekávají generativní modelové providery (`internal/llm/aux.go:32`); nelze jim bez úpravy smlouvy podstrčit Jev.

| Priorita | Použití | Navržená mechanika | Metrika rozhodující o nasazení |
|---|---|---|---|
| P1 | Triage příchozí práce | Explicitní pravidla první; pro nezachycené případy oblast + nezávislé signály; nejistota na dosavadní cestu | Přesnost návrhů při daném pokrytí, omyly zvlášť v češtině |
| P1 | Rerank paměti | Oprávnění a retrieval v kódu → max. 30 kandidátů → společný stav a otázka na každou pasáž | Recall@k / nDCG@k, p95 navíc, velikost kontextu a kvalita finální odpovědi |
| P2 | Výběr skillu | Katalog dostupných skillů, otázka „je nějaký potřeba?“ + výběr včetně žádného | Úspěšné použití skillu, falešná aktivace, tokeny ušetřené v instrukcích |
| P2 | Směrování mezi levným a silným agentem | Jednoduché signály o typu požadavku; rozhodnutí mimo LLM; eskalace nejasností | Celková cena dokončeného úkolu při stejné kvalitě |
| P2 | Hodnocení výsledku běhu | Konkrétní důkazy o splnění požadavků, po deterministických testech | Neshody s lidským verdiktem, falešné „hotovo“ |
| P3 | Deduplikace / konsolidace | Z kandidátních dvojic vyhodnotit podobnost a rozpor; bez automatického mazání | Chybné slučování a zachování novější opravy |
| P3 | Bezpečnostní signály | Pouze dodatečný signál pro eskalaci | Zachycené problémy i falešné poplachy; nesmí oslabit existující gate |

Záměrně nedávám Jev do každé cesty. Regex je na přesné pravidlo rychlejší a levnější, kontrola oprávnění musí být deterministická, plánování vyžaduje návazné uvažování. Výhodu odemyká správně zvolená četná sémantická mikroúloha.

## Co bylo implementováno

- `internal/decisions/client.go`: společný Go klient TypeSafe/OpenRouter; Noul, Choice, Score; oddělené rozhraní `Evaluator`.
- `internal/decisions/recipes.go`: čtyři paralelní otázky pro triage; rerank do 30 kandidátů jedním voláním; původní ID a pořadí při shodě se zachovávají.
- `cmd/crewship/cmd_decisions.go`: `crewship decisions evaluate|triage|rerank`; vstup ze stdin nebo souboru, JSON výstup, `--dry-run`, provider/model/timeout/threshold.
- `scripts/jev-eval/`: 24 syntetických případů EN/CS, nejednoznačnost, negace, destruktivní záměr a vložené instrukce; samostatný příklad rerankingu; měřicí skript používající skutečný CLI klient.

Klient nepřidává závislosti. Odmítá redirect, omezuje velikost vstupu/výstupu, respektuje kontext a timeout, nedělá automatické retry. Ověřuje přítomnost všech odpovědí, typy, konečné pravděpodobnosti v rozsahu, úplnou množinu voleb, součet rozdělení a konzistenci score. Chybové odpovědi poskytovatele se nevypisují, protože mohou obsahovat vstup nebo credential. Nulová pravděpodobnost se rozlišuje od chybějícího pole.

Pilot **nic nepřiřazuje, nespouští ani nemaže**. `needs_review=false` je pouze návrh dostatečně jisté kategorie podle experimentálního prahu, ne souhlas s provedením akce. Rerank neodstraňuje pasáže. Ke cloudu odchází pouze explicitně dodaný obsah; pilot si sám nečte soukromou paměť. Platby jdou přímo přes klíč v prostředí a **zatím se nezapisují do serverového Paymasteru**. Před zapojením do běžícího serveru jsou nezbytné workspace-scoped credentials, rozpočet, journal a přepínač režimu; to v této implementaci není.

Lokální limity 96 KiB vstup / 64 otázek jsou ochrana experimentu, ne záruka tokenového limitu poskytovatele. Rozhraní záměrně podporuje textové instrukce a textové Score úrovně; neimplementuje všechny vnořené formy instrukcí SDK.

## Reprodukce

Z kořene tohoto worktree sestavit CLI:

```bash
go build -ldflags "$(scripts/build-stamp.sh ldflags)" -o /tmp/crewship-jev-pilot ./cmd/crewship
printf '%s' 'V mobilní navigaci není vidět zavírací tlačítko.' \
  | /tmp/crewship-jev-pilot decisions triage --provider openrouter --dry-run
/tmp/crewship-jev-pilot decisions rerank \
  --input scripts/jev-eval/rerank.json --provider openrouter --dry-run
python3 scripts/jev-eval/run.py --binary /tmp/crewship-jev-pilot \
  --output /tmp/jev-local-validation
```

Pro živý běh zpřístupnit `OPENROUTER_API_KEY` bezpečně v prostředí a spustit:

```bash
python3 scripts/jev-eval/run.py --binary /tmp/crewship-jev-pilot \
  --provider openrouter --live --max-calls 24 --output /tmp/jev-live-run-1
```

Případně `--provider typesafe` a `TYPESAFE_API_KEY`. Výstupní adresář musí být nový, staré důkazy se nepřepisují. Při první chybě se běh zastaví. Bez `--live` nevznikají síťová volání ani odhady přesnosti. Klíče nepatří do příkazové řádky ani do výsledků.

### Skutečně provedené ověření

24/24 lokálních požadavků prošlo sestavením a validací přes CLI; byl ověřen i OpenRouter rerank dry-run. [Doklad](reports/jev-2026-09-20/dry-run.json). **Úspěšných autentizovaných inferencí: 0.** Měřicí skript ukončil živý pokus před voláním kvůli chybějícímu `OPENROUTER_API_KEY`; přesnost a cena jsou správně `null`. [Doklad](reports/jev-2026-09-20/live-attempt.json).

Ověřil jsem názvy credential proměnných shellu, odpovídající položky lokálních env souborů, prostředí běžícího procesu dev3 a metadata credentialů aktuálního workspace přes CLI na `localhost:8083`: OpenRouter ani TypeSafe zde nebyly dostupné. Kontrola veřejného endpointu bez autentizace vrátila TypeSafe HTTP 403 a OpenRouter HTTP 401; nevypovídá o kvalitě modelu. Uživatel dostal průběžnou žádost o umístění klíče, nikoli o jeho zveřejnění.

Testy a úplná verifikace jsou zaznamenány v [verification.md](reports/jev-2026-09-20/verification.md).

## Ekonomika: co opravdu počítat

Odvozený příklad při zveřejněné ceně: 1 000 vstupních tokenů × 100 000 volání = 100 milionů tokenů, tedy **$4,20**. Jedno takové volání stojí $0,000042. Šest stejných samostatných klasifikací zbytečně opakuje stav; společné otázky mohou snížit tento režijní vstup, přesný poměr je nutné měřit podle `usage`.

Pro kaskádu: `cena = C_jev + podíl_fallback × C_původní`. Pokud model převezme jen 40 % případů, stále platíme původní model za zbývajících 60 %. Ilustrativně při C_původní=$0,002 a C_jev=$0,000042 je úspora proti samotnému původnímu modelu přibližně 38 %. Při 80% pokrytí přibližně 78 %. To jsou výpočty pro zvolené vstupy, **ne změřená úspora Crewshipu**. Započítat musíme chybná směrování, lidskou kontrolu, retry, překlady a latenci fallback větve.

## Jak získat maximální praktický přínos

1. **Zafixovat test, potom ladit.** Pro triage alespoň několik set skutečných, anonymizovaných případů s lidskou anotací; českou a anglickou část vyhodnocovat odděleně. Současných 24 syntetických příkladů ověřuje integraci, nikoli produkční kvalitu. Překladové dvojice nejsou statisticky nezávislé.
2. **Porovnávat férové varianty.** Současná pravidla, levný LLM s výstupem pouze label, LLM s celým rozdělením, Jev, Jev→LLM kaskáda. Měřit celou cestu se stejnými daty, nikoli jen rychlost jedné API odpovědi.
3. **Kalibrovat konkrétní rubriku.** Rozdělit train/validation/holdout; prahy vybírat mimo test. Accuracy, coverage, selective risk, Brier, ECE z pravděpodobností; zvlášť dopad negací, prompt injection a češtiny. Vysoké confidence samo o sobě není release gate.
4. **Začít doporučováním.** Uživatel vidí návrh oblasti; dosavadní pravidla mají přednost. Journal ukládá verzi modelu, receptu, pravděpodobnosti, čas, usage a přijatou/opravenou kategorii. Chyba API znamená původní cestu a záznam, ne ztracený úkol.
5. **Rerank připojit za autorizovaný retrieval.** Nepřidávat obsah z cizího workspace. Zachovat BM25/RRF jako fallback. Testovat 10/20/30 kandidátů i délku pasáží; hodnotit kvalitu odpovědi agenta, ne pouze modelové score. Výchozí paměť má v roadmapě požadavek lokálního provozu, proto cloudové řazení musí zůstat explicitně volitelné.
6. **Rozšiřovat podle návratnosti.** Výběr skillu a vynechání zbytečného agenta mají potenciálně větší dopad než kosmeticky rychlejší klasifikace. Nezávislé otázky seskupovat, ale neplnit stav nesouvisejícími dokumenty. Invarianty, pořadí akcí a výpočty ponechat v Go.
7. **Hlídat změny.** Pevná verze, regression sada při změně promptu/modelu, timeout, backpressure, měření p95/p99 a rozpočty. Automatický přechod aliasu bez nového vyhodnocení by zneplatnil kalibraci.

Pro první serverové zapnutí navrhuji jako pracovní cíle: alespoň 98% přesnost doporučení na přijaté podmnožině, alespoň 50% pokrytí a p95 pod 1 s z našeho serveru; zvláštní minimální kvalita pro češtinu. Jsou to návrhy akceptačních kritérií, ne pozorované vlastnosti Jev. Bez splnění těchto cílů může stále dávat smysl čistě poradní režim. Oprávnění a destruktivní akce zůstávají za existujícími deterministickými a lidskými branami.
