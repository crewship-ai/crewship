# PRD — Credentials v2: recipe katalog, tool readiness, RBAC reveal

**Status:** návrh k odsouhlasení · **Datum:** 2026-07-28
**Navazuje na:** `CREDENTIALS-VAULT.md` (typový systém, mount chování, migrace v93)
**Cíl v jedné větě:** uživatel vloží secret, přiřadí ho ke crew — a agent v tom
crew má v kontejneru funkční CLI tool, bez jediného kroku uvnitř kontejneru.

---

## 0. Rozsah v1 — KISS (nadřazeno zbytku dokumentu)

> **Revize 2026-07-28 po připomínce:** první verze tohohle PRD navrhovala
> „recipe katalog" se ~150 značkami. To byla spekulativní obecnost. Vaultwarden
> má pět typů položek a nekonečno custom fields — žádný katalog značek — a
> pokryje tím cokoliv. Zaměnil jsem **typy** (šest) za **značky** (tisíce).
>
> Rozhodující argument: **žádný ze tří ověřených blockerů katalog nevyžaduje.**
> Katalog kupoval jen našeptání jména env proměnné a ikonky. Ikony už máme
> (`registry.ts`, 165 značek). Jméno env proměnné zná uživatel líp než my —
> špatný řádek v katalogu je horší než žádný.

### Co v1 je

| # | Věc | Velikost |
|---|---|---|
| 1 | **Crew → agent fanout.** Přiřazení ke crew reálně doručí credential všem agentům crew, i budoucím. | malá — vzor `autoAssignCredentials` už existuje |
| 2 | **Odpojit název od env proměnné.** `credentials.name` = lidská identita účtu; env var jde z bindingu. Odemyká 10 GitHub účtů. | malá — `agent_credentials.env_var_name` už je per-assignment |
| 3 | **Typy položek + custom fields** (Vaultwarden model). Šest typů, zbytek přes vlastní pole. | střední — nová tabulka `credential_fields` |
| 4 | **Nabídnout chybějící devcontainer feature.** Provider→feature má ~20 řádků; 28 už existuje v `crew_resources.go:227`. | malá |
| 5 | **Našeptávač env proměnné** — `credprovider` z 5 na ~30 řádků. Je to *nápověda*, ne brána; neznámé uživatel napíše. | triviální |
| 6 | **Dvě opravy:** Test tlačítko odgatovat z `brand.cli` na skutečnou podporu probe (V1); system prompt, který lže o návratové hodnotě Keepera (V4). | triviální |

### Co v1 není

Recipe katalog se 150 značkami · varianty per značka · probe per značka ·
readiness verify smyčka · L2 helper shimy (git-credential-crewship,
docker-cred-helper) · efemérní HOME · CI zero-disk gate.

Nic z toho není zahozené — je to §2.1–§2.5 níže jako reference, až se ukáže
reálná potřeba. **Ale nestaví se to teď a nesmí to blokovat v1.**

### Bezpečnost — také rozdělená

§2.6 popisuje desetivrstvý standard pro reveal. Do v1 jde **jádro**, které je
malé a nese většinu hodnoty:

- reveal defaultně **vypnutý** pro workspace (L1)
- capability `credentials:reveal`, ne role (L2)
- povinný důvod (L3.3)
- **řetězený audit jako podmínka** — selže zápis, selže reveal (L4)
- klasifikace `SEALED` = nikdy neodkrývat (L0)
- **rotace jako primární cesta v UI** (L8)
- agenti neodkrývají nikdy (L9)

Později: four-eyes, session freshness / TOTP, one-time token s 30s oknem,
auto-seal, anomálie. Ty vrstvy dávají smysl, ale jádro výše funguje i bez nich
a je to ~pětina práce.

---

## 0b. Proč tohle PRD vzniká

Dnešní stav zvládá „mám GH_TOKEN a agent ho vidí jako env proměnnou".
Nezvládá „dal jsem workspace GitHub secret a `gh` prostě funguje". Mezi tím
leží tři konkrétní, ověřené mezery — a žádná z nich není ta, kterou by člověk
čekal (nejsou to shimy ani zero-disk).

Všechna tvrzení níže jsou ověřená v kódu na commitu `dcc7acb1`, s `file:line`.

---

## 1. Ověřený současný stav

### 1.1 Co funguje dobře

| Vlastnost | Kde | Poznámka |
|---|---|---|
| Delivery policy per typ, fail-safe | `internal/credpolicy/credpolicy.go:62` | neznámý typ → `DeliveryNone` + Keeper-gated. Dobrý design. |
| `/secrets` je tmpfs + `rm -rf` po běhu | `internal/provider/docker/docker.go` (`secretsTmpfsSpec`), `internal/orchestrator/secrets_cleanup.go` | refcountované přes překryté běhy |
| Crew-scoped viditelnost pro MEMBER/VIEWER | `internal/api/credentials_loaders.go:25` | `scope='WORKSPACE' OR credential_crews ∩ crew_members` |
| Sidecar loopback callback | `internal/sidecar/keeper_bridge.go` | `127.0.0.1:9119`, `/keeper/request`, `/keeper/execute`, per-agent bearer |
| Lease gating (TTL grantů) | `credentialLeaseGateSQL`, `internal/api/internal_credentials.go:188` | expirovaný lease vypadne i z boot payloadu |
| Typový systém vč. souborových typů | `internal/api/credentials_types.go` | 10 typů, SSH 0600, certy 0400 |
| CLI pokrytí | `cmd/crewship/cmd_credential*.go` | list/get/create/update/rotate/delete/assign/unassign/audit/test-stored/default-env-var |

### 1.2 Blocker #1 — přiřazení ke crew **nedoručuje** credential

Tohle je nejdůležitější zjištění a přímo blokuje tvůj cíl.

Doručení do kontejneru čte **výhradně** `agent_credentials` (per-agent řádek),
a jméno env proměnné bere z `ac.env_var_name`:

- `internal/api/assignments.go:153` — sub-agent / hire boundary
- `internal/api/agent_config.go:631` — `resolveAgentCredentials`

