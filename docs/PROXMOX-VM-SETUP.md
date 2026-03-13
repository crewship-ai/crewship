# PRD: Crewship Development Server na Proxmoxu

## Účel

Potřebujeme přesunout vývoj aplikace **Crewship** z Mac Mini (16GB RAM, přetížený) na výkonný Proxmox server (128GB RAM). Mac Mini bude sloužit jen jako tenký klient (VS Code / Cursor přes SSH), veškerý compute (build, kontejnery, AI agenti) poběží na serveru.

## Co je Crewship

Crewship je platforma pro orchestraci AI agentů. Architektura:
- **Go backend** (single binary) — HTTP server na portu 8080, SQLite databáze, WebSocket
- **Next.js frontend** — statický export (`out/`), embedded v Go binary
- **Agent runtime kontejnery** — Docker kontejnery (Ubuntu-based), 1 kontejner = 1 crew (tým agentů). Agenti běží uvnitř jako Docker exec. Kontejnery potřebují: Node.js, Python, Git, ripgrep, Claude Code CLI.
- **Sidecar proxy** — běží uvnitř každého agent kontejneru, injektuje API klíče, proxy na externí API (Anthropic, OpenAI, atd.)

Aplikace spouští Docker kontejnery zevnitř sebe (mountuje `/var/run/docker.sock`).

## Požadavky na VM

### Hardware
- **RAM:** 32-64 GB (ze 128GB na hostu je dost; agenti žerou hodně RAM)
- **CPU:** 8-16 vCPU (Go build + Node.js build + paralelní agenti)
- **Disk:** 100-200 GB SSD/NVMe (kód, Docker images, SQLite DB, agent výstupy)
- **Síť:** Bridge s DHCP nebo statická IP, přístupný z LAN + internetu (pro SSH z Mac Mini)

### OS
- **Ubuntu 24.04 LTS Server** (minimální instalace, bez GUI)

### Software který je potřeba nainstalovat

1. **Docker Engine** (ne Docker Desktop) — agenti běží v Docker kontejnerech
   ```bash
   curl -fsSL https://get.docker.com | sh
   ```

2. **Go 1.26+**
   ```bash
   wget https://go.dev/dl/go1.26.1.linux-amd64.tar.gz
   tar -C /usr/local -xzf go1.26.1.linux-amd64.tar.gz
   echo 'export PATH=$PATH:/usr/local/go/bin' >> /etc/profile.d/golang.sh
   ```

3. **Node.js 24 + pnpm**
   ```bash
   curl -fsSL https://deb.nodesource.com/setup_24.x | bash -
   apt-get install -y nodejs
   corepack enable pnpm
   ```

4. **Air** (Go hot-reload pro vývoj)
   ```bash
   go install github.com/air-verse/air@latest
   ```

5. **Základní nástroje**
   ```bash
   apt-get install -y git curl wget jq ripgrep htop tmux build-essential sqlite3
   ```

6. **SSH server** — měl by být defaultně, ověřit že běží
   ```bash
   systemctl enable --now ssh
   ```

### SSH přístup
- Root přístup přes SSH klíč (password auth vypnout)
- SSH klíč z Mac Mini (`~/.ssh/id_ed25519.pub` nebo `~/.ssh/id_rsa.pub`) přidat do `/root/.ssh/authorized_keys`
- Port 22 (nebo vlastní, ale 22 je jednodušší)

### Síťová konfigurace
- VM musí mít přístup na internet (stahování Go/Node balíčků, Docker images, Anthropic API)
- Port **8080** přístupný z LAN (Crewship web UI)
- Port **3011** přístupný z LAN (Next.js dev server při vývoji)
- Port **22** přístupný z LAN + WAN (SSH pro remote dev)

### Firewall (volitelné ale doporučené)
```bash
ufw allow 22/tcp    # SSH
ufw allow 8080/tcp  # Crewship backend
ufw allow 3011/tcp  # Next.js dev
ufw enable
```

## Po vytvoření VM

Až bude VM ready, pošli mi:
1. **IP adresu** VM
2. Potvrzení že **SSH přes klíč funguje** (`ssh root@<IP>`)
3. Potvrzení že **Docker běží** (`docker info`)

Já pak:
1. Naklonuju repozitář na server
2. Nastavím `.env` s API klíči
3. Spustím `./dev.sh start`
4. Připojím se z Mac Mini přes VS Code Remote SSH

## Výsledný workflow po setupu

```
Mac Mini (VS Code / Cursor)
    │
    │  SSH (port 22)
    ▼
Ubuntu VM na Proxmoxu (128GB RAM pool)
    ├── Go backend (air hot-reload)
    ├── Next.js dev server
    ├── Docker Engine
    │   ├── crewship-team-engineering (agent kontejner)
    │   ├── crewship-team-quality (agent kontejner)
    │   ├── crewship-team-operations (agent kontejner)
    │   └── ... (další crews)
    └── SQLite DB + agent výstupy
```

## Poznámky
- **Žádný Coolify/Portainer** — přímý Docker, plná kontrola
- **Žádný Kubernetes** — overkill pro single-node dev
- **SQLite** stačí, nepotřebujeme PostgreSQL (zatím)
- VM může sloužit i jako staging/preview prostředí později
- Pokud je potřeba HTTPS, přidat Caddy jako reverse proxy později
