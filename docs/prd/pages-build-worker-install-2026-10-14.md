# Pages Apps — zapnutí jedním příkazem

Stav: veřejný návrh z 2026-10-14, revize `main` @ `612908edc`. Řeší
[#3108](https://github.com/crewship-ai/crewship/issues/3108). Implementováno
kromě §4.3 (výchozí úložiště projektů) a §8 (odloženo); kontrakt je
[docs/specs/pages-apps.md](../specs/pages-apps.md), provozní postup
[docs/guides/pages-apps-operations.mdx](../guides/pages-apps-operations.mdx).
Změna pravidla „digest image v konfiguraci“ (§4.1) je zapsaná jako navržené
rozhodnutí [ADR-PAGES-IMAGE-BY-LABEL-2026-10-14](../decisions/ADR-PAGES-IMAGE-BY-LABEL-2026-10-14.md)
a čeká na souhlas maintainera.

## 1. Shrnutí

| | Dnes | Po změně |
| --- | --- | --- |
| Lokální instalace a vývoj | zdrojový checkout, ruční `docker build`, 2–3 proměnné, restart | `crewship page enable` |
| Produkce | totéž + DNS/TLS pro druhý web | `crewship page enable` + `CREWSHIP_PAGE_RUNTIME_ORIGIN` + DNS/TLS |
| Upgrade binárky | build stránky selže obecnou chybou | server, `doctor` i API řeknou „spusťte `crewship page enable`“ |
| Bez internetu | ruční build a digest | `docker save`/`docker load` image vyrobeného příkazem `enable` |

Nemění se: build nikdy nestahuje image ani závislosti (`--pull=never`,
`--network=none`), běží z neměnného image ID, otisk profilu musí odpovídat
serveru, a vypnuté Pages Apps nic neinstalují ani nespouštějí.

## 2. Dnešní stav (ověřeno v kódu)

Aby šla vlastní Page aplikace postavit a publikovat, musí správce dnes:

1. mít zdrojový adresář `tools/pages-build` (binárka, balíček ani Homebrew ho nemají),
2. spustit `docker build`, přečíst `sha256:` ID a zapsat ho do `CREWSHIP_PAGE_BUILD_IMAGE`,
3. nastavit `CREWSHIP_PAGE_PROJECTS_PATH` (mimo `base_path`),
4. nastavit `CREWSHIP_PAGE_RUNTIME_ORIGIN` (jiný web než Studio),
5. restartovat server.

| Fakt | Důkaz |
| --- | --- |
| Worker se zapne jen s image + projects path, jinak 503 bez návodu | `internal/api/router_pages.go:64-75`, `internal/api/pages_build.go:60`, `:183` |
| Publikace, kontrola a review potřebují i runtime origin, jinak 503 | `internal/api/pages_project_publish.go:192`, `:796`, `internal/api/pages_project_review.go:910` |
| Image se nikdy nestahuje, tagy se odmítají | `internal/pagebuild/docker.go:20-33` |
| Neshoda verze image se pozná až po buildu | `internal/pagebuild/docker.go:101-103` |
| Žádný příkaz `crewship` image nestaví; jen skripty a CI | `scripts/test-pages-apps.sh:9`, `.github/workflows/ci.yml:479-489` |
| Release image kompilátoru nepublikuje | `.github/workflows/release.yml` (jen `ghcr.io/…/crewship`) |
| Binárka už embeduje profil kompilátoru, chybí jen `Dockerfile` a `pnpm-workspace.yaml` | `tools/pages-build/profile.go:13` |
| Seed: chybějící storage přeskočí jednou, chybějící worker hlásí u každé aplikace | `cmd/crewship/cmd_seed_data_pages.go:65-96` |
| `dev.sh` nastaví projects path, ale ne image ani runtime origin | `dev.sh:110`, `:617` |
| Studio `http://localhost:P` a runtime `http://127.0.0.1:P` jsou pro kontrolu dva různé weby; HTTP je povolené na loopback IP | `internal/pagebuild/runtime.go:22-31`, `:44-73` |
| Výchozí `base_path` je `/var/lib/crewship`, takže projects path nemůže ležet uvnitř datového adresáře | `internal/config/paths.go:75-89`, `internal/config/page_projects.go:13-37` |
| Precedens pro strojově čitelný kód chyby | `replyErrorCode` v `internal/api/helpers.go:404`, `restricted_runtime_unavailable` v `internal/api/restricted_workflow.go:28` |

Architektura Pages Apps ([pages-apps-architecture.md](pages-apps-architecture.md),
sekce o nákladech modulu) vyžaduje, aby tools image byl volitelně instalovaný a
aby vypnuté Pages Apps neinstalovaly image ani nespouštěly worker. Automatická
instalace při startu je proto vyloučena; zapnutí musí být výslovný krok.

## 3. Cíle a necíle

**Cíle**

- G1. Na stroji s Dockerem zapne Pages Apps jeden příkaz, bez checkoutu a bez editace konfigurace.
- G2. Lokálně není potřeba nastavit nic dalšího; v produkci jen runtime origin.
- G3. Každé místo, které narazí na chybějící nebo zastaralý kompilátor, řekne, který příkaz spustit.
- G4. `./dev.sh start` + `crewship seed` vytvoří publikované demo aplikace.

**Necíle**

- Instalace bez výslovného souhlasu správce.
- Změna izolace buildu nebo pravidla „build nestahuje“.
- Automatizace DNS/TLS pro produkční runtime origin.
- Publikovaný image v registry (viz §8).
- Jiný build engine než Docker.

## 4. Návrh

### 4.1 Image se hledá podle štítku, ne podle konfigurace

`crewship page enable` postaví image a označí ho štítkem
`ai.crewship.pages.image=<pageprofile.ImageFingerprint()>`.

Server hledá image takto:

1. Je-li nastaven `CREWSHIP_PAGE_BUILD_IMAGE`, platí jako dnes (přepsání pro speciální případy).
2. Jinak najde lokální image se štítkem rovným vlastnímu otisku
   (`docker image ls --filter label=… --format '{{.ID}}'`, při více shodách nejnovější).
   Nalezené ID si zafixuje; build dál běží z neměnného `sha256:` ID.
3. Hledá při startu a znovu tehdy, když by build jinak vrátil 503. Po
   `crewship page enable` proto **není potřeba restart**.
4. Nenajde-li nic, Pages Apps jsou vypnuté. Existence image tedy je výslovné
   zapnutí správcem.

Bezpečnost: vyrobit image s tímto štítkem může jen ten, kdo má přístup k Docker
daemonu, tedy fakticky root na hostiteli. To je stejná úroveň důvěry jako dnešní
lokálně postavený digest. Pojistka po buildu (`decodeWorkerArtifact`) zůstává.
Tuto změnu pravidla „digest v konfiguraci“ je potřeba zapsat jako rozhodnutí.

### 4.2 Výchozí runtime origin pro lokální instalaci

Když `CREWSHIP_PAGE_RUNTIME_ORIGIN` chybí a zároveň platí:

- Studio origin (`PageStudioOrigin()`) je `http://localhost:<port>`,
- server poslouchá na adrese dosažitelné přes `127.0.0.1` (`0.0.0.0`, `::`, `127.0.0.1`, `localhost`),

server použije `http://127.0.0.1:<server.port>`. Jde o skutečně jiný web, ne o
vývojovou výjimku same-origin. Ve všech ostatních případech (vzdálení uživatelé,
HTTPS, vlastní doména) se nic neodvozuje a chyba řekne, že je potřeba nastavit
`CREWSHIP_PAGE_RUNTIME_ORIGIN`.

### 4.3 Výchozí úložiště projektů (samostatný PR, po prvním)

Odloženo z prvního PR: `page_projects_path` čtou i zálohy a obnova
(`internal/backup/runner_create.go:456`, `runner_restore.go:976`), `crewship reset`
(`cmd/crewship/cmd_reset.go:116`) a `crewship paths` (`cmd_paths.go:90`). Změna
výchozí hodnoty proto mění chování záloh existujících instalací a potřebuje
vlastní testy. Do té doby `enable` a `doctor` chybějící cestu ohlásí a vypíšou,
co nastavit; `dev.sh` ji nastavuje už dnes.

Cílový návrh: když jsou Pages Apps zapnuté (§4.1) a `CREWSHIP_PAGE_PROJECTS_PATH` chybí, použije
se sourozenec datového adresáře: `<base_path>-page-projects`
(např. `/var/lib/crewship-page-projects`). Nesmí ležet uvnitř `base_path`, protože
ten se mountuje do agentů. `ProjectStore` adresář vytvoří s právy `0700`
(`internal/pages/project_store.go:34`). Balíček systemd ho vytvoří předem
(`StateDirectory=crewship crewship-page-projects` v `packaging/crewship.service`),
protože služba nemůže zapisovat do `/var/lib`. Když se adresář vytvořit nedá,
`doctor` a 503 řeknou, co nastavit.

### 4.4 CLI

```text
crewship page enable     # postaví a označí image kompilátoru pro tuto verzi
crewship page disable    # odstraní image této verze (vypne Pages Apps); --all odstraní všechny verze
```

`enable`:

1. Ověří Docker (stejná detekce jako `crewship doctor`).
2. Když image pro aktuální otisk existuje, vypíše „already enabled“ a skončí.
3. Zapíše embedovaný build context do dočasného adresáře a spustí
   `docker build --label ai.crewship.pages.image=<otisk> --iidfile …`.
   Síť je potřeba jen teď (base image, pnpm), ne při buildech stránek.
4. Na stdout vypíše jen ID image (pro skripty: `image=$(crewship page enable)`),
   vše ostatní na stderr. Starší Pages image ponechá kvůli rollbacku a vypíše,
   jak je odstranit.
5. Vypíše, co ještě chybí: typicky runtime origin v produkci.

Nezapisuje žádnou konfiguraci, a proto nepotřebuje práva k souborům služby.
Stačí přístup k Dockeru.

`crewship doctor` dostane řádek Pages: `enabled` / `not enabled` /
`outdated (image for another version)` / `runtime origin missing` /
`projects path not writable`.

`crewship seed`: první 503 s kódem `page_apps_not_enabled` nastaví příznak,
zbylé aplikace přeskočí a vypíše jednu zprávu s příkazem, stejně jako dnes u
chybějící storage. Nový endpoint není potřeba.

### 4.5 API

503 v `pages_build.go:60`, `:183`, `pages_project_publish.go:192`, `:796` a
`pages_project_review.go:910` přejdou na `replyErrorCode` s kódy:

- `page_apps_not_enabled`: „Pages Apps are not enabled. Run `crewship page enable` on the server host.“
- `page_runtime_origin_missing`: „Set CREWSHIP_PAGE_RUNTIME_ORIGIN to a separate site.“

Chyba otisku po buildu (`docker.go:102`) změní text na „run `crewship page enable`“.
Odpovídající aktualizace OpenAPI proběhne přes `cmd/gen-openapi`.

### 4.6 Vývoj a testy

- `dev.sh start`: pokud běží Docker a image pro aktuální otisk chybí, spustí
  `crewship page enable` (vypnutí: `CREWSHIP_DEV_PAGES=0`). Runtime origin se
  odvodí podle §4.2, protože `CREWSHIP_NEXTJS_URL` je `http://localhost:<next port>`.
- `scripts/test-pages-apps.sh` a CI (`ci.yml:479-489`) použijí `crewship page enable`
  místo vlastního `docker build`, takže testují cestu zákazníka.

## 5. Postup implementace

Před prvním commitem: `scripts/claim-issue.sh 3108`. Každý krok je samostatný PR
se zelenou verifikační smyčkou (`go test ./... -count=1 -timeout 40m`, `go vet ./...`,
`go run ./scripts/docs-inventory -strict`).

| Krok | Obsah | Soubory | Testy |
| --- | --- | --- | --- |
| 0 | Rozhodnutí o hledání image podle štítku | `docs/decisions/` | — |
| 1 | Embedovaný build context | `tools/pages-build/profile.go` (+ `Dockerfile`, `pnpm-workspace.yaml`), funkce pro zápis contextu | embedovaný context je bajtově shodný se soubory v repu |
| 2 | Kódy chyb a texty | `internal/api/pages_build.go`, `pages_project_publish.go`, `pages_project_review.go`, `internal/pagebuild/docker.go` | odpovědi 503 obsahují kód a příkaz |
| 3 | Seed jedna zpráva | `cmd/crewship/cmd_seed_data_pages.go` | rozšířit `cmd_seed_data_pages_test.go` (vzor: storage 503 na řádku 646) |
| 4 | `crewship page enable` / `disable` | `cmd/crewship/cmd_page_enable.go`, `docs/cli/page.mdx` | idempotence, bez Dockeru nenulový exit; Docker test se přeskočí bez daemonu |
| 5 | Hledání image podle štítku + opětovné hledání místo 503 | `internal/pagebuild/` (resolver), `internal/api/router_pages.go`, `pages_build.go` | štítek pro jiný otisk se ignoruje; explicitní env má přednost; zapnutí bez restartu |
| 6 | Výchozí runtime origin a projects path | `internal/config/page_projects.go`, `config.go`, `packaging/crewship.service` | tabulka případů: localhost / LAN IP / HTTPS / vlastní host / base_path překryv |
| 7 | `doctor` | `cmd/crewship/cmd_doctor*.go` | všechny stavy řádku Pages |
| 8 | Dev a CI | `dev.sh`, `scripts/test-pages-apps.sh`, `.github/workflows/ci.yml` | `scripts/test-pages-apps.sh` zelený |
| 9 | Dokumentace | `docs/guides/pages-apps-operations.mdx`, `tools/pages-build/README.md`, `docs/configuration/environment.mdx`, `yaml-config.mdx` | `docs-inventory -strict` |

Kroky 1–3 mají hodnotu samy o sobě a lze je mergnout dřív než zbytek.

## 6. Akceptační kritéria

1. Čistá instalace binárkou na Linuxu s Dockerem, Studio na `http://localhost:8080`:
   `crewship page enable` → bez restartu a bez editace konfigurace jde Page
   aplikace postavit, zkontrolovat a publikovat.
2. Totéž s vlastní doménou: bez `CREWSHIP_PAGE_RUNTIME_ORIGIN` vrací publikace 503
   `page_runtime_origin_missing`; po nastavení a restartu funguje.
3. Bez Dockeru: `enable` skončí nenulovým kódem a srozumitelnou zprávou.
4. Druhé `enable` stejnou verzí nic nestaví.
5. Po výměně binárky za verzi s jiným otiskem: `doctor` hlásí `outdated`, API vrací
   `page_apps_not_enabled`, po `enable` vše funguje. Starý image zůstal.
6. `docker rmi` image: `doctor` hlásí `not enabled` před prvním buildem.
7. Bez `enable` server nespouští žádný Pages kontejner a nevytváří projects adresář.
8. Bez internetu: `docker save` image z online stroje se stejnou verzí, `docker load`
   na cílovém stroji → Pages Apps zapnuté bez dalšího kroku.
9. `./dev.sh start` + `crewship seed` na čistém dev stroji s Dockerem → demo
   aplikace publikované; s `CREWSHIP_DEV_PAGES=0` seed vypíše jednu zprávu.
10. Build stránky dál běží s `--pull=never`, `--network=none` a neměnným ID
    (stávající `internal/pagebuild/docker_test.go` beze změny).

## 7. Rizika

| Riziko | Opatření |
| --- | --- |
| Změna pravidla „digest v konfiguraci“ | Krok 0: rozhodnutí; explicitní env zůstává a má přednost |
| `enable` uvnitř Docker Compose nasazení selže, socket proxy má `BUILD=0` (`docker/docker-compose.prod.yml:108-114`) | Dokumentovat spuštění `crewship page enable` na hostiteli se stejnou verzí; rozšíření proxy jen výslovně |
| Hostitel nesmí do `/var/lib` | systemd `StateDirectory`; jinak jasná zpráva v `doctor` |
| Stará image po upgradech zabírají místo | `enable` je vypíše; `disable --all` je odstraní; správce rozhoduje kvůli rollbacku |

## 8. Odloženo

- Publikovaný podepsaný image v `ghcr.io` a `enable --pull`. Dává smysl, až budou
  uživatelé Compose bez BUILD nebo bude build příliš pomalý. Pull by byl povolen
  jen v `enable`, nikdy při buildu stránky.
- Průvodce produkčním runtime originem v `crewship setup`.
