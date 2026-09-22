# Routines — převzetí, opravy a zbývající přejímka (21. září 2026)

## Verdikt

Implementační opravy jsou připravené a ověřené také ve společné integrační
větvi. **Nejsou dosud sloučené ani společně nasazené. PRD není přijaté.**
Nula doložených průchodů reprezentativních uživatelů znamená §11 NOT VERIFIED.
Žádná procenta dokončení nejsou měřená ani používaná.

## Připravené změny

| PR | Výsledek | Věcný head |
| --- | --- | --- |
| [#2631](https://github.com/crewship-ai/crewship/pull/2631) | Stránkování katalogů, dávkové lookupy plánů/kalendáře, indexy feedů | `2836a43d3` + dokumentační doplnění |
| [#2638](https://github.com/crewship-ai/crewship/pull/2638) | Work a Deliveries v Activity, `/work` přesměrování, společná navigace | `81cfae18e` |
| [#2640](https://github.com/crewship-ai/crewship/pull/2640) | Funkční Decisions tab v Edit, otázky/akce přes draft/publish, pravdivý People diff | `3b41fc829` |
| [#2641](https://github.com/crewship-ai/crewship/pull/2641) | Povinné HTTP credentials se nesmějí tiše změnit na anonymní požadavek; chyba probe blokuje běh | `98a651d35` |

R8 nebyl pouze chybějící browserový důkaz: starý builder neměl produkčního
volajícího. #2640 obnovuje úpravu formulářů **existujících** approval kroků;
nevytváří obecný vizuální editor grafu. Nový approval krok se nadále vytváří
přes CLI nebo agenta. #2641 je výslovná změna kompatibility: veřejný HTTP
endpoint má vynechat credential_ref, nikoli spoléhat na jeho tiché selhání.

## Co bylo nově ověřeno

- Samostatné frontendové opravy: Activity 9 218 testů / 773 souborů;
  R8 cíleně 362 testů. Build, lint bez chyb a testová typová kontrola prošly.
- Společná větev `test/routines-acceptance-integration`, commit `d4dfae3b7`:
  produkční build, 496 cílených frontendových testů, typy, lint, cílené API
  a database, celý pipeline balík, vet, migration lint a agents-invariants.
- Browser společného exportu proti autentizovanému API DEV1: Work redirect,
  přepínání/reload/Back/Forward, klávesnice, 390 px bez overflow; formulář
  author → draft → publish → rozhodnutí → dokončený běh. Readback běhu
  `run_cmub40u52002023dc17ce`: `data.amount=42.5`, `action_id=continue`.
- Testy mají negativní důkaz: oba nové editorové scénáře selžou na původním
  main; porušení replace přesměrování odhalí test; původní HTTP executor
  neprojde třemi novými testy chybějícího resolveru. Změněný kód prochází.
- Deset živých HTTP kontrol vlastníka/druhého účtu/anonyma: očekávané
  200/401/403/404. Je to podmnožina autorizace, nikoli úplná matice rolí.
- Handler 300 plánů: medián přibližně 15 → 3 ms. Browserový baseline stejného
  datasetu má opakované časy, request count a bajty; podrobnosti a omezení v
  [auditu](routines-security-performance-audit-2026-09-20.md).
- Historie syntetického běhu: 100 definovaných kroků, 1 000 uložených pokusů
  přes deset API stránek bez duplicit; v prohlížeči načteno dalších 500 journal
  událostí. API přečetlo všech 10 000 událostí přes 21 požadavků (poslední
  prázdný) bez duplicit. Nejde o 1 000 skutečných runtime retry ani crash test.

Počáteční journal fixture měla nekanonický čas; před stránkovacím ověřením
byla opravena na millisekundový formát skutečného zapisovače. Počáteční R8
harness špatně četl envelope exekucí; opraven na `.rows` a dokončený pokus.
Tyto chyby testovacího postupu nejsou produktové defekty.

## Co není doložené jako hotové

- Lokální celý Go běh na Go zdrojích main dosáhl 40min timeoutu v database
  při migračních testech. Jejich samostatné opakování prošlo za 36,560 s.
  Ostatní balíky a vet prošly; příčina timeoutu není
  definitivně diagnostikována. Následný celý běh credential větve
  (`go test ./... -count=1 -timeout 40m`) prošel: API 1 455,765 s, database
  2 241,311 s, výstupní kód 0. Dva backendové Race CI joby stále běžely
  při kontrole v 10:48 UTC; lokální úspěch nenahrazuje jejich výsledek.
- CodeRabbit na všech čtyřech PR hlásil rate limit, nikoli skutečné review.
  Ruční kontrola autora a regresní důkazy jsou v PR dle fallbacku CONTRIBUTING.
  GitHub přesto vyžaduje jiné schválení posledního pushnutí (`REVIEW_REQUIRED`,
  `require_last_push_approval`, alespoň 1 approval). Pokus o běžný merge byl
  odmítnut. Auto-merge repozitář nepovoluje. Pravidla nebyla obcházena.
- DEV1 autentizovaně hlásil `2836a43d3`, build `2026-09-21T08:13:18Z`, dirty=true.
  Nové Activity/R8/credential opravy se testovaly v izolovaných exportech a
  testovacích serverech. Jejich společný veřejný deploy ještě neproběhl.
- Historické důkazy R1–R7/R9–R10 zůstávají historické. Nový tvrdý pád serveru,
  živý nejistý externí zápis a úplná matice rolí nebyly dnes zopakovány.
- Lidská přejímka vyžaduje pět reprezentativních lidí, každou z pěti úloh
  nejméně čtyři zvládnou bez nápovědy. Recorder v PR #2640 má jen syntetický
  self-test. Staré odkazy na fixture je třeba před studií ověřit v cílovém
  workspace; nevydávat interní walkthrough za výsledek studie.

## Navazující postup

1. Dokončit CI na finálních headech a skutečné schválení jiným reviewerem;
   řešit každý nález, neslučovat podle zeleného rate-limit statusu.
2. Sloučit čtyři PR postupně, zachovat oba záznamy při konfliktu CHANGELOG.
   Společný ověřený strom je připravený v integrační větvi.
3. Nasadit pouze DEV1, ověřit API/frontend/binary identitu a zopakovat společný
   browserový průchod nad veřejným nasazením. Zachovat cizí WIP.
4. Provést a zaznamenat lidskou přejímku. Podle výsledků opravit konkrétní
   neúspěšné úlohy; teprve poté rozhodnout o přijetí §11.

Lokální evidence: `/srv/crewship/backups/crewship_1/routines-integration-20260921/`,
`routines-performance-20260921/`, `routines-security-live-20260921/` a původní
samostatné R8/Activity/credential adresáře pod stejným backup rootem.
Všech původních 17 WIP souborů bylo SHA-256 ověřeno jako nezměněných.
Vlastní testovací rutiny byly smazány a auditní historie zachována; dedikovaný
druhý bezpečnostní účet/workspace zůstává. Dočasné testovací API servery
byly ukončeny a jejich tokeny odstraněny. DEV2/DEV3 nebyly měněny.
