# Crewship -- Network Policy (NETWORK-POLICY.md)

**Verze:** 1.0
**Datum:** 2026-03-10
**Status:** NAVRH

---

## 1. MOTIVACE

Crewship kontejnery aktuálně mají **neomezený přístup k internetu**. Docker bridge network
je vytvořen s `Internal=false`, sidecar proxy injektuje credentials ale **nefiltruje**
odchozí provoz. Agent může komunikovat s libovolnou doménou.

Pro produkční nasazení potřebujeme možnost **omezit síťový přístup** na úrovni crew,
aby administrátoři mohli kontrolovat, kam agenti mohou posílat data.

### 1.1 Současný stav

```
Container (Docker bridge, Internal=false)
├── Agent (UID 1001) → může přistoupit KAMKOLI na internet
└── Sidecar (UID 1002)
    ├── Injektuje credentials pro LLM API
    ├── DomainAllowlist existuje, ale blokuje jen non-allowlisted
    └── DefaultAllowedDomains: api.anthropic.com, api.openai.com, googleapis.com, api.factory.ai
```

**Problém:** Sidecar allowlist funguje jen pro HTTP requesty procházející proxy.
Agent může obejít proxy přímým TCP spojením (pokud zná IP adresu), nebo
může poslat data na libovolnou doménu přes HTTP proxy (sidecar forwarding povolí
i domény bez credential injection -- jen bez klíčů).

### 1.2 Cíl

Crew-level nastavení síťového režimu:
- **Free** (default): neomezený internet -- současné chování
- **Restricted**: sidecar blokuje vše kromě allowlisted domén

---

## 2. NÁVRH

### 2.1 Crew-level konfigurace

Network mode se nastavuje **per-crew** (ne per-agent), protože všichni agenti
v crew sdílejí jeden kontejner.

```
crews table:
  + network_mode TEXT NOT NULL DEFAULT 'free'    -- 'free' | 'restricted'
  + allowed_domains TEXT                          -- JSON array, jen pro restricted mode
```

### 2.2 Režimy

| Režim | Chování | Use case |
|---|---|---|
| `free` | Sidecar forwarding neomezuje domény. Agent může přistoupit kamkoli. | Development, prototyping, důvěryhodní agenti |
| `restricted` | Sidecar blokuje **všechny** domény kromě: (1) DefaultAllowedDomains (LLM API), (2) crew `allowed_domains` | Produkce, compliance, data-sensitive prostředí |

### 2.3 Default allowed domains (vždy povolené v restricted mode)

```
api.anthropic.com         -- Anthropic API (Claude)
api.openai.com            -- OpenAI API
generativelanguage.googleapis.com  -- Google AI API
api.factory.ai            -- Factory API
```

Tyto domény jsou hardcoded a nelze je odebrat -- bez nich agent nemůže fungovat.

### 2.4 Crew allowed_domains (custom allowlist)

JSON array extra domén povolených v restricted mode:

```json
["github.com", "api.github.com", "registry.npmjs.org", "pypi.org"]
```

Pravidla:
- Přesná shoda domén (žádné wildcardy) -- konzistentní s existující `DomainAllowlist`
- Case-insensitive
- Port se stripuje
- Prázdný array nebo NULL = jen DefaultAllowedDomains

---

## 3. IMPLEMENTACE

### 3.1 Databáze (migrace #18)

```sql
ALTER TABLE crews ADD COLUMN network_mode TEXT NOT NULL DEFAULT 'free';
ALTER TABLE crews ADD COLUMN allowed_domains TEXT;  -- JSON array
```

### 3.2 API změny

**GET/POST/PUT /api/crews** -- přidat pole `network_mode` a `allowed_domains`

```json
{
  "id": "...",
  "name": "My Crew",
  "network_mode": "restricted",
  "allowed_domains": ["github.com", "api.github.com"]
}
```

Validace:
- `network_mode` musí být `"free"` nebo `"restricted"`
- `allowed_domains` je validní JSON array stringů (nebo null)
- Domény se normalizují na lowercase

### 3.3 Sidecar integrace

**Orchestrator → Sidecar stdin:**

Rozšířit sidecar stdin JSON o network policy:

```json
{
  "credentials": [...],
  "memory": {...},
  "network_policy": {
    "mode": "restricted",
    "allowed_domains": ["github.com", "api.github.com"]
  }
}
```

**Sidecar chování:**

| Mode | Sidecar DomainAllowlist |
|---|---|
| `free` | Bypass -- všechny domény povoleny (allowlist check přeskočen) |
| `restricted` | DefaultAllowedDomains + crew allowed_domains |

