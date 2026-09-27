# Agent access / runtime — implementace Release 1.0

Navazuje na [rešerši](RESEARCH-AGENT-ACCESS-RUNTIME-HEARTBEAT-2026-09-27.md).
Tracking: [#2703](https://github.com/crewship-ai/crewship/issues/2703).
Uživatel autorizoval vývoj, testy a nasazení na dev1. Základ implementace je
`8dc421fdb` na main; jiné instance ani produkce nejsou cílem.

## Dodávky a hranice

| Balík | Stav | Akceptace |
|---|---|---|
| A1: směrované oprávnění sdílených souborů mezi crews | Implementováno, probíhá release ověření | Settings/API/CLI, none/read/read+delivery, stale update 409, role/workspace, odebrání dalšího requestu |
| A2: agent→agent / projektové granty | Připravený návrh, neimplementováno | stabilní resource ID, efektivní dědění, všechny čtecí cesty, shell hranice |
| B: omezený klientský běh a konverzace | Neimplementováno | žadatel→běh→výstup, historie, paměť a artefakty dvou klientů |
| C: service desired state / obnova po rebootu | Doložen současný kód, chybí implementace a reboot akceptace | durable running/stopped, rekonciliace, data/identity, žádná duplicita |
| D: credentials / revokace konkrétního grantu | Policy/proxy cílené testy z rešerše, další mezery otevřené | rozdílné lease, odebrání jedinému agentovi, výpadek autority, izolovaná delivery |
| E: Chat / Issues / Routines / Pages | Existující mechanismy inventarizované, společná omezená autorita nedokončena | negativní end-to-end matice včetně logů/streamů/delegace |
| F: levná kontrola práce před heartbeat | Návrh, neimplementováno | žádné prázdné LLM wake, budget/dedupe/recovery bez oslabení lease |

Tabulka není prohlášení, že celý Release 1.0 je připraven. Každý další balík musí
mít vlastní reproduktor a testovací bránu. Sdílené UID crew zůstává důvěrovou
hranicí: A1 neizoluje libovolný shell a nepřidává klientovi právo jen na jednoho agenta.

## A1 — konečný kontrakt

- Link nadále určuje směr komunikace/delegace. `forward_file_access` a
  `reverse_file_access` zvlášť zužují shared-file API v každém směru.
- `none` nic; `read` výpis/čtení; `read_write` čtení a doručení souboru.
  Doručení není obecná editace celého projektu. Žádné implicitní credentials
  nebo soukromé HOME. Směr bez komunikačního linku zůstává nepřístupný.
- Migrace zachovává dosavadní `read_write` existujících linků. Nové linky ze
  Settings začínají `none`; staré API/CLI vytvoření bez nových polí zachovává
  historický default. POST existujícího linku nemění file permissions.
- MANAGER+ používá verzovaný PUT. Souběžná stará změna dostane 409; role,
  workspace a requester membership se kontrolují serverově. Nové čtení/zápis
  používá aktuální DB pravidlo. Již přijatý požadavek může doběhnout.
- Změna směru v existující UI cestě zachovává směrová file permissions i při
  nahrazení řádku a při rollbacku. Chyba načítání nehlásí prázdný workspace;
  neověřená nastavení nelze měnit. MEMBER vidí hodnoty bez editačních ovladačů.
- CLI: `crew file-access <connection> <requester> <level> <version>`, pozorovaná
  verze je v `crew connections --format json`.

## Nově potvrzený nález a oprava

`TestCrewFileAccess_DeliveryCannotOverwriteSharedRoot` před opravou vrátil 201
a přepsal umělý `shared/project.sh` uploadem na `../../project.sh`. `filepath.Join`
smazal traversal ještě před kontrolou cíle. Oprava validuje cestu před spojením
s `incoming/<sender>`. Po opravě je požadavek 400 a původní obsah zůstává zachován.
Toto je potvrzená chyba request path; netvrdí úplnou procesní/symlink izolaci crew.

`TestCrewFileAccess_CannotImpersonateSiblingRequester` před opravou prokázal
čtení (200) i upload (201) s tokenem crew C, ale `requester_crew_id` crew A.
Workspace kontrola sama nestačila. Obě file operace nyní používají také
`assertBoundCrewWorkspaceDB` na requester; cíl zůstává legitimně cizí crew.
Workspace-bound/master kompatibilita zůstává, nejde o novou agentí identitu.

Verzovaný DELETE chrání UI změnu směru před obnovením oprávnění ze staré
otevřené obrazovky. Neshoda vrátí 409 a UI znovu načte aktuální stav.

## Doložený experiment Linux

Na dev1 byl spuštěn pouze vlastní jednorázový Docker kontejner z již přítomného
image, bez sítě, s read-only rootfs, `cap-drop ALL`, `no-new-privileges`, UID 1001
a samostatným tmpfs `/probe`. Umělé agentí adresáře měly 0700, soubor obsahoval
pouze `synthetic-only` a měl 0400. Proces s HOME agent-b přečetl soubor agent-a.
Kontejner se po testu odstranil (`--rm`). Nebyla čtena skutečná hesla ani data
existujících crews. Výsledek potvrzuje omezení DAC při společném UID; nejde o
test všech ochranných vrstev živé Crewship instalace.

## Ověření

- Cílené backend testy A1, stávajících connections/messaging a traversal prošly.
- Settings: 20 testů prošlo; přidána změna přesného směru/verze a chyba načítání.
- CLI: nový příkaz a předání chyby stale verze prošly.
- TypeScript, ESLint (0 chyb, 30 existujících varování), produkční Next export
  a Go vet prošly v průběžném ověření.
- První celý Go průchod zachytil chybějící novou operaci v OpenAPI a role manifestu.
  OpenAPI byl regenerován, role manifest doplněn; finální průchod probíhá.
- Reálný reboot hosta, úplná izolace klientských běhů a všechny balíky B–F nejsou
  tímto ověřeny. Dev1 smoke a konečné výsledky doplnit po nasazení.

## Navazující implementační pořadí a release brány

1. **A2 + B: objektová autorita a klientský kontext.** Inventář entrypoints
   (HTTP, WS, download/export, proxy, interní tool, fronta) musí uvést konkrétní
   enforcement místo. Stabilní ID subjektu a resource; oprávnění read, edit,
   execute, publish a credential-use se nesmějí slít do write. Omezený běh
   uchovává žadatele, scope a verzi politiky; delegace scope jen zužuje.
   Spouštění z chatu/rutiny/issue musí používat stejný kontrakt. Test dvou lidí
   u stejného agenta musí pokrýt historii, soubory, paměť, log i stream.
2. **Procesní hranice A2.** Zachovat UID 1001/1002, zavést samostatný runtime
   podle důvěrové autority místo změn UID v existující crew. Read-only sdílení
   připojit jen do povoleného prostoru, write projekt má konkrétního vlastníka.
   Bezpečnostní test používá syntetická secrets a pokusy ze shellu; průchod
   souborovým API sám o sobě nestačí. Migrace interních crews je explicitní,
   nepřeruší dosavadní sdílenou práci ani ji nepřejmenuje na izolovanou.
3. **D: autorita credentials.** Nejdříve reproduktor odebrání jednoho agentího
   grantu při zachování jiného, dvě lease a stale sidecar. Potom versioned
   grant snapshot/refresh a definovaný limit zastarání pro omezený runtime.
   Proxy-only preference nezmění fakt, že plaintext doručený do procesu je
   pro tento proces čitelný. Každý delivery typ má vlastní negativní test.
4. **C: spravované služby.** Durable desired state a controller s lease;
   idempotentní adopt/create/start/stop; manuální Stop se po restartu zachová.
   Proces 24/7 patří do samostatné deklarované služby, ne do nehlídaného
   background procesu agenta. Nejdříve testy na izolovaném provideru, potom
   disposable kontejnery. Restart celého sdíleného Dockeru/hosta nesmí být
   součástí dev1 smoke; reboot brána vyžaduje samostatnou testovací instanci.
5. **E: sjednocení cest.** Pages zachovají vlastní existující granty; napojení
   na společný rozhodovací kontrakt má test změny identity publikujícího,
   spouštění akce, čtení výsledku a revokace. Routines/Issues zkontrolují
   oprávnění při přijetí i skutečném spuštění, včetně retry a recovery.
6. **F: heartbeat preflight.** Použít existující queue/trigger/lease. Levně
   zjistit vykonatelnou práci, změnu vstupů, budget a kapacitu; nebudit LLM
   bez práce. Dedupe zahrnuje autoritu, ne pouze agenta. Recovery nesmí
   slepě opakovat externí účinek. Měřit počet probuzení bez práce, tokeny,
   čekání na práci a duplicity proti stejnému vzorku událostí.

Každá dodávka má migraci/kompatibilitu, Settings/API/CLI, audit, pozitivní i
negativní test a dev1 smoke. Výkon měřit odděleně: p50/p95 autorizace a seznamů,
počet SQL dotazů na request, cold/warm start, RAM izolovaného runtime a služby,
čas zotavení, tokeny na dokončený úkol. Bez měření uvádět pouze očekávaný směr
dopadu. A1 nepřidává background loop; nahrazuje kontrolu linku jedním dotazem
na link a jeho file permission. Výkonový benchmark zatím nebyl proveden.
