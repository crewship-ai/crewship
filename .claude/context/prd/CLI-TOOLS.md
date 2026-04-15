# Crewship -- CLI Tools & Credentials (CLI-TOOLS.md)

**Verze:** 1.0
**Datum:** 2026-03-10

---

## 1. PŘEHLED

Crewship podporuje CLI tools (gh, glab, vercel, kubectl, terraform...) uvnitř
agent kontejnerů. Agent si tools nainstaluje sám za běhu (Claude Code styl).
Credentials pro CLI tools jsou spravovány přes stávající credential systém
s novým typem `CLI_TOKEN`.

### Klíčové principy

1. **Jeden base image** — žádné custom images, žádné flavors
2. **Agent si instaluje sám** — Claude Code nainstaluje CLI tools jako první krok
3. **Pre-run apt-get jako root** — system packages se instalují před startem agenta
4. **CLI credentials = env vars** — injektovány při Docker exec, agent je vidí
5. **Per-agent izolace** — Karel a Tomáš mají různé credentials

---

## 2. PODPOROVANÉ CLI TOOLS

| CLI Tool | Package | Env Var | Install metoda | Test endpoint |
|---|---|---|---|---|
| GitHub CLI (`gh`) | `gh` (apt) | `GH_TOKEN` | `apt-get install gh` | `GET https://api.github.com/user` |
| GitLab CLI (`glab`) | `glab` (binary) | `GITLAB_TOKEN` | Agent install (go/binary) | `GET https://gitlab.com/api/v4/user` |
| Vercel CLI | `vercel` (npm) | `VERCEL_TOKEN` | `npm install -g vercel` | `GET https://api.vercel.com/v2/user` |
| AWS CLI | `awscli` (pip) | `AWS_ACCESS_KEY_ID` | `pip install awscli` | `sts get-caller-identity` |
| kubectl | `kubectl` (apt) | `KUBECONFIG` | `apt-get install kubectl` | N/A (config file) |
| Terraform | `terraform` (apt) | `TF_TOKEN_*` | `apt-get install terraform` | N/A |

### Provider → env var auto-mapping

```
GITHUB     → GH_TOKEN
GITLAB     → GITLAB_TOKEN
VERCEL     → VERCEL_TOKEN
AWS        → AWS_ACCESS_KEY_ID
KUBERNETES → KUBECONFIG
CUSTOM_CLI → (uživatel zadá manuálně)
```

---

## 3. CREDENTIAL MODEL

### 3.1 Nové hodnoty (žádné schema změny)

`credentials` tabulka — TEXT sloupce `type` a `provider`:

```
type:     "CLI_TOKEN"   (nové, vedle API_KEY, AI_CLI_TOKEN, SECRET)
provider: "GITHUB"      (nové)
          "GITLAB"      (nové)
          "VERCEL"      (nové)
          "AWS"         (nové)
          "KUBERNETES"  (nové)
          "CUSTOM_CLI"  (nové)
```

### 3.2 Credential flow

```
1. Uživatel vytvoří credential v Settings → Credentials:
   Name:     "GitHub Karel"
   Type:     CLI Token
   Provider: GitHub
   Value:    ghp_xxxxxxxxxxxx    ← AES-256-GCM encrypted v DB

2. Uživatel přiřadí credential agentovi Karel:
   Agent Karel → Credentials tab → Assign Credential
   → Vybere "GitHub Karel"
   → Env var: GH_TOKEN (auto-suggested)

3. Karel spustí run:
   crewshipd čte agent_credentials pro Karla
   → Docker exec s env vars:
     GH_TOKEN=ghp_xxxxxxxxxxxx
     GITLAB_TOKEN=glpat-yyyyyyyy  (pokud má i GitLab)

4. Claude Code uvnitř kontejneru:
   - Vidí GH_TOKEN env var
   - Může volat `gh pr list`, `gh issue create` atd.
   - Nebo přímo GitHub API s tokenem
```

### 3.3 CLI_TOKEN vs SECRET

