# Diskuze: Licensing, Backupy, Container Provisioning, Runtime Architecture

**Datum:** 2026-04-10
**Status:** Záznam strategické diskuze — rozhodnutí se budou postupně promítat do ADR, PRD a implementace
**Účastníci:** Pavel Srba, Claude Opus 4.6

Tento dokument zachycuje strategickou diskuzi o směrování Crewshipu — licencování, strategie backupů, výběr container provisioning nástrojů, a úvahu o BYOE (Bring Your Own Environment) runtime modelu. Některá rozhodnutí jsou finální, některá odložená, některá explicitně odmítnutá. Dokument slouží jako historický záznam myšlení a důvodů.

---

## Téma 1: Licensing strategie (Apache 2.0 vs alternativy)

### Kontext

Crewship je dnes pod Apache 2.0. Otázka: co když někdo vezme kód, zabalí ho jako vlastní SaaS a bude ho prodávat? Jsme v pasti?

### Analýza Apache 2.0

**Co Apache 2.0 dovoluje:**
- Komerční použití zdarma
- Uzavření do proprietárního produktu
- **Zabalení jako vlastní SaaS a prodej ho** (AWS/Elastic problém)
- Jediná povinnost: zachovat copyright notice + NOTICE file

**Co Apache 2.0 přidává oproti MIT/BSD:**
- **Patent grant** (§3) — přispěvatelé dávají patent license. Proto ji mají rádi enterprise.

**Realita AWS/Elastic scénáře:**
Přesně tohle se stalo Elasticu, MongoDB, Redisu, Terraformu. AWS vzal Elasticsearch, udělal z toho managed službu, Elastic neviděl ani dolar. Proto Elastic přešel na SSPL, Mongo na SSPL, Redis na RSALv2/SSPL, HashiCorp na BSL. Všichni to řešili ex post a všichni si tím naštvali komunitu.

### Ochranné vrstvy, které Crewship už má

1. **Trademark** — Apache 2.0 NEDÁVÁ právo používat název "Crewship" ani logo. Forkař musí produkt přejmenovat. Silnější obrana, než se zdá (Valkey vs Redis).
2. **Copyright holder** — dokud kód píše Pavel (nebo má CLA od přispěvatelů), může licenci kdykoli změnit pro budoucí verze.
3. **Network effect + execution** — fork není produkt. Potřebuje tým, roadmapu, support, integrace. Většina forků umře.

### Srovnání alternativ

| Licence | Co řeší | Proč ne pro Crewship |
|---|---|---|
| **AGPL-3.0** | SaaS provozovatel musí publikovat změny | Enterprise to NENÁVIDÍ, whitelist blacklist → zabiješ adoption |
| **SSPL** (Mongo, Elastic) | Publikovat i celý stack okolo | Není OSI-approved, Fedora/Debian odmítají, reputační problém |
| **BSL / BUSL-1.1** (HashiCorp, Sentry) | Non-compete na X let, pak Apache | Solidní, ale matoucí — "je to opravdu open source?" |
| **Fair Source (FSL)** | Novější varianta BSL | Zatím bez ekosystému, ale dobrá trajektorie |
| **MIT** | Jednoduchost | Chybí patent grant — horší než Apache |

### Rozhodnutí

**Zůstat u Apache 2.0 pro core + proprietární `/ee` adresář** (GitLab open-core model, CRE-79).

Důvody:
1. Apache má nejnižší tření pro enterprise due diligence
2. `/ee` adresář pod BUSL-1.1 nebo vlastním EE EULA chrání placené features před AWS-scénářem
3. Zachovaná důvěra komunity a adopce

### Doporučení k implementaci