Změny v `internal/sidecar/proxy.go`:
- Přidat `freeMode bool` do `ProxyConfig`
- V `handleHTTP()`: pokud `freeMode == true`, přeskočit `allowlist.IsAllowed()` check
- Credential injection funguje v obou režimech (pro LLM domény)

Změny v `internal/sidecar/allowlist.go`:
- Žádné -- existující implementace je dostatečná
- Crew domains se přidají přes `allowlist.Add()` při startu

Změny v `cmd/crewship-sidecar/main.go`:
- Parsovat `network_policy` z stdin JSON
- Pokud `mode == "free"`, nastavit `proxy.freeMode = true`
- Pokud `mode == "restricted"`, přidat `allowed_domains` do allowlist

### 3.4 Docker network

**Žádná změna Docker networku.** Network mode se vynucuje na úrovni sidecar proxy,
ne na úrovni Docker networku. Docker bridge zůstává `Internal=false`.

Důvody:
- Docker `Internal=true` by úplně zablokoval internet -- příliš restriktivní
- Sidecar proxy je lepší enforcement point (granulární per-domain kontrola)
- Agent stejně komunikuje přes `HTTP_PROXY` → sidecar

**Omezení:** Agent může obejít proxy přímým TCP spojením. Pro Phase 2 zvážit
iptables pravidla v kontejneru (DROP vše kromě loopback + sidecar).

### 3.5 UI změny

**Crew Settings / Edit Crew dialog:**

Přidat sekci "Network Policy":
- Toggle/select: Free / Restricted
- Pokud Restricted: textarea/tag-input pro allowed domains
- Info text vysvětlující co jednotlivé režimy dělají
- Zobrazit DefaultAllowedDomains jako read-only (vždy povolené)

### 3.6 CLI změny

```bash
# Vytvořit crew s restricted mode
crewship crew create --name "Secure Team" --network-mode restricted --allowed-domains "github.com,api.github.com"

# Update existující crew
crewship crew update my-crew --network-mode restricted --allowed-domains "github.com"

# Zobrazit network policy
crewship crew info my-crew
# → Network Mode: restricted
# → Allowed Domains: github.com, api.github.com + 4 default
```

---

## 4. BEZPEČNOSTNÍ ÚVAHY

### 4.1 Enforcement boundary

Network policy se vynucuje na úrovni **sidecar HTTP proxy**. Toto má známá omezení:

| Vektor | Mitigace | Riziko |
|---|---|---|
| Přímý TCP (obejití proxy) | Agent ENV má `HTTP_PROXY` → většina nástrojů proxy respektuje | STŘEDNÍ -- raw TCP/netcat může obejít |
| DNS exfiltrace | Žádná v Phase 1 | NÍZKÉ -- malý bandwidth |
| IP adresa místo domény | Sidecar blokuje IP adresy (existující chování) | NÍZKÉ |
| CONNECT tunnel | Allowlist check existuje pro CONNECT | NÍZKÉ |

### 4.2 Phase 2 hardening

- **iptables/nftables** v kontejneru: DROP vše kromě loopback a sidecar port
- **DNS filtering**: custom DNS resolver v sidecar, blokuje non-allowlisted domény
- **Audit log**: logovat blokované requesty pro security review

---

## 5. BACKWARDS COMPATIBILITY

- Default `network_mode = 'free'` → žádná změna pro existující crews
- Sidecar stdin JSON backwards-compatible (chybějící `network_policy` = free mode)
- API vrací `network_mode` a `allowed_domains` pro všechny crews

---

## 6. TESTOVÁNÍ

| Test | Popis |
|---|---|
| Unit: sidecar free mode | Proxy povolí libovolnou doménu když `freeMode=true` |
| Unit: sidecar restricted mode | Proxy blokuje non-allowlisted domény |
| Unit: crew allowed_domains merge | DefaultAllowedDomains + crew domains = celkový allowlist |
| Unit: stdin parsing | `network_policy` správně parsován, backwards-compatible |
| Integration: crew CRUD | API create/update crew s network_mode a allowed_domains |
| Integration: E2E | Agent v restricted mode nemůže přistoupit na evil.com |
| Migration: upgrade | Existující crews dostanou `network_mode='free'` |

---

## 7. IMPLEMENTAČNÍ FÁZE

### Phase 1 (tento ticket)
- DB migrace (network_mode, allowed_domains na crews)
- API CRUD
- Sidecar stdin rozšíření + free/restricted mode
- Orchestrator předává network policy do sidecar
- UI: crew settings
- CLI: --network-mode, --allowed-domains flagy
- Testy

### Phase 2 (budoucí)
- iptables hardening v kontejneru (blokovat přímý TCP)
- DNS filtering
- Audit log blokovaných requestů
- Wildcard domény (*.github.com)
- Domain group presets (např. "GitHub" = github.com + api.github.com + raw.githubusercontent.com)