| | CLI_TOKEN | SECRET |
|---|---|---|
| Provider dropdown | Ano (GitHub, GitLab...) | Ne |
| Auto-suggest env var | Ano (GH_TOKEN...) | Ne |
| Test endpoint | Ano (volá provider API) | Ne |
| Injection | Vždy env var | Env var (bez Keeperu) / Keeper API (s Keeperem) |
| Scrubbing | Specifické patterny (ghp_*) | Generické patterny |

### 3.4 Proč CLI tokeny MUSÍ být env vars (ne sidecar proxy)

Sidecar proxy interceptuje HTTP requesty a přidává headers. Ale CLI tools:

1. Používají vlastní HTTP klienty (ne HTTP_PROXY)
2. `gh` čte `GH_TOKEN` env var při startu a používá ho pro VŠECHNY requesty
3. Sidecar by musel dešifrovat HTTPS (MITM) — nechceme
4. Domain allowlist v sidecaru CONNECT tunnel jen propouští, neinjektuje

**Bezpečnostní kompenzace:**
- Agent je izolovaný v kontejneru (nemůže exfiltrovat snadno)
- Network allowlist omezuje kam může volat
- Stdout scrubber redaktuje tokeny v logu (ghp_*, glpat-*)
- Per-agent izolace: Karel vidí JEN své tokeny, ne Tomášovy

---

## 4. CLI TOOLS INSTALLATION

### 4.1 Strategie: Agent si instaluje sám

MVP přístup — agent (Claude Code) dostane instrukci nainstalovat CLI tools
jako první krok. Toto je pattern používaný v OpenHands, SWE-agent, Devin.

**Výhody:**
- Zero image komplexita
- Maximální flexibilita (agent nainstaluje cokoliv)
- Vždy nejnovější verze
- Self-healing

**Nevýhody:**
- Čas při novém kontejneru (10-30s per tool)
- Token spotřeba (minimální — install je jednoduchý)
- Reinstalace po recyklaci kontejneru (OK pro MVP)

### 4.2 Pre-run install (apt-get jako root)

System packages vyžadující root se instalují PŘED startem agenta:

```go
func (o *Orchestrator) PreRunInstallPackages(ctx, containerID, packages) error {
    // Spustí apt-get install jako root (UID 0)
    exec := ExecConfig{
        Cmd:  []string{"sh", "-c", "apt-get update -qq && apt-get install -y -qq " + join(packages)},
        User: "0:0",  // ROOT — jen pro install
    }
    // Agent pak běží jako UID 1001 (non-root)
}
```

**Flow:**
```
1. Agent má cli_tools: ["gh", "glab"]
2. crewshipd detekuje apt packages: gh → apt
3. Pre-run: docker exec -u root apt-get install -y gh
4. Agent start: docker exec -u 1001:1001 claude --print "..."
5. Agent vidí `gh` v PATH, má GH_TOKEN v env
```

### 4.3 Package mapping

```
gh        → apt (apt-get install gh)
glab      → binary (agent sám, nebo pre-run download)
vercel    → npm (npm install -g vercel)
awscli    → pip (pip install awscli)
kubectl   → apt (apt-get install kubectl)
terraform → apt (apt-get install terraform)
```

### 4.4 Agent cli_tools sloupec

```sql
ALTER TABLE agents ADD COLUMN cli_tools TEXT;
-- JSON array: ["gh", "glab", "vercel"]
```

---

## 5. DB MIGRACE

```sql
-- Migrace: add_agent_cli_tools
ALTER TABLE agents ADD COLUMN cli_tools TEXT;
```

Žádné další schema změny. `credentials.type` a `credentials.provider`
jsou TEXT sloupce — nové hodnoty (CLI_TOKEN, GITHUB, GITLAB...) se
přidají pouze validací v Go kódu.

---

## 6. SECURITY

### 6.1 CLI token patterny v scrubber

```
ghp_[a-zA-Z0-9]{36}                  — GitHub PAT (classic)
github_pat_[a-zA-Z0-9]{22,}          — GitHub PAT (fine-grained)
gho_[a-zA-Z0-9]{36}                  — GitHub OAuth
ghs_[a-zA-Z0-9]{36}                  — GitHub App
ghr_[a-zA-Z0-9]{36}                  — GitHub Refresh
glpat-[a-zA-Z0-9_-]{20,}             — GitLab PAT
```

