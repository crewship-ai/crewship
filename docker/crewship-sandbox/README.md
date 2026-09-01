# Crewship Sandbox Image

A security-hardened Docker image for Crewship crew containers, providing L3/L4 network isolation via iptables + ipset.

## Inspired by

[Claude Code's devcontainer](https://github.com/anthropics/claude-code/tree/main/.devcontainer) — Anthropic's own secure sandbox pattern.

## Key features

- **L3/L4 firewall**: iptables + ipset allowlist (stronger than HTTP-proxy filtering — cannot be bypassed)
- **Default DROP policy**: Explicit REJECT for all non-allowed traffic
- **Self-verifying**: Runs connectivity tests on startup (fails loud if firewall is broken or leaking)
- **Non-root**: UID 1001 (matches Crewship convention)
- **Allowed egress**:
  - Anthropic / OpenAI / Google AI APIs
  - GitHub (IP ranges from api.github.com/meta)
  - npm / Yarn / PyPI registries
  - Debian package mirrors
  - OCI registries (ghcr.io, Docker Hub) — for devcontainer features
  - Host network (for crewshipd IPC)

## Why L3/L4 vs HTTP proxy?

| Layer | What it sees | Bypass risk |
|-------|--------------|-------------|
| HTTP proxy (app-level) | HTTP/HTTPS only | Agent can use raw TCP, UDP, DNS exfiltration |
| iptables + ipset (L3/L4) | ALL IP traffic | Cannot bypass without root |

## Required capabilities

The container MUST run with `NET_ADMIN` and `NET_RAW` Linux capabilities, or the firewall script will fail to configure iptables/ipset rules.

> [!WARNING]
> **Crewship's container provider does not grant them.** Nothing in the tree
> parses a devcontainer's `runArgs`, and `internal/devcontainer/config.go`
> refuses any direct capability grant other than `NET_BIND_SERVICE`
> (`ErrCapabilityNotAllowed`). So on a normal crew this firewall does not
> configure itself — treat it as **not an active control** until that path
> exists. It works when you run the image yourself with the flags below, and
> on a `Privileged` crew, which is outside Crewship's threat model.

If running manually, pass `--cap-add=NET_ADMIN --cap-add=NET_RAW` to `docker run`.

## Usage

As a base image for a Crewship crew:

1. Set `runtime_image` on the crew to `ghcr.io/crewship-ai/crewship-sandbox:latest`
2. The crew container needs `NET_ADMIN` + `NET_RAW`. **Crewship does not grant
   these** (see the warning above), so on a non-privileged crew step 3 does not
   happen and the image runs without its firewall.
3. Where the capabilities *are* present, the firewall initializes on container
   start via `postStartCommand`.

Or bring your own devcontainer.json extending the sandbox:

```jsonc
{
    "image": "ghcr.io/crewship-ai/crewship-sandbox:latest",
    "features": {
        "ghcr.io/devcontainers/features/python:1": {}
    },
    "runArgs": ["--cap-add=NET_ADMIN", "--cap-add=NET_RAW"],
    "postStartCommand": "sudo /usr/local/bin/init-firewall.sh"
}
```

## Customizing allowed destinations

Edit `init-firewall.sh` and rebuild. The `ALLOWED_DOMAINS` array lists outbound hosts; the `GitHub IP ranges` section auto-fetches github.com CIDRs.

## Building locally

```bash
cd docker/crewship-sandbox
docker build -t crewship-sandbox:local .
```

## Running for testing (without Crewship)

```bash
docker run -it --rm \
    --cap-add=NET_ADMIN --cap-add=NET_RAW \
    crewship-sandbox:local bash -c \
    "sudo /usr/local/bin/init-firewall.sh && bash"
```
