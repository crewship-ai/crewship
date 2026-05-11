# Crewship Infrastructure — Remote Environments

All Crewship development and dogfood production happens on remote Proxmox VMs via SSH. **Never build or run services locally on the Mac Mini.**

## `crewship-dev` — VMID 300 (development)

- **Connect:** `ssh crewship-dev` (alias for `ubuntu@192.168.1.201`)
- **DNS:** `crewship-dev.unifylab.cz` → 192.168.1.201
- **Repo path:** `/opt/crewship`
- **Backend:** `http://crewship-dev.unifylab.cz:8080` (or http://192.168.1.201:8080)
- **Frontend:** `http://crewship-dev.unifylab.cz:3001` (or http://192.168.1.201:3001)
- **Resources:** 12 vCPU, 48 GB RAM (balloon 24 GB, reduced from 64 GB on 2026-04-27 — peak ~29 GB), 200 GB NVMe
- **Tracks:** hosts **feature branches** for shared PR testing — branches are pushed from developer machines and selected via **manual checkout on the VM**. **Never auto-pulls from `main`.** See user's `feedback_dev_vm_branches.md` for the rule. Started via `./dev.sh start` in tmux.
- **VS Code / Cursor:** `code --remote ssh-remote+crewship-dev /opt/crewship`
- Go PATH: `export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin` (already in `.bashrc`)
- Tags: `crewship`, `dev`, `environment-development`

## `crewship-prod` — VMID 301 (dogfood production)

- **Connect:** `ssh crewship-prod` (alias for `ubuntu@192.168.1.202`)
- **DNS:** `crewship-prod.unifylab.cz` → 192.168.1.202
- **Repo path:** `/opt/crewship-prod`
- **Backend / Frontend (embedded):** `http://crewship-prod.unifylab.cz:8080` (or http://192.168.1.202:8080)
- **Resources:** 4 vCPU, 16 GB RAM (balloon 8 GB), 60 GB local-lvm disk
- **OS:** Ubuntu 24.04.4 LTS cloud-init image, Docker 29.4.1 native (overlayfs, cgroup v2)
- **Tracks:** `release` branch (created from main on 2026-04-27). Push to deploy: `git push origin main:release`. systemd timer polls every 5 min and rebuilds if SHA changed. Rollback: `git push -f origin <good-sha>:release`.
- **systemd units:** `crewship-prod.service` (server), `crewship-deploy.timer` (5-min poll → `/opt/crewship-prod/deploy.sh`)
- **Env file:** `/etc/crewship/crewship-prod.env` (mode 0600, root-owned). Contains `NEXTAUTH_SECRET`, `ENCRYPTION_KEY` (env-unique, NOT shared with dev), `CREWSHIP_ENV=production`, etc.
- **Storage:** DB at `/opt/crewship-prod/data/crewship.db`, localfs provider at `/var/lib/crewship`
- **Deploy SSH key:** GitHub Deploy Key on `crewship-ai/crewship` (read-only) so the timer can `git pull` without agent forwarding
- Tags: `crewship`, `prod`, `environment-production`

## Why VM, not LXC

Unprivileged LXC + Docker fails at first `docker run` (`runc create failed: open sysctl ip_unprivileged_port_start: permission denied`). Privileged LXC would fix it but doesn't match what real customers run (~70 % self-host on cloud VMs with native Docker). Native VM matches Tier 1 customer reality. ~1.5 GB RAM overhead is the price.

## Prod network isolation

Proxmox firewall on net0 (`/etc/pve/firewall/301.fw`). Default ACCEPT in/out, but explicit `OUT DROP` rules block crossover to:
- `.101` (truenas/minio)
- `.200` (coolify)
- `.201` (crewship-dev)
- `.221` (MBA runner)
- `.230` (truenas alt-IP)
- `.251` (proxmox host)

Internet (GitHub, LLM APIs) and gateway/DNS (`.1`) remain reachable. SSH from LAN unaffected. Tested: `ping .201` from prod VM = blocked, `curl https://github.com` = OK.

## Other Proxmox VMs (non-Crewship)

- VMID 103 `truenas` (storage NAS)
- VMID 200 `coolify` (self-hosted PaaS, also catches `*.unifylab.cz` wildcard for undefined subdomains)
- Proxmox host: `ssh proxmox` (alias for `root@192.168.1.251`, DNS `proxmox.unifylab.cz`)

## NEXTAUTH_SECRET deployment trap

Missing `NEXTAUTH_SECRET` on the server → silent 404s on every route. Only signal is `WARN NEXTAUTH_SECRET not set, WebSocket auth disabled`. Server guard in `internal/server/server.go` gates BOTH API-router AND SPA handler.

Healthy startup logs both `Go API routes mounted` AND `serving embedded static UI`. Verify:
```bash
journalctl -u crewship-prod | grep -E "API routes mounted|serving embedded"
```

## Self-hosted GitHub runner

`mba-m3-arm64` (MBA M3) runs Frontend + Go jobs since PR #218. Org-level CI broken since 2026-04-21.

## Hardware split

- **Mac Mini** = Claude Code (this machine)
- **Proxmox host** = builds, Docker, dev/prod VMs
- **MBA M3** = self-hosted GH Actions runner