> Poznámka: GitHub patterny (ghp_, gho_, ghs_, ghr_, github_pat_) jsou
> v scrubber JIŽ IMPLEMENTOVANÉ. Přidáme jen GitLab (glpat-).

### 6.2 Network allowlist

Pokud agent má CLI credentials, sidecar domain allowlist automaticky
povolí odpovídající domény:

```
GITHUB     → api.github.com, github.com, *.githubusercontent.com
GITLAB     → gitlab.com, *.gitlab.com (+ custom host z GITLAB_HOST)
VERCEL     → api.vercel.com, vercel.com
AWS        → *.amazonaws.com
```

Toto je Phase 2 — pro MVP agent používá CONNECT tunnel přes sidecar,
který defaultně povoluje většinu domén.

---

## 7. UI ZMĚNY

### 7.1 Credentials page — Add Credential dialog

Rozšířit provider dropdown o nové providery:

```
Anthropic (Claude)     ← stávající
OpenAI (GPT / Codex)   ← stávající
Google (Gemini)         ← stávající
─────────────────────
GitHub                  ← NOVÉ
GitLab                  ← NOVÉ
Vercel                  ← NOVÉ
AWS                     ← NOVÉ
Custom CLI              ← NOVÉ
```

Nový type button: "CLI Token" vedle "AI CLI Token", "API Key", "Secret".

Při výběru CLI Token + provider → auto-fill name (env var):
- GitHub → GH_TOKEN
- GitLab → GITLAB_TOKEN
- etc.

### 7.2 Assign Credential dialog

Rozšířit ENV_VAR_PRESETS:

```typescript
const ENV_VAR_PRESETS = [
  "ANTHROPIC_API_KEY",
  "OPENAI_API_KEY",
  "GOOGLE_API_KEY",
  "GH_TOKEN",         // NOVÉ
  "GITLAB_TOKEN",     // NOVÉ
  "VERCEL_TOKEN",     // NOVÉ
]
```

### 7.3 Provider icons

Přidat GitHub, GitLab, Vercel, AWS ikony do PROVIDER_ICONS mapy.

---

## 8. FAZOVÁNÍ

| Fáze | Co | Kdy |
|---|---|---|
| **MVP (teď)** | CLI_TOKEN typ, nové providery, env var injection, pre-run apt, scrubber, UI | Nyní |
| **Phase 2** | Auto domain allowlist dle credentials, compound credentials (AWS), GitLab GITLAB_HOST | Po MVP |
| **Phase 3** | Official flavors (devops, fullstack), cache volume pro tools, Skill-driven auto-install | Později |
| **Phase 4** | Custom base image (derived Dockerfile), build-time injection | Mnohem později |

---

## 9. OTEVŘENÉ OTÁZKY

1. **GitLab self-hosted:** GITLAB_HOST env var — uložit jako metadata na credential, nebo druhá credential?
2. **AWS compound credentials:** AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY — 1 credential s 2 hodnotami, nebo 2 credentials?
3. **Domain allowlist auto-config:** Automaticky přidat domény dle provider? Nebo manuálně?
4. **Tool version pinning:** Potřeba pinovat verze CLI tools? Nebo vždy latest?

---

## 10. CREWSHIP BACKUP (CRE-125 / CRE-127)

Admin-only CLI pro zalohy workspace a crew. Plna specifikace v
`.claude/context/prd/BACKUP.md`. Strucny pristup:

```bash
crewship backup create --scope=workspace
crewship backup create --scope=crew --crew=<slug-or-id>
crewship backup list
crewship backup inspect <file>
crewship backup restore <file> [--as-workspace <new-slug>]
crewship backup delete <file>
```

Pozadavky:
- Role OWNER nebo ADMIN na workspaceu.
- Docker daemon dostupny pri `create` (pro pause/unpause/copy).
- Kontejner nesmi mit zadneho agenta se statusem `running` nebo
  `busy` behem `create`; jinak flow odmitne s jasnou hlaskou.

Bundly defaultne letí do `~/.crewship/backups/` a jsou sifrovane
pres AGE. Pro plnou dokumentaci restore mimo Crewship viz
`DEPLOYMENT.md` sekce "Restore mimo Crewship".