`credential_crews` (tabulka „credential patří crew") ovlivňuje **jen
viditelnost v UI a v sidecar metadata listingu** (`credentials_loaders.go:39`,
`internal_credentials.go:200`). Nikde se nefanoutuje do `agent_credentials`.

**Důsledek:** „přiřadím heslo ke crew 1" dnes znamená „členové crew 1 ho uvidí
v UI". Agenti v crew 1 ho v kontejneru **nemají**. Musí se přiřadit každému
agentovi zvlášť.

Precedens pro fanout už v kódu je: `autoAssignCredentials`
(`internal/api/crew_templates.go:176`) při vzniku agenta z template vloží
`agent_credentials` řádky pro Anthropic klíče. Je to hardcoded na
`provider='ANTHROPIC'` a běží jen jednou při vzniku.

### 1.3 Blocker #2 — credential neví nic o toolu, tool neví nic o credentialu

Znalost je rozsypaná do **pěti nespojených registrů**, a ten největší nenese
žádnou injektážní sémantiku:

| Registr | Kde | Řádků | Nese injektáž? |
|---|---|---|---|
| Brand katalog | `lib/credential-providers/registry.ts` (**frontend**) | ~165 | ❌ ikona, barva, keywords, prefixy |
| Provider → env var | `internal/credprovider/credprovider.go:25` | 5 mapování / 11 providerů | ✅ ale skoro prázdné, sám sebe označuje jako „documentation-grade" |
| Typ → delivery kanál | `internal/credpolicy/credpolicy.go:62` | 10 | ✅ jen hrubý kanál, ne per-tool |
| Devcontainer feature → CLI binárka | `internal/api/crew_resources.go:227` | ~28 | ❌ používá se jen pro text system promptu |
| Feature katalog (tool surface) | `internal/devcontainer/catalog.go` + scrape `containers.dev/features` | 12 built-in + ~1000 upstream | ❌ o credentials neví |

Join `github-cli → gh → GH_TOKEN → credential` je **jeden hop**, rozdělený mezi
`crew_resources.go:234` (`"github-cli": "gh"`) a `credprovider.go:26`
(`"GITHUB": "GH_TOKEN"`). Ty dvě mapy o sobě nevědí.

**Důsledek:** join „který tool chce co" se dnes odehrává v hlavě člověka, který
ručně vyplní `agent_credentials.env_var_name`. Proto „plácnu GitHub tool a
funguje to" funguje jen pro `GH_TOKEN` — protože to tak někdo pojmenoval.

### 1.4 Blocker #3 — mít credential ≠ mít tool

Runtime image (`docker/crewship-sandbox/Dockerfile`) obsahuje: `git`, `curl`,
`jq`, `build-essential`, shelly. **Neobsahuje `gh`, `aws`, `kubectl`, `gcloud`,
`ansible`, `docker`.** Ty přicházejí jen tehdy, když crew ve svém devcontaineru
deklaruje odpovídající feature (`ghcr.io/devcontainers/features/github-cli:1`).

**Důsledek:** i po vyřešení blockerů #1 a #2 platí, že vložení GitHub secretu
nezpůsobí, že v kontejneru bude `gh`. Nikde neexistuje vazba
„credential provider GITHUB ⇒ crew potřebuje feature `github-cli`".

Tohle je ta část tvého cíle, kterou původní rešerše (secretless/shimy) vůbec
neadresovala.

### 1.5 Menší, ale konkrétní vady

| # | Vada | Kde | Dopad |
|---|---|---|---|
| V1 | Test tlačítko gated na `brand.cli`, které má **5 značek** (Anthropic, OpenAI, Google, Cursor, Factory) — ale server umí probe pro **8** (+ GITHUB, GITLAB, VERCEL) | `credential-form.tsx:355`, `credential-detail-sheet.tsx:207,313` vs `credentials_test_endpoint.go:47-210` | „connectivity nefunguje" — UI schovává tlačítko, které backend umí obsloužit |
| V2 | `$HOME` je na perzistentním svazku | `exec_env.go:23` → `/crew/agents/<slug>`; `/crew` bind mount `docker.go:702`; `/home/agent` named volume | `gh auth login` / `credential.helper=store` přežije běh → cache secretu na disku |
| V3 | `/keeper/execute` HOME neřeší vůbec | `internal/api/keeper_execute.go` | spadne na image `ENV HOME=/home/agent` — taky volume |
| V4 | System prompt lže agentovi | `agent_config.go:1399` „If ALLOW, the response contains the credential value" vs `internal/keeper/types.go` — žádné `Value` pole | agent čeká hodnotu, nedostane ji; tichý false-success |
| V5 | Model je jeden secret = jedna hodnota | schéma `credentials`: `encrypted_value` + `username` | AWS (key id + secret + region) nejde vyjádřit jedním záznamem; USERPASS je řešen speciálním případem na sloupci `username` |
| V6 | Zero-disk není nikde asertované | `scripts/test-harness/test-realworld-github.sh` (58 ř.) testuje jen `gh auth status` proti **veřejnému** repu | „zero-disk" je tvrzení, ne vlastnost |

---

## 2. Cílový model

### 2.1 Recipe katalog — jedna tabulka, server-side, jako data

Nahrazuje pět registrů z §1.3 jedním zdrojem pravdy v Go, exportovaným na
frontend endpointem. Frontend **přestane** držet vlastní 165řádkovou kopii.

```go
// internal/credcatalog/catalog.go — data v embedded JSON, ne v kódu
type Recipe struct {
    Key       string   // "GITHUB" — stabilní, jde do credentials.provider
    Label     string   // "GitHub"
    Category  string   // "Source" | "AI" | "Cloud" | "Comms" | "Data" | ...
    Icon      string   // simple-icons slug, FE si dohledá komponentu
    Hex       string
    DarkHex   string
    Keywords  []string // pro detekci z názvu
    Prefixes  []string // pro detekci z hodnoty ("ghp_", "github_pat_")

    // Co uživatel vyplňuje (§2.2)
    ItemType  string   // "TOKEN" | "USERPASS" | "SSH_KEY" | "FILE" | "JSON" | ...
    Fields    []Field  // multi-field: AWS = 3 pole

    // Jak se to doručí (§2.4)
    Delivery  []Target // env / file / helper / proxy

    // Které tooly to odemyká (§2.3)
    Tools     []ToolBinding

    // Jak se ověří, že žije
    Probe     *Probe   // metoda, URL, očekávaný status
}

type Field struct {
    Key      string // "access_key_id"
    Label    string // "Access key ID"
    Secret   bool   // false → uloženo cleartext (jako dnešní username)
    Required bool
    Pattern  string // volitelná validace
}

type Target struct {
    Mode    string // "env" | "file" | "helper"
    Name    string // "AWS_ACCESS_KEY_ID" nebo basename souboru
    From    string // který Field
    Mode0   string // 0400 / 0600 pro file
    Template string // pro file: šablona obsahu (npmrc, kubeconfig)
}

type ToolBinding struct {
    Binary  string // "gh"
    Feature string // "ghcr.io/devcontainers/features/github-cli:1"
    Verify  string // "gh auth status"  — příkaz pro readiness check
}
```

**Rozsah:** ~150 receptů pokryje reálný povrch. 1000 receptů netřeba — long tail
kolabuje, drtivá většina CLI čte jednu env proměnnou. Neznámá značka jede na
konvenci `<KEY>_TOKEN` / `<KEY>_API_KEY` + zachovává dnešní fail-safe.

**Zdroj dat:** mapování „který tool chce co" vytěžit z 1Password shell-plugins
(open-source) **jako data, po kontrole licence** — ne jako runtime závislost.
Storage, policy, RBAC i callback kanál už máme vlastní; Vault/1Password/CyberArk
jako dependency neberem.

**Migrace:** `credpolicy` zůstává (typ → kanál je ortogonální a fail-safe je
dobrý). `credprovider` a `crew_resources.featureToolNames` se pohltí do
katalogu. Frontend `registry.ts` se zredukuje na render helpery nad daty z API.

### 2.2 Typy položek — inspirace Vaultwarden, ale multi-field

Dnešní model „jedna hodnota + volitelný username" (V5) nestačí. Cílový tvar
kopíruje Bitwarden/Vaultwarden item model, ale s **custom fields jako
first-class** — to je přesně ta věc, která pokryje long tail 1000 nástrojů bez
1000 typů:

| Item type | Pole | Příklad |
|---|---|---|
| `TOKEN` | value | GH PAT, API klíč |
| `USERPASS` | username, password, (totp) | DB login, SMTP |
| `KEYPAIR` | access_id, secret, (region) | AWS static creds |
| `SSH_KEY` | private_key, (passphrase), (public_key) | git-over-SSH |
| `CERTIFICATE` | cert_pem, (key_pem), (ca_pem) | mTLS |
| `FILE` | filename, blob | GCP SA JSON, kubeconfig |
| `CONNECTION_STRING` | dsn | Postgres/Redis URL |
| `NOTE` | text | ne-secret poznámka |
| **custom fields** | N × {key, label, secret?} | cokoliv dalšího |

Pravidlo z existujícího PRD zůstává: **ne-tajné identifikátory jsou cleartext**
(username je identifikátor, ne secret) — kvůli search/sort bez per-row decryptu
a kvůli menší GCM ploše.

Schéma: nová tabulka `credential_fields (credential_id, key, encrypted_value,
is_secret, ordinal)`. Stávající `encrypted_value` + `username` na `credentials`
zůstávají jako pole `value` / `username` pro zpětnou kompatibilitu; migrace je
backfill, ne přepis.

### 2.3 Tool readiness — vazba credential → devcontainer feature

Tohle uzavírá blocker #3 a je to ta „na kliknutí" část.

1. Recept nese `Tools[].Feature` (OCI ref) a `Tools[].Verify` (příkaz).
2. Když se credential přiřadí ke crew, server spočítá **readiness diff**:
   které features crew potřebuje a nemá.
3. UI to zobrazí jako jednu akci: *„GitHub secret přiřazen. Crew `engineering`
   nemá `gh` — přidat github-cli feature? [Přidat a rebuildnout]"*.
4. Potvrzení zapíše feature do crew devcontaineru (`crew.Spec.Devcontainer.
   Features`, `internal/manifest/kinds/crew.go:195`) a spustí rebuild.
5. Po rebuildu běží `Verify` uvnitř kontejneru a výsledek se uloží jako
   **readiness stav** viditelný v UI i v `crewship credential list`.

Klíčové rozhodnutí: **feature se nepřidává automaticky bez potvrzení.** Rebuild
image je viditelná, časově drahá operace; tiché spuštění by bylo horší UX než
jedno kliknutí. „Na kliknutí" ano — „bez vědomí uživatele" ne.

### 2.4 Tři vrstvy doručení

| Vrstva | Mechanismus | Pokrývá | Soubor se secretem |
|---|---|---|---|
| **L1 env** | jen env proměnná | `gh`, většina API-key CLI, `aws` static, `pip`, `cargo` | ne |
| **L2 helper** | shim binárka v image volá `/keeper/token`, config jen odkazuje | `git` (GIT_ASKPASS / credential helper), `docker` (credHelpers), `kubectl` (exec:), `ssh` (agent socket) | ne |
| **L3 file** | recept vygeneruje soubor ze šablony do tmpfs, unlink po běhu | `gcloud` user-auth, AWS SSO, npm scoped `.npmrc`, GCP SA JSON, ansible-vault password file | ano, ale efemérně |

**Poctivá formulace slibu:** *žádný skript, který píše uživatel.* Doslovné
„nikdy žádný soubor" nejde — u L3 je soubor strukturálně nutný. Ale i tam ho
deklaruje recept jako šablonu a materializuje se do tmpfs při execu.

### 2.5 Zero-disk garance

1. `$HOME` efemérní (tmpfs) pro každou exec cestu — **včetně `/keeper/execute`**,
   které dnes HOME neřeší (V3).
2. Aktivně zabít disk-persistující cesty: `git config --system
   credential.helper` nastavit na náš helper (ne `store`), `gh auth login`
   blokovat / neproběhnout (env-only cesta ho nepotřebuje).
3. CI aserce (§4.3), bez které je zero-disk jen tvrzení.

Pozn.: perzistentní `$HOME` dnes drží i legitimní věci (shell history, agent
memory pod `/crew/agents/<slug>/.memory`). Efemérní HOME **nesmí** rozbít
memory — návrh: memory zůstává na `/crew/...` bind mountu, HOME se přesune na
tmpfs a memory cesta se předává explicitně (dnes už se předává,
`orchestrator_run.go:911`). Tohle je jediné místo návrhu, kde vidím riziko
regrese, a chce vlastní test (T-M1 v §4).

### 2.5b Multi-account — 10 crews, 10 GitHub účtů

Nejostřejší test celého návrhu. Odpověď: **jde to, ale jen když se rozpletou dvě
věci, které jsou dnes slepené do jedné.**

#### Proč to dnes nejde (ověřeno)

```sql
-- migrate_consts_v01_init.go:296
UNIQUE(workspace_id, name)
```

A zároveň konvence z `recipes.go:61`:

> *„EnvVarName is what the agent will see at runtime (GH_TOKEN, …).
> **Doubles as the credential name inside the workspace** per existing convention."*

Dohromady: název credentialu **je** jméno env proměnné, a musí být v rámci
workspace unikátní. Takže druhý GitHub účet by se taky musel jmenovat
`GH_TOKEN` → porušení UNIQUE. **Deset GitHub účtů v jednom workspace dnes
literálně nelze založit.** Tady má obava pravdu.

#### Proč to je opravitelné a není to hluboké

Jsou to dvě různé věci, které se historicky slily:

| Pojem | Co to je | Kde už dnes žije |
|---|---|---|
| **Identita účtu** | *který* účet u providera | `credentials.name` + `account_label` + `account_email` (sloupce **už existují**) |
| **Slot** | *pod jakým jménem* to přistane v kontejneru | `agent_credentials.env_var_name` — **per-assignment**, ne per-credential |

Doručovací vrstva už dnes klíčuje na to druhé a zvládá, aby víc credentialů
soupeřilo o stejný slot: `agent_credentials.go:64` řadí
`ORDER BY ac.env_var_name, ac.priority DESC`. Mechanismus tam **je**. Blokuje
to jen konvence „name = env var".

#### Cílový model

```
Recipe: GITHUB
  ├─ Variant: fine-grained PAT | classic PAT | OAuth | SSH key | GitHub App
  └─ Slot:    GH_TOKEN (env) + git credential helper

Credential = JEDEN ÚČET
  name:          github-acme          ← unikátní, lidské, NE env var
  provider:      GITHUB
  account_label: acme-bot
  variant:       fine-grained PAT

Binding = (scope, slot) → credential
  crew acme    → GITHUB/GH_TOKEN → github-acme
  crew globex  → GITHUB/GH_TOKEN → github-globex
  …10×
```

**Invariant:** v jednom resolution scope se jeden slot vyhodnotí na právě jeden
credential. Vynucené UNIQUE indexem **na bindingu**, ne na názvu credentialu.

**Pořadí vyhodnocení:** `agent > crew > workspace`. Nejkonkrétnější vyhrává;
`agent_credentials.priority` už existuje jako rozhodčí při shodě.

**Zpětná kompatibilita:** credential bez bindingu se doručí pod svým názvem —
tedy dnešní chování. Migrace nic nepřejmenovává; jen přestane odvozovat env var
z názvu u nově zakládaných.

#### Poctivá hranice — kde má obava pravdu i po opravě

**V jednom kontejneru existuje jen jedna výchozí identita na nástroj.** `gh`
čte `GH_TOKEN` a nic jiného. 10 crews × 10 účtů je v pohodě. Ale *jedna* crew,
která potřebuje dva GitHub účty najednou, na to narazí: druhý účet nemůže být
výchozí a musí dostat explicitní název slotu (`GH_TOKEN_READONLY`) — a použije
se jen tam, kde nástroj umí explicitní volbu (`gh --hostname`, git per-remote
config, `AWS_PROFILE`).

Proto recept nese `multi_account: true|false` a UI to říká rovnou, místo aby to
uživatel zjistil až selháním. **Slib, který udržíme:** jedna identita na nástroj
na scope, transparentně. Víc identit v jednom scopu jen tam, kde to nástroj sám
umožňuje — a my řekneme které.

#### Volba typu secretu (varianty)

Krok 1 onboardingu zůstává **nástroj** (to je otázka, kterou si uživatel klade).
Typ secretu je **varianta uvnitř receptu**, protože jeden nástroj má víc tvarů:
GitHub umí fine-grained PAT, classic PAT, OAuth, SSH klíč i GitHub App — každý
s jinými poli a jiným doručením.

Varianta se předvybere detekcí z vložené hodnoty (`ghp_` → classic PAT,
`github_pat_` → fine-grained, `-----BEGIN OPENSSH` → SSH klíč) a jde kdykoliv
přepnout ručně.

### 2.6 RBAC, discovery a reveal — bezpečnostní standard

Reveal je jediná nová bezpečnostní plocha v celém PRD a zároveň nejcitlivější
operace v produktu. Návrh níže je psaný jako **referenční standard**, na který
se má zbytek Crewshipu odkazovat, ne jako minimální splnění požadavku.

**Vedoucí princip: reveal není oprávnění, je to obřad.** Role samotná ho nikdy
neuděluje. Reveal projde jen tehdy, když se sejde *pět* nezávislých podmínek —
klasifikace, workspace switch, capability, čerstvá interaktivní session a
zapsaný důvod. To je záměrné: každá z nich je jinak zranitelná a útočník musí
prolomit všechny.

#### Co už existuje a stavíme na tom

| Stavební kámen | Kde | Jak se použije |
|---|---|---|
| Role `OWNER/ADMIN/MANAGER/MEMBER/VIEWER` | `workspaces_member_role.go:76` | nutná, ne postačující podmínka |
| Crew-scoped discovery | `credentials_loaders.go:25` | beze změny |
| Per-crew elevace MANAGERa | `rbac.go:175` | beze změny |
| Capability JSON na membershipu | migrace v109, `workspace_members.capabilities` | nosič `credentials:reveal` |
| **Journal s HMAC hash-chainem** | `internal/journal/emit.go:62`, `verify.go` | tamper-evidentní audit revealu |
| Signed checkpoints při mazání | `journal/verify.go:38` | mazání auditu nejde zamaskovat |
| **Keeper ESCALATE → inbox** | `keeper_phase2.go:227` `insertKeeperInbox` | mechanismus druhého schvalovatele |
| Rotace s grace overlapem | migrace v70 | podklad pro §L8 (rotace místo revealu) |
| Scrubber (~17 regexů) | `internal/scrubber` | egress hygiena |
| Runtime-laditelné rate limitery | `internal/ratelimitcfg` | vlastní bucket pro reveal |

Co v repu **není** a musí vzniknout: MFA/TOTP ani step-up re-auth neexistuje
(ověřeno — žádný `totp`/`mfa`/`reauth` v `internal/auth` ani `internal/api`).
Session freshness (§L3) je proto reálná práce, ne konfigurace.

#### L0 · Klasifikace citlivosti

Každý credential nese `sensitivity`. Výchozí hodnotu dává recept, uživatel ji
smí **zvýšit kdykoliv, snížit jen jako auditovanou a schválenou akci**.

| Třída | Reveal | Typický obsah |
|---|---|---|
| `STANDARD` | OWNER/ADMIN s obřadem | dev tokeny, read-only klíče |
| `RESTRICTED` | + druhý schvalovatel (four-eyes) | produkční API klíče, deploy klíče |
| `SEALED` | **nikdy, žádnou rolí** — jen rotace a výměna | produkční DB, root creds, cokoliv vytvořené agentem |

`SEALED` je záměrně bez únikové cesty. Break-glass pro sealed credential není
reveal, ale rotace: vytvoř novou hodnotu, starou nech doběhnout v grace okně.

#### L1 · Default deny na úrovni workspace

Reveal je pro celý workspace **vypnutý, dokud ho OWNER v Settings nezapne**.
Zapnutí je samo o sobě journalovaná událost a notifikuje všechny adminy.
Čerstvě založený workspace tedy nemá reveal vůbec — je to vědomé rozhodnutí,
ne výchozí stav.

#### L2 · Žádné trvalé oprávnění

Reveal vyžaduje capability `credentials:reveal` na membershipu (v109 JSON pole).
Role ji **nezakládá automaticky** — ani OWNER. Přidělení capability je
journalované a je vidět v Settings. Doporučený default pro korporát: capability
drží 2 lidé, ne celý ADMIN tým.

#### L3 · Obřad jednoho revealu

1. **Jen interaktivní lidská session.** Endpoint je nedosažitelný přes API
   token, přes internal/sidecar token i přes neinteraktivní CLI. (Test T-R7.)
2. **Čerstvost session** — poslední ověření musí být mladší než N minut
   (default 15); jinak re-auth heslem. Toto je nová práce (viz výše).
3. **Povinný důvod** — volný text, minimální délka, ukládá se do auditu.
   Prázdný nebo generický („test") důvod se odmítá.
4. **Four-eyes pro `RESTRICTED`** — žádost jde přes existující Keeper inbox
   (`insertKeeperInbox`, TargetRole MANAGER+), má TTL, a **schvalovatel nesmí
   být žadatel**. Vypršelá žádost se nedá oživit, jen podat znovu.
5. **Jednorázové zobrazení** — odpověď je krátkodobý one-time token, hodnota se
   ukáže jednou, okno 30 s, žádné znovuvyžádání bez nového obřadu.
6. **Hlavička `Cache-Control: no-store`**, hodnota nikdy do URL, nikdy do GET.

#### L4 · Audit jako podmínka, ne vedlejší efekt

Reveal se zapisuje do `internal/journal` (HMAC řetěz, signed checkpointy,
verifier) — **ne pouze** do `credential_audit`, což je plochá tabulka bez řetězu
(`migrate.go:1097`).

Zápis je **precondition**: když se řetězený zápis nepovede, reveal selže
uzavřeně (500) a hodnota se nevrátí. Nikdy „vrátíme hodnotu a audit zkusíme
potom". (Test T-R5.)

Do payloadu journalu jde: actor, cíl, klasifikace, důvod, IP, schvalovatel,
výsledek. **Nikdy hodnota ani její hash** — hash by umožnil offline slovníkový
útok na krátké secrety.

#### L5 · Out-of-band alerting

Každý reveal notifikuje adminy workspace + zakladatele credentialu existujícím
notify pipeline. Důvod: ukradená admin session nesmí exfiltrovat *potichu*.
Notifikace je best-effort (neblokuje reveal, aby výpadek Discordu nebyl DoS na
legitimní práci), ale **selhání notifikace je samo journalované**.

#### L6 · Rate limit a anomálie

Vlastní bucket v `ratelimitcfg`, řádově jednotky za hodinu — ne obecných
120/min. Překročení prahu vyvolá alert a volitelně **auto-seal**: credential se
dočasně překlopí do `SEALED`, dokud to admin nerozhodne. Vzor bere z
`admin_security_posture.go:159`, které už dnes hlásí „`/credentials/test` je
validation oracle, když je limiter vypnutý".

#### L7 · Oddělení pravomocí

Kdo smí odkrývat, nesmí spravovat audit. `credentials:reveal` a správa
journalu/retence jsou **vzájemně vylučující se capability** — držení obou je
konfigurační chyba, kterou Settings hlásí jako varování. Mazání journalu už dnes
vyžaduje podepsaný checkpoint (`journal/verify.go:38`), takže tichý úklid stop
po sobě nejde.

#### L8 · Lepší výchozí cesta: rotovat místo odkrývat

Většina legitimních důvodů pro reveal zní „potřebuju tu hodnotu někam vložit".
Na to je **lepší nástroj rotace**, kterou už máme včetně grace overlapu (v70):

> Vygeneruj novou hodnotu → ukaž ji jednou při vzniku → stará doběhne v grace okně.

UI proto nabízí **„Rotovat a zobrazit novou"** jako primární akci a
„Odkrýt stávající" až jako sekundární, s vysvětlením rozdílu. Tohle je
nejsilnější prvek celého návrhu: snižuje *objem* revealů, a kontrola, která se
používá zřídka, je kontrola, která reálně funguje.

#### L9 · Agenti neodkrývají nikdy

Reveal endpoint je z kontejneru nedosažitelný — sidecar ani internal token na
něj nedosáhne. Agenti mají jedinou cestu: Keeper. Zároveň se tím opravuje V4
(system prompt dnes agentovi slibuje hodnotu, kterou Keeper nevrací).

#### L10 · Egress hygiena

Odkrytá hodnota nesmí skončit v: logu, telemetrii, crash reportu (Sentry),
payloadu journalu, těle notifikace, ani v odpovědi zachycené proxy. Scrubber
(`internal/scrubber`) se aplikuje na všechny tyto cesty a má na to vlastní test.

#### Discovery zamčených credentials

**Default: vypnuto.** MEMBER nevidí ani jméno credentialu mimo svůj scope —
samotný název (`PROD_DB_DSN`) nese informaci. Workspace si to může zapnout
v Settings, pokud upřednostní objevitelnost („vím, že to existuje, můžu
požádat"). Žádost jde přes Keeper escalation.

*(Toto obrací můj původní návrh — po vyhodnocení citlivosti je bezpečnější
default nezobrazovat.)*

#### Kde se to konfiguruje

Veškerá politika výše žije v **Settings → Access & Secrets**, ne na stránce
Credentials. Důvod: je to workspace-wide bezpečnostní politika, ne vlastnost
jednoho secretu, a vidět ji má jen úzký okruh rolí.

- **Edituje:** OWNER, ADMIN
- **Vidí read-only:** MANAGER — musí znát pravidla, pod kterými pracuje, ale
  nesmí je uvolnit
- **Nevidí vůbec:** MEMBER, VIEWER

---

## 3. Wireframe / UX

Soubor: `.claude/context/wireframes/credentials-v2.html` (9 obrazovek).

**Ikony:** ve wireframu zástupné (`GH`, `✦`) kvůli čitelnosti drátěnky.
V implementaci je ikona povinná součást receptu (`Icon` + `Hex`/`DarkHex`) a
renderuje se všude: v seznamu, v katalogu, v detailu, v readiness pillech i
v Settings. Značka bez ikony do katalogu nepatří.

Struktura kopíruje Integrations (levý sidebar), ale s jednou **inverzí toku**:

> Dnes: přidám *secret* → hádám env proměnnou → doufám, že agent to najde.
> Nově: vyberu *nástroj* → systém řekne, která pole chce → ukáže, co to odemkne.

Obrazovky:

1. **Přehled** — levý sidebar (kategorie + „potřebuje pozornost" + scope), hlavní panel = seznam s readiness stavem
2. **Onboarding krok 1** — katalog nástrojů, search, „nevidím svůj nástroj → generic"
3. **Onboarding krok 2** — formulář generovaný z receptu (multi-field), auto-detekce z vloženého tokenu
4. **Onboarding krok 3** — scope & přiřazení (workspace / crew / agent) + readiness diff („crew nemá `gh`")
5. **Onboarding krok 4** — verifikace: probe + tool readiness + „co tohle odemyká"
6. **Detail existujícího** — parametry, pole, přiřazení, rotace, audit; „rotovat a zobrazit novou" je primární akce, „odkrýt stávající" sekundární
7. **RBAC pohledy** — vedle sebe co vidí OWNER / MANAGER / MEMBER
8. **Settings → Access &amp; Secrets** — celá politika z §2.6 na jednom místě, vč. varování na porušené oddělení pravomocí
9. **Obřad odkrytí** — pět podmínek, důvod, čekání na schvalovatele, a „co se stane / co se nikdy nestane"

---

## 4. Testovací plán

Pravidlo repa: **failing test first**. Každá položka níže musí červenat na
současném `main`.

### 4.1 Unit (Go)

| ID | Test | Kde | Asertuje |
|---|---|---|---|
| T-C1 | Recipe katalog: každý recept má neprázdný `Key`, validní kategorii, alespoň jeden `Target` | `internal/credcatalog/catalog_test.go` | integrita dat |
| T-C2 | Každý `Target.From` odkazuje na existující `Field` | tamtéž | žádné visící reference |
| T-C3 | Neznámý provider → generic recept, nikdy panic, nikdy `DeliveryNone` mlčky | tamtéž | fail-safe zachován |
| T-C4 | Křížový test: každý `credpolicy` typ má aspoň jeden recept, který ho používá | `credcatalog/crosscheck_test.go` | zámek proti driftu (vzor po `credpolicy` × `credentials_types`) |
| T-C5 | `featureToolNames` a `credprovider.defaultEnvVars` jsou podmnožinou katalogu | tamtéž | migrace nic neztratila |
| T-F1 | Fanout crew → agent: přiřazení ke crew vytvoří `agent_credentials` pro všechny agenty crew | `internal/api/credentials_fanout_test.go` | blocker #1 |
| T-F2 | Nový agent v crew zdědí crew credentials při vzniku | tamtéž | |
| T-F3 | Odebrání z crew smaže odvozené grantu, ale **ne** ručně přiřazené | tamtéž | nechceme mazat explicitní intent |
| T-F4 | Fanout respektuje lease TTL | tamtéž | |
| T-M1 | **Efemérní HOME nerozbije memory** — agent zapíše memory, kontejner restart, memory tam je | `internal/orchestrator/exec_env_home_test.go` | jediné regresní riziko §2.5 |
| T-M2 | `BuildEnvVars` i `BuildEnvVarsSidecar` emitují HOME na tmpfs cestu | tamtéž | |
| T-M3 | `/keeper/execute` nastavuje stejný HOME jako běžný exec | `internal/api/keeper_execute_home_test.go` | V3 |
| T-V1 | Multi-field credential: AWS recept vyprodukuje 3 env proměnné z jednoho záznamu | `internal/orchestrator/exec_env_multifield_test.go` | V5 |
| T-V2 | Cleartext pole se neukládá zašifrovaně a naopak | `internal/api/credential_fields_test.go` | |
| T-A1 | **10 GitHub credentialů v jednom workspace jde založit** (různá jména, stejný recept) | `internal/api/credentials_multiaccount_test.go` | §2.5b — dnes červená na UNIQUE |
| T-A2 | 10 crews × vlastní GitHub binding → každý kontejner dostane **svůj** `GH_TOKEN` | tamtéž | jádro use-case |
| T-A3 | Dva bindingy na stejný slot ve stejném scopu → 409, ne tichý poslední-vyhrává | tamtéž | invariant |
| T-A4 | Resolution: agent přebije crew, crew přebije workspace | `credentials_resolution_test.go` | pořadí |
| T-A5 | Credential bez bindingu se doručí pod svým názvem (dnešní chování) | tamtéž | zpětná kompatibilita |
| T-A6 | Druhý účet ve stejném scopu s explicitním slotem `GH_TOKEN_READONLY` projde | tamtéž | poctivá hranice |
| T-A7 | Detekce varianty: `ghp_` → classic, `github_pat_` → fine-grained, PEM → SSH klíč | `credcatalog/variant_test.go` | volba typu secretu |
| T-A8 | Recept s `multi_account: false` odmítne druhý binding do stejného scopu s vysvětlením | tamtéž | říct to dřív, než to selže |
| T-R1 | Reveal: čerstvý workspace → 403, dokud OWNER nezapne switch | `internal/api/credentials_reveal_test.go` | L1 default deny |
| T-R2 | Reveal: ADMIN **bez** capability `credentials:reveal` → 403 | tamtéž | L2 — role není postačující |
| T-R3 | Reveal: MEMBER/VIEWER 403 i pro credential, který vidí | tamtéž | |
| T-R4 | Reveal: MANAGER 403 pro credential mimo jeho crew scope | tamtéž | |
| T-R5 | Reveal: cross-tenant 404 (ne 403 — neprozrazovat existenci) | tamtéž | |
| T-R6 | Reveal: `SEALED` → 403 pro **každou** roli včetně OWNERa | tamtéž | L0 — žádná úniková cesta |
| T-R7 | Reveal: API token, internal token i sidecar → 403 | `credentials_reveal_authpath_test.go` | L9 — agenti nikdy |
| T-R8 | Reveal: session starší než freshness okno → 401 s výzvou k re-auth | tamtéž | L3.2 |
| T-R9 | Reveal: prázdný / příliš krátký důvod → 400 | tamtéž | L3.3 |
| T-R10 | Reveal `RESTRICTED`: bez schválení 202 (pending), se schválením 200 | `credentials_reveal_fourEyes_test.go` | L3.4 |
| T-R11 | Four-eyes: schvalovatel == žadatel → 403 | tamtéž | oddělení rolí |
| T-R12 | Four-eyes: vypršelá žádost nejde oživit, jen podat znovu | tamtéž | |
| T-R13 | Reveal: druhé volání téhož one-time tokenu → 410 | tamtéž | L3.5 |
| T-R14 | **Chained audit selže → 500 a hodnota se nevrátí** | `credentials_reveal_audit_test.go` | L4, fail-closed |
| T-R15 | Journal payload revealu neobsahuje hodnotu **ani její hash** | tamtéž | L4 |
| T-R16 | Řetěz journalu po revealu projde `journal.Verify` | tamtéž | tamper-evidence |
| T-R17 | Selhání notifikace reveal nezablokuje, ale zapíše se do journalu | `credentials_reveal_notify_test.go` | L5 |
| T-R18 | Překročení reveal bucketu → 429 + alert (+ auto-seal, když zapnuto) | `credentials_reveal_ratelimit_test.go` | L6 |
| T-R19 | Držení `credentials:reveal` **i** správy journalu → Settings hlásí varování | `settings_access_secrets_test.go` | L7 |
| T-R20 | Snížení klasifikace je auditovaná akce; zvýšení ne | `credentials_sensitivity_test.go` | L0 |
| T-R21 | Odkrytá hodnota se neobjeví v logu, telemetrii, crash reportu ani notifikaci | `credentials_reveal_egress_test.go` | L10 |
| T-R22 | Discovery zamčených: default vypnuto — MEMBER nedostane ani název | `credentials_discovery_test.go` | bezpečný default |
| T-P1 | Probe existuje pro každý recept, který má `cli`/`Probe` | `credentials_test_endpoint_test.go` | V1 |

### 4.2 Frontend (Vitest + Playwright)

| ID | Test | Asertuje |
|---|---|---|
| T-U1 | Onboarding: vložení `ghp_...` předvybere GitHub recept | auto-detekce z prefixu |
| T-U2 | Formulář se generuje z receptu — AWS ukáže 3 pole, GitHub 1 | data-driven forma |
| T-U3 | Test tlačítko se zobrazí pro GITHUB (dnes schované) | V1 |
| T-U4 | Readiness diff se zobrazí, když crew nemá potřebnou feature | §2.3 |
| T-U5 | MEMBER nevidí reveal ani edit; vidí „požádat" | RBAC v UI |
| T-U6 (e2e) | Celý flow: přidat GitHub PAT → přiřadit crew → potvrdit feature → readiness zelená | hlavní use case |

### 4.3 Runtime harness — `scripts/test-harness/`

Tohle je ten „universal use case" test podle CLAUDE.md. Nový soubor
`test-secretless-github.sh`, rozšiřuje dnešní `test-realworld-github.sh`.

| ID | Krok | Asertuje |
|---|---|---|
| T-H1 | Čerstvý workspace → vlož GitHub PAT → přiřaď crew → agent v crew spustí `gh auth status` | **funguje bez jediného kroku uvnitř kontejneru** |
| T-H2 | Nikdy neproběhl `gh auth login` | grep historie příkazů + absence `hosts.yml` |
| T-H3 | **Zero-disk**: grep hodnoty tokenu napříč FS kontejneru → 0 hitů | `/home/agent`, `$HOME`, `~/.config/gh/hosts.yml`, `~/.git-credentials`, `~/.docker/config.json`, `/secrets` |
| T-H4 | `git clone` privátního repa přes HTTPS projde, `.git-credentials` nevznikne | L2 helper |
| T-H5 | `git push` do privátního repa projde | write scope |
| T-H6 | `docker login ghcr.io` + `docker pull` týmž PATem, `~/.docker/config.json` obsahuje jen odkaz na helper | L2 |
| T-H7 | git-over-SSH přes agent forwarding, klíč nikdy na disku | SSH_KEY typ |
| T-H8 | **Revocation**: zneplatnit token v Crewshipu → další exec selže | nejsilnější důkaz, že nikde neleží zaschlá kopie |
| T-H9 | Cross-crew izolace: agent v crew B nevidí credential crew A | scope |
| T-H10 | Efemérní HOME: agent zapíše memory, restart kontejneru, memory přežije | T-M1 v reálu |
| T-H11 | Multi-field: AWS creds → `aws s3 ls` na test bucket projde | V5, L1 |
| T-H12 | L3: npm scoped registry — `.npmrc` vznikne v tmpfs a po běhu zmizí | L3 |
| T-H13 | Agent se pokusí zavolat `/credentials/{id}/reveal` z kontejneru → odmítnuto | L9 |
| T-H14 | Agent se pokusí přesvědčit Keepera, ať mu vrátí hodnotu `SEALED` credentialu → odmítnuto | L0 + red-team |
| T-H15 | Po revealu projde `crewship journal verify` — řetěz nenarušen | L4 |

Poslední tři patří k `test-redteam-insider.sh` / `test-keeper-audit-integrity.sh`,
které už existují — rozšířit je, ne zakládat nové.

### 4.4 CI gate

Nový job „Secretless assertion": po každém credentialed tool callu v harness
udělá `docker exec` a assertuje prázdné `/secrets/<slug>` + nulový grep tokenu.
Dnes `logCredentialExposures` (`exec_env.go:574`) plaintext expozici jen loguje
— tohle ji povyšuje na failující gate.

---

## 5. Co potřebuju od tebe (účty a fixtures)

### Minimum pro první validní běh (pokryje `gh` + git HTTPS/SSH + GHCR)

1. **Dummy GitHub účet** (např. `crewship-test-bot`) — throwaway, nic sdíleného s tvým osobním
2. **Privátní repo pod ním** (`secretless-test`) s 2–3 commity — pro clone/push/pull
3. **Fine-grained PAT** scoped **jen na to repo**, Contents: Read+Write
4. **Classic PAT** (`repo` scope) — pro srovnání obou tvarů tokenu
5. **SSH keypair** přidaný do SSH keys toho účtu — pro T-H7

> Ten samý PAT poslouží i na `docker login ghcr.io` (T-H6), takže GHCR není třeba řešit zvlášť.

**Jak mi je předat:** ne do chatu a ne do repa. Do `.env.local` klonu jako
`SEED_GITHUB_TOKEN` / `SEED_GITHUB_TOKEN_CLASSIC` / `SEED_GITHUB_SSH_KEY` +
`SEED_GITHUB_TEST_REPO`. Harness je čte odtud a suite SKIPne, když chybí.

### Volitelné — dokázat celých „80 %"

6. **AWS**: throwaway IAM user, programmatic access key, policy scoped na jeden test S3 bucket → T-H11 (multi-field)
7. **npm**: read-only token na scoped registry → T-H12 (L3 must-be-file)
8. **Dummy HTTP endpoint** s bearer tokenem (stačí echo) → generic recept

### Jen dokumentačně (netestovat)

9. **GCP** service-account key JSON — potvrdit file-requirement; zero-disk jde jen přes Workload Identity Federation na GCP compute, mimo GCP ne

---

## 6. Fázování

### v1 — to, co se staví teď

| PR | Obsah | Závislost |
|---|---|---|
| **P1** | Dvě drobné opravy: V1 (Test tlačítko) + V4 (lživý keeper prompt) | žádná — shippable hned |
| **P2** | Crew → agent fanout (blocker #1). Testy T-F1…T-F4. | žádná |
| **P3** | Odpojit název od env proměnné + binding `(scope, slot) → credential`. Testy T-A1…T-A6. | P2 |
| **P4** | `credential_fields` — typy položek + custom fields. Testy T-V1, T-V2. | P3 |
| **P5** | Nabídnutí chybějící devcontainer feature + našeptávač env var (~30 řádků). | P3 |
| **P6** | UI: sidebar, onboarding, detail podle wireframu. | P4, P5 |
| **P7** | Reveal — jádro (L0, L1, L2, L3.3, L4, L8, L9). Testy T-R1…T-R6, T-R14…T-R16, T-R20. | P6 |
| **P8** | Harness `test-secretless-github.sh` — T-H1…T-H9 bez zero-disk částí. | P5 |

### Později — až se ukáže reálná potřeba

Recipe katalog · varianty · probe per značka · readiness verify smyčka ·
L2 shimy · efemérní HOME · CI zero-disk gate · zbývající vrstvy revealu
(four-eyes, freshness/TOTP, one-time token, auto-seal).

Zero-disk garance je jediná z odložených věcí, u které si nejsem jistý, jestli
smí čekat — je to bezpečnostní vlastnost, ne pohodlí. Ale je pravda, že dnešní
stav (secret v env proměnné, `/secrets` na tmpfs) je srovnatelný s tím, co dělá
většina CI platforem, takže to není akutní regrese. **Rozhodnutí k potvrzení.**

---

## 7. Rozhodnutí

| # | Otázka | Rozhodnuto |
|---|---|---|
| 1 | Fanout sémantika | „Přiřazeno ke crew" **doručuje** všem agentům crew, i budoucím. To je očekávané chování. |
| 2 | Auto-add devcontainer feature | Ukázat diff + jedno kliknutí. Tiché přidání ne — rebuild je drahá viditelná operace. |
| 3 | Reveal pro MANAGER | **Ne defaultně.** Reveal drží capability, ne role. MANAGER ji může dostat explicitně, ale jen ve svém scopu a nikdy pro `SEALED`. |
| 4 | Discovery zamčených credentials | **Default vypnuto.** Název secretu sám nese informaci. Opt-in per workspace. |
| 5 | Kde se politika konfiguruje | **Settings → Access & Secrets**, ne v Credentials. Edituje OWNER/ADMIN, čte MANAGER, MEMBER/VIEWER nevidí. |

### Zbývá rozhodnout

1. **Session freshness okno** — default 15 min? Kratší je bezpečnější, ale
   otravnější; delší dělá z ukradené session větší problém.
2. **Auto-seal po překročení rate limitu** — zapnout defaultně (bezpečnější,
   ale může zablokovat legitimní práci v incidentu), nebo jen alert?
3. **Re-auth mechanismus** — heslo stačí, nebo rovnou stavět TOTP? V repu není
   ani jedno; TOTP je větší kus práce, ale bez něj je re-auth slabý, když
   útočník už heslo má.