- **Zavést CLA nebo DCO** před přijetím prvního externího contributora — dává flexibilitu přelicencovat budoucí verze. [cla-assistant.io](https://cla-assistant.io) pro GitHub.
- **Zaregistrovat trademark "Crewship"** — EU (EUIPO, ~850 €) a US (USPTO). Nejúčinnější obrana proti forkům.
- **`/ee` licencovat pod BUSL-1.1** — zavedenější než custom EULA.
- Hlavní `LICENSE` a `README` musí jasně stanovit, že `/ee/**` není pod Apache.

---

## Téma 2: Container backup strategie a bezpečnostní audit

### Ground truth z code exploration (2026-04-10)

**Container lifecycle** (`internal/provider/docker/docker.go`):
- Jeden fixed base image, pullne se jednou (`Config.RuntimeImage`, line 243-268)
- **Rootfs je READONLY** (`ReadonlyRootfs: true`, line 534) — `apt install` uvnitř kontejneru neprojde
- Dockerfile v `docker/agent-runtime/Dockerfile`, build v CI, žádný per-crew build

**Persistent state uvnitř kontejnerů:**
- Bind mounts (host → kontejner):
  - `/workspace` ← `{OutputBasePath}/workspaces/{CrewID}`
  - `/output` ← `{OutputBasePath}/{CrewID}`
  - `/crew` ← `{OutputBasePath}/crews/{CrewID}`
  - `/secrets` ← `{OutputBasePath}/secrets/{CrewID}`
- Named Docker volumes (persistentní):
  - `/home/agent` ← `crewship-home-{slug}` (line 308)
  - `/opt/crew-tools` ← `crewship-tools-{slug}` (line 309)

**Důsledek:** `pip install --user`, `npm install -g` s prefixem do `/home/agent`, `cargo install` do `~/.cargo` — **to všechno přežije restart i recreate kontejneru**, dokud nesmažeš volume.

**Network restriction** (`internal/sidecar/allowlist.go`):
- Dva módy: `free` (default, `FreeMode=true`, `server.go:150`) a `restricted`
- **Default "free" mode** — agent se dnes dostane kamkoli, včetně sousedních Docker kontejnerů na stejné bridge síti
- Restricted mode má default allowed domains: `api.anthropic.com`, `api.openai.com`, `generativelanguage.googleapis.com`, `api.factory.ai`
- **Sidecar filtruje jen podle domain name přes HTTP proxy** — nenahrazuje network-layer izolaci

**Backup/snapshot:** ŽÁDNÝ EXISTUJÍCÍ KÓD. Ani jedna řádka. `RemoveCrewVolumes()` (line 330) volumes maže bez ceremonie.

### Identifikované bezpečnostní díry

1. **`FreeMode=true` jako default** — v rozporu se "safety first" pozicí produktu. Agent může dnes volat `curl http://gitlab-container:8080` na sousední kontejner a projde to.
2. **Žádná dedikovaná Docker network** — crew kontejnery jsou na default bridge s ostatními kontejnery na hostu.
3. **Potenciální Docker socket risk** — audit je potřeba ověřit, že `/var/run/docker.sock` NENÍ mountnutý do crew kontejneru. Pokud ano, je to game over (root na hostu).

### Strategie backupů — architektonický návrh

**Dvě různé věci, které se snadno pletou:**

**Backup A — stav workspace** (kód, git repo, editované soubory)
- Source: `/workspace/{CrewID}` bind mount
- Backup: tar host cesty. Žádný Docker API není potřeba.
- **95 % toho, co si klient představuje pod "backupem".**

**Backup B — stav nástrojů, paměti a konfigurace** (instalované balíky, `.memory/`, shell history, npm cache)
- Source: named volumes `crewship-home-{slug}`, `crewship-tools-{slug}` + `/output/{CrewID}/.memory/`
- Backup: `docker run --rm -v crewship-home-{slug}:/src -v /backups:/dst alpine tar czf /dst/home-{slug}-{ts}.tar.gz -C /src .`

**Navržená CLI architektura:**

```
crewship crew backup <slug> [--include=workspace,tools,memory,home]
  → jeden .tar.zst se strukturou:
     manifest.json         (crew meta, base image digest, timestamp, volume list)
     workspace/            (tar bind mountu)
     home/                 (tar named volume)
     tools/                (tar named volume)
     memory/               (tar .memory adresáře)

crewship crew restore <archive> [--as=new-slug]
  → vytvoří/nahradí volumes, obnoví bind mounty, spustí kontejner proti stejnému image digestu
```

**Klíčové detaily, které systémy backupu kazí:**

1. **Zmrazit stav před tarem** — kontejner musí být `Pause` nebo `Stop` po dobu tar-ování volumes, jinak dostaneš nekonzistentní snapshot (filesystém uprostřed zápisu). Pause je rychlejší než Stop.
2. **Uložit image DIGEST, ne TAG** — když obnovíš backup za 6 měsíců a tag `agent-runtime:latest` je jiný image, binárka uvnitř `/opt/crew-tools` cílící na staré libc nefunguje. Ukládat `sha256:...`.
3. **NEUkládat `/secrets`** — ten mount obsahuje dešifrované API-key materiály. Backup by je vynesl mimo šifrovaný credstore → díra. Backup tento adresář přeskočí; secrets se při restore znovu injectnou ze credstore.
4. **Content-addressed storage pro dedup** — až budeš mít 50 crews denně, všichni mají 2 GB `~/.cache/pip`. Nepouštět se do toho v MVP, ale mít to v návrhu formátu.
5. **Nevymýšlet restic** — pro V2+ zvážit integraci s `restic` nebo `borg` jako storage backendem. Umí šifrování, dedup, retention, remote repos (S3, B2).

**Docker-native vs Crewship-integrated:**
Nabídnout obě cesty, default na Crewship. Většina cílových uživatelů považuje Docker za černou skříňku. Pro power-users dokumentovat `docker save` / `docker commit` cestu.

### Procentuální důležitost (pro úspěch produktu)

| Problém | Důležitost | Proč |
|---|---|---|
| **Network izolace od sousedních kontejnerů** | **95 %** | Blocker pro enterprise procurement. První security review to najde a padne deal. Bez toho "safety first" není pravda. |
| **Backup workspace** | **85 %** | "Ztratil jsem 3 dny práce agenta" = churn event #1. |
| **Lead agent si doinstaluje tools** | **70 %** | Rozhoduje, jestli je produkt "hračka" nebo "universal tool". |
| **Readonly rootfs pochopený uživateli** | **60 %** | Frustrace #1: "proč `apt install` nefunguje". Chybová hláška nebo přesměrování. |
| **Backup tools/home volumes** | **55 %** | Nice to have, workspace je to hlavní. |
| **Audit log instalací + quota na tools volume** | **50 %** | Enterprise požadavek, hobbyists nezajímá. EE feature. |
| **Content-addressed/dedup backup storage** | **25 %** | Premature optimization. |
| **`docker commit` based backup (celý rootfs)** | **15 %** | Proti-vzor. Nepoužívat. |

### Pořadí implementace

1. **Fix network isolation** (1-2 dny) — dedikovaná Docker network `crewship-agents` + `FreeMode=false` default. **Musí být v dalším release.** Bez toho safety-first claim nemá opodstatnění.
2. **Audit Docker socket** — ověřit, že nikde není mountnutý.
3. **Dokumentovat `/opt/crew-tools` a `/home/agent` jako install cesty**, přidat helper `crew-tools install <pkg>` do base image.
4. **MVP backup: `crewship crew backup/restore`** jen pro `workspace` + `.memory`, tar.zst, lokální filesystem. 2-3 dny práce.
5. **V2 backup**: `--include=home,tools`, `--remote=s3://...`, retention.
6. **EE backup**: restic/borg backend, audit log, scheduling, compliance retention.

---

## Téma 3: Container provisioning tooling (Terraform? Ansible?)

### Otázka

Nemůžeme využít Ansible nebo Terraform pro správu kontejnerů, abychom "nevymýšleli už vymyšlené"? Co je licenčně použitelné?

### Zamítnutí Terraformu a Ansiblu

**Terraform** — BSL licence od v1.6 (srpen 2023), non-compete, **nekompatibilní s Apache 2.0 embeddingem**. Navíc designovaný pro infra provisioning, ne container config. OpenTofu (fork, MPL-2.0) je licenčně OK, ale stále špatná abstrakce.

**Ansible**:
1. **GPL-3.0** — nakazí embedding
2. **Python runtime** — rozbíjí single-binary distribuci (Crewship je jeden Go binary)

**Packer** (HashiCorp) — BSL, stejný problém jako Terraform.

**Pulumi** — Apache 2.0, Go SDK existuje, ale overkill (infra provisioning, ne container config).

### Co skutečně sedne (licenčně čisté)

#### 1. **Devcontainer spec** — klíčový nález

`devcontainer.json` je open spec (Microsoft/GitHub, MIT licence):

```json
{
  "image": "mcr.microsoft.com/devcontainers/base:ubuntu-22.04",
  "features": {
    "ghcr.io/devcontainers/features/terraform:1": {},
    "ghcr.io/devcontainers/features/kubectl:1": {},
    "ghcr.io/devcontainers/features/node:1": {"version": "20"}
  },
  "postCreateCommand": "crew-tools verify"
}
```

**Proč je to klíčové:**
- **Features ekosystém** — na [containers.dev/features](https://containers.dev/features) je několik set hotových idempotentních installerů pro běžné tooly (Terraform, kubectl, Node, Python, Go, Rust, Docker-in-Docker, AWS CLI, gh CLI, atd.)
- **Je to standard** — Claude Code, Cursor, VS Code, GitHub Codespaces všechno to používá. Uživatelé to znají.
- **Adoptuj spec, ne CLI** — oficiální `devcontainer` CLI je Node.js (nechceme). Parsovat v Go si naparsuje.
- **Licence**: spec CC-BY-4.0, features MIT, CLI MIT. Vše použitelné.

**Ilustrativní scénář "Claude Code vydal novou verzi":**

*Dnes:*
```
1. Upravit Dockerfile: RUN npm i -g @anthropic-ai/claude-code@2.5.0
2. docker build -t ghcr.io/crewship-ai/agent-runtime:v1.4.3
3. docker push (800 MB image)
4. Uživatelé pullnou
5. Zítra v2.5.1: opakovat
```

*S devcontainer.json + features:*
```
1. postCreateCommand: npm i -g @anthropic-ai/claude-code@latest
2. Nový crew automaticky dostane nejnovější verzi při vytvoření
3. Ty nebuildíš nic. Microsoft maintainuje base image.
```

**Co to znamená prakticky:** přestaneš být kuchař pekoucí chleba. Stáváš se restaurací s dodávkou čerstvého pečiva.

#### 2. **mise** (dříve rtx) — runtime version manager

[mise](https://mise.jdx.dev/) je single Go-kompatibilní binárka (MIT), která spravuje verze dev toolů deklarativně:

```toml
# .mise.toml
[tools]
node = "20"
python = "3.12"
go = "1.23"
terraform = "1.9"
kubectl = "1.30"
```

- Jeden příkaz `mise install` nainstaluje všechno, každou verzi vedle sebe.
- **Nahrazuje `asdf`, `nvm`, `pyenv`, `rbenv` jedním nástrojem.**
- Instaluje do `~/.local/share/mise` → persistentní `/home/agent` volume → přežije restart zdarma.
- MIT, single binary.

#### 3. **BuildKit** (Go library) — pro budoucí custom image build (V2)

`github.com/moby/buildkit` — Apache 2.0, embeddable v Go, buildí OCI images bez Docker daemonu. Až budeme chtít feature "freeze crew → custom image".

#### 4. **Docker Compose** — jako export formát (V2)

Apache 2.0, standardní. Crewship může nabídnout `crewship crew export --format=compose` jako user-facing portability feature.

### Rozhodnutí (final)

**Adoptovat:**

| Vrstva | Nástroj | Fáze | Proč |
|---|---|---|---|
| Base image build (CI) | Dockerfile → GHCR | Dnes | Jednoduché, máme |
| **Crew definition format** | **devcontainer.json spec** | MVP | Standard, známý, rozšiřitelný |
| **Tool install uvnitř kontejneru** | **devcontainer features + mise** | MVP | Hotové installery, žádný vlastní kód |
| Custom per-crew image | BuildKit Go library | V2+ | Apache 2.0, embeddable |
| Lifecycle management | Vlastní Go kód (máme) | Dnes | Naše požadavky jsou specifické |
| Export/sharing formát | docker-compose.yaml | V2 | User-facing, standardní |
| Container runtime | Docker (MVP), K8s (EE) | Dnes | Žádná změna |

**Zamítnuto (a proč):**
- Terraform — BSL, overkill
- Ansible — GPL-3.0, Python runtime
- Packer — BSL
- Salt/Chef/Puppet — Python/Ruby runtime
- Nix/NixOS — skvělé technicky, ale učební křivka zabije adoption
- Dagger — Apache 2.0 a Go, ale špatná use-case (CI/CD pipelines, ne long-running containers)
- OpenTofu — MPL-2.0 OK, ale stále špatná abstrakce

**Klíčové architektonické pravidlo:** devcontainer features pro systémové tooly, mise pro jazykové runtimes. Nemíchat.

### Související memory záznam

Uloženo jako project memory: `memory/project_container_provisioning_tools.md`

---

## Téma 4: Runtime architecture (BYOE diskuze a ODMÍTNUTÍ localproc)

### Původní úvaha

Pavel nadhodil otázku: "Co kdybychom neřešili kontejnery vůbec? Jen poskytneme agent + memory + orchestrace. Runtime bude cokoli — Mac, Linux, Windows, Docker, VM, whatever."

To vedlo k návrhu "BYOE" (Bring Your Own Environment) s konceptem `localproc` runneru — spouštění agent CLI jako normální subprocess na host stroji místo uvnitř kontejneru.

### Co by "localproc" znamenalo v jedné větě

**Localproc = spustit `claude` jako normální subprocess na uživatelově Macu místo uvnitř Docker kontejneru.** Nic víc.

Dnes:
```
docker exec crewship-team-alpha  claude --print --stream-json "..."
```

S localproc:
```
claude --print --stream-json "..."
```

Stejná binárka, stejný protokol, stejný stream-json output. Chybí jen "docker exec" obálka.

### Proč to bylo zamítnuto

**Pavlova obava (2026-04-10):**

> "Ten local proces se mi zatím moc nelíbí. Nedokážu si to představit. Já se chci soustředit na Docker, protože to je pro mě security first věc a dá mi ten největší smysl. Jak má to být lokál proces, tak se může stát, že agent ovládne celý systém, na kterém běží, třeba macOS, nebo může zneužít data."

Tato obava je **validní a odborně odůvodněná**:

1. **Žádná izolace** — agent běžící jako subprocess na Macu má přístup ke všemu, co má uživatelův account: `~/.ssh`, `~/.aws/credentials`, `~/Documents`, iCloud Drive, keychain (pokud není zamčený), Safari cookies, atd.
2. **Cross-platform sandbox je slabý** — macOS `sandbox-exec` je deprecated, Linux `bwrap` vyžaduje instalaci, Windows AppContainer je neužitečný. Není žádná konzistentní cesta k "bezpečnému" hostování agenta bez kontejneru.
3. **Safety-first pozice produktu** — Crewship se odlišuje od konkurence tím, že "agent nemůže nic rozbít". Localproc by tuto pozici rozmělnil i jako opt-in — marketingově matoucí.
4. **Riziko rozštěpení kódu** — dva code paths (Docker + localproc) znamenají víc bugů, feature drift, dvojnásobnou testovací matrici. Pavel výslovně vyjádřil obavu, že se mu "program za 3 měsíce rozštěpí pod rukama".
5. **Není to potřeba pro MVP** — tržní signály, které by localproc ospravedlňovaly ("Docker je moc těžký"), zatím nepřišly. Implementovat to preventivně by byla předčasná optimalizace.

### Definitivní rozhodnutí

**Crewship zůstává Docker-first a container-centric.** Localproc runner se **neimplementuje**. AgentRunner interface abstrakce se **nevytváří** (dokud nebude konkrétní důvod).

- **Docker je security baseline a zůstává jím.**
- Enterprise příběh: "agent je vždy v hardened kontejneru s egress allowlistem, readonly rootfs, drop capabilities."
- Pokud v budoucnu přijde tržní signál (enterprise požadavek, masivní Mac/Windows friction feedback), bude se localproc zvažovat znovu — ale jako **samostatná strategická diskuze**, ne jako MVP feature.

### Co zůstalo z BYOE úvah jako užitečné

- **Path resolver abstrakce** — byla navržena jako enabling work pro localproc. **Také se neimplementuje teď** (protože nemáme localproc), ale koncept je uložen pro případ, že by byl potřeba pro jiné providery (K8s, remote runner, cloud mode).
- **Memory portability jako koncept** — přežívá. Viz Téma 5.
- **Zjištění o system preamble leaking cest do LLM promptu** (`internal/orchestrator/exec.go:22`) — dobré to vědět. Pokud se někdy budou přidávat další providery, tohle bude blocker.

---

## Téma 5: Memory architecture (shared memory gap + portable brain)

### Ground truth (2026-04-10)

**Dnešní stav memory systému:**

- **Source of truth: markdown soubory na disku**, NE SQLite
- Per-agent memory: `/crew/agents/{slug}/.memory/AGENT.md` + `.memory/daily/{YYYY-MM-DD}.md`
- FTS5 index per-agent: `/crew/agents/{slug}/.memory/index.sqlite` (lokální, ne v hlavní DB)
- Main SQLite má jen `agents.memory_config TEXT` (JSON config) — žádné memory chunks
- FTS5 engine: `internal/memory/engine.go`, reindex `internal/memory/index.go`
- Search: `internal/memory/search.go`, BM25 přes sparse index (žádné dense embeddings)

**Plumbing:**
- Sidecar má endpoints: `POST /memory/search`, `GET /memory/status`, `POST /memory/reindex`
- Orchestrator injectuje memory context do system promptu: `internal/orchestrator/memory.go:25` (`buildMemoryContext`)
- DB migration 3: `memory_config TEXT` column on agents table
- Memory flow: DB → internal API → resolver → ChatInfo → AgentRunRequest → orchestrator

### Identifikovaný gap

**Per-crew shared memory existuje v kódu, ale NENÍ FTS5-indexovaná.**

- `/crew/shared/` path je referencována v `crewshipSystemPreamble` (`exec.go:30`)
- Agent může číst/zapisovat soubory přes file access tooly
- **Ale `buildMemoryContext` je nenačítá do promptu** a search přes ně nefunguje
- Klient, který chce "crew má sdílené znalosti", to dnes nedostane

### Pavlova vize paměti

> "Uvnitř každého crew a každý agent bude mít vlastní paměť. Bude mít společnou crew paměť, ale každý agent bude mít ještě vlastní solo paměť, takže si bude pamatovat všechny věci, které dělal. Klient si tak bude dlouho opečovávat svůj vlastní 'mozek' — ten musí být zálohovatelný a hlavně musí být přenositelný do jiné crew/agent."

**Tři požadavky:**
1. Per-agent solo memory (MÁME, funguje)
2. Per-crew shared memory (PLUMBING JE, ale FTS5 chybí → **gap to fix**)
3. Backup + portability (NEEXISTUJE → **implementovat**)

### Přijaté řešení (zjednodušený plán)

**Phase A: Shared memory FTS5 fix** (1-2 dny práce)

**Cíl:** per-crew shared memory je vyhledávatelná a injectovaná do promptu stejně jako per-agent memory.

**Změny:**
- `internal/memory/engine.go` — přidat druhou instanci FTS5 engine pro `CrewShared()` cestu
- `internal/orchestrator/memory.go:25` (`buildMemoryContext`) — query oba engines s budget split (doporučeno 70 % agent, 30 % crew shared)
- `internal/orchestrator/exec.go:30` — aktualizovat system preamble aby agent věděl, že shared memory je FTS5-indexovaná
- Migrace: žádná (FTS5 index se generuje at-runtime)
- Test: `internal/memory/engine_shared_test.go`

**Proč tohle první:**
- Čistá přidaná hodnota bez závislostí
- Nedotýká se runnerů, orchestrace, ani provider abstrakcí
- Žádné riziko rozštěpení
- Okamžitě použitelné pro klienty

**Phase B: Memory portability (volitelná, může následovat)** (3-5 dní práce)

**Cíl:** klient může exportovat, importovat, přenést, backupovat agent brain.

**Komponenty:**
- Nový balíček `internal/memory/portable/` s `format.go`, `export.go`, `import.go`
- Formát `.crewship-brain` = tar.zst s manifestem:
  ```
  manifest.json         (schema_version, agent meta, source_crew, cursor, file checksums)
  AGENT.md
  daily/*.md
  notes/*.md
  attachments/*         (optional)
  index.sqlite          (optional, regenerable)
  ```
- CLI commands v `cmd/crewship/cmd_agent_memory.go`:
  - `crewship agent export <slug> [-o file.crewship-brain]`
  - `crewship agent import <file> --as <slug> [--crew <crew>]`
  - `crewship agent transfer <from> <to>` (convenience)
  - `crewship crew export-shared <slug>` / `import-shared`
  - `crewship agent backup list <slug>` / `restore <snapshot-id>`
- Auto-backup hook v `orchestrator.go` kolem řádku 634 (kde status flipne na `completed`)
  - Config: `memory.auto_backup_every=10runs` nebo `24h`
  - Retention: keep last N (default 10)
- DB migration v31:
  ```sql
  CREATE TABLE memory_backups (
      id TEXT PRIMARY KEY,
      agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
      storage_path TEXT NOT NULL,
      bytes INTEGER NOT NULL,
      checksum TEXT NOT NULL,
      kind TEXT NOT NULL,  -- 'auto' | 'manual'
      created_at TEXT NOT NULL DEFAULT (datetime('now'))
  );
  ```

**Bezpečnostní pravidla pro export:**
- Volitelný `--redact` flag přes `internal/scrubber/` odstraňuje credentials z exportu
- NIKDY neexportovat `/secrets/` adresář
- Manifest obsahuje hash contents pro tamper detection
- EE: HMAC signature s workspace key

**Proč toto druhé:**
- Je to produktový moat ("pestuj si vlastní agent brain, přenes ho kamkoli")
- Dnes to nikdo v konkurenci nemá jako first-class feature
- Memory je file-first → implementace je skoro zadarmo
- Nerozbíjí Docker flow, nedotýká se runneru

### Odložené / NEimplementované věci z původního plánu

Tyto komponenty byly v původním plánu navrženy, ale **odkládají se indefinitely**, protože byly vázané na localproc:

- Path resolver abstrakce (`internal/paths/`)
- System preamble templating
- AgentRunner interface
- Docker runner adapter
- Feature flag `CREWSHIP_RUNNER_EXPERIMENTAL`
- Linux/Windows support mimo Docker
- Remote runner (`cmd/crewship-runner/`)
- Sandbox profily (sandbox-exec, bwrap)
- Egress proxy refactor pro standalone použití
- Devcontainer.json adopce (viz Téma 3 — zůstává platné rozhodnutí, ale aplikuje se uvnitř Docker kontejneru, ne jako enabling work pro BYOE)

---

## Souhrn rozhodnutí

### Finální, k implementaci

| # | Rozhodnutí | Priorita | Odhad práce |
|---|---|---|---|
| 1 | **Licence: Apache 2.0 core + `/ee` (BUSL-1.1)** — zůstat u open-core modelu | Strategie | — |
| 2 | **Trademark "Crewship"** — EU + US | Strategie | Admin |
| 3 | **CLA pro contributors** — cla-assistant.io | Před externími PR | 0.5 dne |
| 4 | **Network izolace: dedikovaná `crewship-agents` Docker network** | **Kritické** | 1-2 dny |
| 5 | **Network allowlist: `FreeMode=false` jako default** | **Kritické** | 0.5 dne |
| 6 | **Audit Docker socket mount** — ověřit, že NENÍ | **Kritické** | 1 h |
| 7 | **Shared memory FTS5 fix** — per-crew memory indexována | High | 1-2 dny |
| 8 | **MVP crew backup: `crewship crew backup/restore`** | High | 2-3 dny |
| 9 | **Memory portability: `crewship agent export/import/transfer` + auto-backup** | High | 3-5 dní |
| 10 | **Devcontainer.json + mise adopce** (uvnitř Docker) | Medium | 3-5 dní |
| 11 | Dokumentace `/opt/crew-tools` a `/home/agent` jako install cesty | Medium | 1 den |

### Explicitně odmítnuto

- ❌ **Localproc runner** — security concerns (host access), risk rozštěpení kódu
- ❌ **Terraform / Ansible / Packer / Salt / Chef / Puppet** — licence nebo runtime deps
- ❌ **AGPL / SSPL licence** — adoption killer
- ❌ **Custom sandbox profily** (sandbox-exec, bwrap) — příliš křehké, závislost na Dockeru je čistší
- ❌ **Full cloud SaaS model** — v rozporu se self-hosted pozicí
- ❌ **`docker commit` based backup** — mísí security hranice, 10× větší backupy

### Odloženo do budoucna (opětovně zvážit při tržním signálu)

- ⏸ **BYOE / localproc** — pokud přijde masivní Mac/Windows friction feedback
- ⏸ **Path resolver + AgentRunner abstrakce** — jen pokud bude více providerů
- ⏸ **Remote runner** — feature candidate pro EE
- ⏸ **K8s provider** — Enterprise tier
- ⏸ **BuildKit pro per-crew custom images** — V2, až bude user demand

---

## Související dokumenty

- **Plan file** (tento session): `/Users/pavelsrba/.claude/plans/cozy-hopping-mitten.md` — původní BYOE plán (obsahuje i odmítnuté localproc části, zachováno jako historie)
- **Memory záznamy:**
  - `memory/project_container_provisioning_tools.md` — devcontainer/mise/BuildKit rozhodnutí
  - `memory/project_enterprise_features.md` — EE tier planning
  - `memory/project_timestamp_defaults_followup.md` — existující tech debt
- **PRD docs** (kontext):
  - `.claude/context/prd/SECURITY.md` — bude potřeba aktualizovat o network izolaci
  - `.claude/context/prd/SIDECAR.md` — network allowlist default změna
  - `.claude/context/prd/MEMORY.md` — shared memory FTS5 a portability
  - `.claude/context/prd/ADR.md` — nová ADR pro "Docker-first, no BYOE" rozhodnutí

## Další kroky

1. **Pavel rozhodne**, kterou z "Finální, k implementaci" položek chce řešit jako první
2. Pokud to bude **Shared memory fix** nebo **Network izolace**, připravit samostatné implementační plány s TDD přístupem (testy first)
3. Po dokončení každé významné práce aktualizovat relevantní `.claude/context/prd/*.md` docs
4. Linear task: **ZATÍM NEVYTVÁŘET** (Pavel explicitně vyžádal — chce si nejdřív přečíst tento dokument)
