# BYOI Agent Containers: Research Report

## How Competing AI Agent Platforms Handle Container Runtime Images

**Date:** 2026-02-26
**Purpose:** Research for Crewship's "Bring Your Own Image" (BYOI) approach — allowing users to provide any Docker image and injecting sidecar + tooling at container start time.

---

## Executive Summary

The industry has converged on **three dominant patterns** for sandboxing AI agents:

1. **Sidecar volume mount** (Warp/Namespace) — Mount tooling as a read-only volume alongside the user's image
2. **Image layering / build-time injection** (E2B, Coder/envbuilder, devcontainer Features) — Build a new image that layers platform tooling on top of the user's base
3. **Agent-outside-sandbox** (Modal, Daytona) — Agent runs externally; sandbox is called as a tool via API

**Recommendation for Crewship:** The **sidecar bind-mount approach** (Pattern 1) is the best fit. It's what Warp uses, maps cleanly to Docker bind mounts, requires zero image modification, and allows instant tooling updates without rebuilding images.

---

## Platform-by-Platform Analysis

### 1. Warp (warp.dev) ⭐ Most Relevant

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — any glibc-based Linux image (Debian, Ubuntu, etc.). Alpine/musl not supported. |
| **Tooling injection** | **Sidecar volume mount** via Namespace's sidecar volume support. Mounts Warp CLI, git, CA certificates under an `agent/` directory merged with the user's base image. |
| **UID/permissions** | Credentials are short-lived, scoped to the triggering user. Sandboxes fully isolated per team (Namespace tenant) and per sandbox. |
| **Features/layers** | No formal "features" system. The sidecar volume is the single injection mechanism. |
| **Limitations** | Must be glibc-based. Linux only currently (macOS/Windows planned). |
| **Key insight** | "The base image doesn't even need to have Warp installed!" — tooling is entirely sidecar-mounted. Updates ship instantly without user intervention. |

**Source:** https://www.warp.dev/blog/secure-cloud-sandboxes-for-ai-dev-with-namespace

---

### 2. E2B (e2b.dev)

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — via "sandbox templates" defined as Dockerfiles or code (Build System 2.0). Must be Debian-based. |
| **Tooling injection** | **Build-time image layering.** E2B builds a container from your template, provisions it (installs `envd` agent binary + dependencies), then **snapshots the entire running VM** (filesystem + processes). Sandboxes restore from snapshot in ~80-200ms. |
| **UID/permissions** | Runs as Firecracker microVMs (not containers). Hardware-level isolation. Each sandbox gets its own kernel. |
| **Features/layers** | Templates define the full environment. Layer commands execute during build. No plug-in "features" system. |
| **Limitations** | Needs Debian-based images. Kernel is fixed at build time (LTS Linux). Not a container — it's a microVM. |
| **Key insight** | Uses **Firecracker microVMs** (same tech as AWS Lambda). The snapshot-and-restore approach gives VM-level isolation with container-like startup times. |

**Source:** https://e2b.dev/docs/sandbox-template, https://e2b.dev/docs/template/how-it-works

---

### 3. Daytona (daytona.io)

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — supports devcontainer.json with custom Dockerfiles, or declarative image building via SDK. |
| **Tooling injection** | **Declarative Image Builder** — SDK-based; Daytona builds and manages images automatically. Also supports devcontainer.json for standard tooling. Agent runs **outside** the sandbox (Pattern 2 — sandbox-as-tool). |
| **UID/permissions** | Managed via devcontainer user configuration. Sandboxes are isolated per-session. |
| **Features/layers** | Supports devcontainer Features for adding tools. Declarative SDK for dynamic image building. |
| **Limitations** | Primarily Linux. SDK-first approach may not suit all workflows. |
| **Key insight** | Sub-90ms cold start. Persistent, stateful sandboxes that agents can start/pause/fork/snapshot/resume. Agent typically calls sandbox via API rather than running inside it. |

**Source:** https://www.daytona.io/dotfiles/declarative-image-builder

---

### 4. Coder (coder.com)

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — full custom image support via templates. Supports "golden images" (standardized) and developer-owned images. Also supports devcontainer.json. |
| **Tooling injection** | **Self-contained workspace builds:** The Coder agent binary is downloaded at workspace startup via `curl` from `coderd`. Image just needs `curl` available. Alternatively, "remote builds" upload agent + code-server to the workspace. **Envbuilder** builds devcontainer images in-place without Docker-in-Docker (uses Kaniko). |
| **UID/permissions** | Recommends non-root user in base images. Agent runs as the workspace user. Templates define permissions model. |
| **Features/layers** | Supports devcontainer Features via envbuilder. Templates are Terraform-based, allowing arbitrary infrastructure. |
| **Limitations** | Requires `curl` in base image for self-contained builds. Envbuilder is ~74MB. |
| **Key insight** | Agent binary downloads itself at startup — zero image modification needed. Just needs network access to `coderd` and `curl`. Prebuilt workspaces can pool environments for instant startup. |

**Source:** https://coder.com/docs/admin/templates/managing-templates/image-management, https://coder.com/blog/envbuilder-is-here

---

### 5. Gitpod

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — via `.gitpod.yml` referencing public/private Docker images or custom Dockerfiles (`.gitpod.Dockerfile`). |
| **Tooling injection** | **Layered image build.** Gitpod builds on top of base images (workspace-base, workspace-full). Custom Dockerfiles extend these layers. Gitpod's own IDE tooling (VS Code server, etc.) is injected during workspace creation. Supports prebuilt images for faster startup. |
| **UID/permissions** | Default user `gitpod` (UID 33333). Custom images should use this user or configure appropriately. |
| **Features/layers** | "Chunks" system — language-specific layers (go-chunk, node-chunk, etc.) composed into workspace images. Not devcontainer Features per se, but similar modular concept. |
| **Limitations** | Must be compatible with Gitpod's workspace orchestration. Alpine supported but with caveats. Docker-in-Docker supported via Gitpod's privileged setup. |
| **Key insight** | The "chunks" architecture is a pre-devcontainer approach to modular tooling layers. Prebuilt images amortize build time. |

**Source:** https://github.com/gitpod-io/workspace-images, https://www.gitpod.io/docs/configure/workspaces/workspace-image

---

### 6. GitHub Codespaces / devcontainer Spec

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — any Docker image via `devcontainer.json`. Supports Dockerfiles, Docker Compose, and pre-built images. |
| **Tooling injection** | **devcontainer Features** — self-contained units (OCI artifacts) with `install.sh` scripts. Published to container registries. Features are layered on top of the base image at build time. GitHub injects VS Code Server, git credentials, and dotfiles during creation. |
| **UID/permissions** | Configurable via `remoteUser` in devcontainer.json. Default `vscode` user. Supports `containerUser` for build-time and `remoteUser` for runtime. |
| **Features/layers** | ✅ **The industry standard for composable tool injection.** Features are OCI artifacts with `devcontainer-feature.json` metadata + `install.sh`. Supports dependency resolution, architecture detection, versioning. 100s of community features available. |
| **Limitations** | Features run at build time, not runtime. Base image must be Linux. Build can be slow without prebuilds. |
| **Key insight** | devcontainer Features are the closest thing to a universal standard for "inject tool X into any container image." The OCI artifact distribution model is well-designed. |

**Source:** https://containers.dev/implementors/features/, https://docs.github.com/en/codespaces/setting-up-your-project-for-codespaces/adding-features-to-a-devcontainer-json-file

---

### 7. Modal (modal.com)

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — supports Docker Hub images, private registries (ECR, GCR), and custom Dockerfiles. Also supports building images programmatically via Python API (method chaining: `apt_install`, `pip_install`, etc.). |
| **Tooling injection** | **Agent-outside pattern.** Modal sandboxes are called as tools — the agent runs externally and sends commands via API. The sandbox itself is a standard container with no special agent binary injected. |
| **UID/permissions** | Managed by Modal's platform. Containers run in Modal's infrastructure with network isolation. |
| **Features/layers** | Image builder API provides composable layers (`Image.debian_slim().apt_install(...).pip_install(...)`). Not devcontainer Features, but similar composability. |
| **Limitations** | Python-centric API. Sandboxes up to 24 hours. No persistent state between sandbox runs. |
| **Key insight** | The code-first image builder API is elegant for programmatic environment definition. No tooling injection needed because agent stays outside. |

**Source:** https://modal.com/docs/guide/images, https://modal.com/blog/top-code-agent-sandbox-products

---

### 8. DevPod (devpod.sh)

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — full devcontainer.json support with custom Dockerfiles and images. Supports Docker Compose. |
| **Tooling injection** | Uses the **devcontainer spec** natively. DevPod injects an SSH server into the container at creation time for IDE connectivity. Supports devcontainer Features for additional tooling. Prebuild system builds images ahead of time and stores them in container registries. |
| **UID/permissions** | Follows devcontainer spec for user configuration. SSH-based access means the container user's permissions apply. |
| **Features/layers** | ✅ Full devcontainer Features support. Prebuild hashes based on devcontainer.json + Dockerfile content. |
| **Limitations** | Client-only — no server component. Provider-based (Docker, K8s, cloud VMs). Workspace state tied to individual hosts. |
| **Key insight** | Pure devcontainer implementation with SSH-based access. No proprietary injection — everything goes through the standard spec. |

**Source:** https://devpod.sh/, https://devpod.sh/docs/developing-in-workspaces/prebuild-a-workspace

---

### 9. OpenHands (formerly Open-Devin) ⭐ Close Competitor

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Yes — supports custom Docker images. Default is `nikolaik/python-nodejs`. Must be Debian-based. |
| **Tooling injection** | **Build-time image layering.** OpenHands builds a new runtime image on top of the user's base image, incorporating OpenHands-specific code (ActionExecutor, bash shell, plugins, Jupyter server). Uses a tagging system for efficient image management. Inside the container, an `ActionExecutor` initializes and communicates with the backend via REST API. |
| **UID/permissions** | Container-per-user isolation. Agent server manages Docker containers per workspace. Configurable resource limits and cleanup policies. |
| **Features/layers** | Plugin system (Jupyter, VSCode web) initialized at startup. No formal "features" system. |
| **Limitations** | Must be Debian-based. Rebuilds image when base changes. The ActionExecutor binary is baked into the image, not mounted. |
| **Key insight** | Classic "agent-inside-container" pattern. The image rebuild approach means any base image change requires a new build. V1 architecture moved to modular SDK with optional sandboxing. |

**Source:** https://docs.openhands.dev/openhands/usage/architecture/runtime, https://docs.openhands.dev/sdk/arch/overview

---

### 10. CrewAI / AutoGen / LangGraph

| Aspect | Details |
|---|---|
| **Custom images?** | **AutoGen:** ✅ supports Docker containers for code execution. **CrewAI:** ⚠️ requires manual Docker setup. **LangGraph:** ❌ no native container support (uses external tools). |
| **Tooling injection** | These are **orchestration frameworks, not container platforms.** They don't inject tooling into containers — they call containers (or code interpreters) as tools. AutoGen has the most integrated Docker support. |
| **UID/permissions** | Depends on the underlying Docker/container setup. Not managed by the frameworks. |
| **Features/layers** | None — these operate at the agent orchestration level, not container level. |
| **Limitations** | Not comparable to container platforms. They're the "agent brain," not the "agent body." |
| **Key insight** | For Crewship's purposes, these are orthogonal — they'd run *inside* Crewship's containers, not replace them. |

**Source:** https://scalexi.medium.com/comparing-llm-agent-frameworks-code-execution-capabilities

---

### 11. Sysbox Runtime

| Aspect | Details |
|---|---|
| **Custom images?** | ✅ Any OCI image — Sysbox is a container runtime, not a platform. |
| **Tooling injection** | Not applicable — Sysbox is the runtime layer. It enables running Docker/K8s inside containers safely without privileged mode. Uses Linux user namespaces for rootless nested containers. |
| **UID/permissions** | **User namespace remapping** — root inside the container maps to an unprivileged user on the host. Strong isolation without privileged mode. |
| **Features/layers** | N/A — it's a runtime, not an application layer. |
| **Limitations** | Linux only. Requires kernel support (Ubuntu Jammy + kernel 6.5 LTS recommended). Latest release v0.6.6 (Jan 2025). Now part of Docker. |
| **Key insight** | Enables Docker-in-Docker safely. If Crewship agents need to run Docker commands inside their containers, Sysbox is the way to avoid `--privileged`. |

**Source:** https://github.com/nestybox/sysbox

---

## Injection Patterns: Comparison

### Pattern 1: Sidecar Bind Mount (Runtime Injection)
**Used by:** Warp/Namespace, Kubernetes sidecar pattern (Istio)

```
docker run -v /host/agent-tools:/opt/agent:ro \
           -v /host/sidecar:/opt/sidecar:ro \
           user-image:latest
```

| Pros | Cons |
|---|---|
| Zero image modification | Requires compatible filesystem paths |
| Instant tooling updates (no rebuild) | Bind mount paths visible inside container |
| Works with ANY base image | Can't modify image's PATH without entrypoint wrapper |
| Smallest attack surface | Host filesystem dependency |
| Fastest startup | Need entrypoint wrapper to set up PATH/env |

**How Warp does it:** Mounts `agent/` directory with Warp CLI, git, CA certs as a sidecar volume. Made CLI "almost entirely self-contained" to minimize dependency footprint.

**How Istio does it:** MutatingAdmissionWebhook injects init container (iptables setup) + sidecar container (Envoy proxy) into Pod spec at creation time. The sidecar shares the pod's network namespace.

---

### Pattern 2: Build-Time Image Layering
**Used by:** E2B, OpenHands, Gitpod, devcontainer Features

```dockerfile
FROM user-image:latest
COPY agent-binary /usr/local/bin/agent
COPY sidecar /usr/local/bin/sidecar
RUN setup-agent.sh
```

| Pros | Cons |
|---|---|
| Clean, self-contained image | Requires rebuild when tooling changes |
| Full control over PATH, env, etc. | Build time adds to startup latency |
| Can modify system config | Image storage costs multiply |
| Devcontainer Features ecosystem | Base image compatibility issues |

**How E2B does it:** Builds container → provisions → snapshots entire VM (filesystem + processes). Restores from snapshot in ~80-200ms.

**How devcontainer Features work:** OCI artifacts with `install.sh` scripts. Dependency-aware installation ordering. Architecture detection for multi-platform support. Published to container registries.

---

### Pattern 3: Agent Downloads Itself at Startup
**Used by:** Coder

```bash
# In container entrypoint:
curl -fsSL https://coderd.example.com/bin/agent | sh
```

| Pros | Cons |
|---|---|
| Minimal image requirements (just curl) | Depends on network connectivity |
| Always gets latest agent | Startup latency for download |
| No image modification | Single point of failure (coderd) |
| Works with most images | Requires curl in image |

---

### Pattern 4: Agent Outside, Sandbox as Tool
**Used by:** Modal, Daytona (partial), LangChain patterns

```python
sandbox = Sandbox.create("user-image")
result = sandbox.commands.run("echo hello")
sandbox.close()
```

| Pros | Cons |
|---|---|
| No injection needed at all | Network latency on every command |
| Clean separation of concerns | Agent can't use local tools in sandbox |
| Easy to update agent logic | No persistent environment state (usually) |
| Sandbox is a pure execution env | Less "native" feel for the agent |

---

### Pattern 5: docker cp at Runtime
**Not widely used by major platforms, but viable**

```bash
docker create --name agent-container user-image:latest
docker cp ./agent-binary agent-container:/usr/local/bin/
docker cp ./sidecar agent-container:/usr/local/bin/
docker start agent-container
```

| Pros | Cons |
|---|---|
| Works with any image | Adds startup latency |
| No bind mount path dependencies | Cannot modify running container filesystem layout |
| One-time copy, no host dependency | Must be done between create and start |
| Simpler than bind mounts | Less elegant than volume mounts |

---

## Sandboxing Technology Comparison

| Technology | Isolation Level | Cold Start | Custom Images? | Persistence |
|---|---|---|---|---|
| **OS Primitives** (bubblewrap, Landlock) | Process-level | ~0ms | N/A (host process) | N/A |
| **Docker/Containers** | Namespace/cgroup | ~1-5s | ✅ | Session-scoped |
| **gVisor** (runsc) | Userspace kernel | ~500ms | ✅ | Session-scoped |
| **Sysbox** | Enhanced container | ~2-5s | ✅ | Session-scoped |
| **Firecracker microVM** | Hardware (KVM) | ~125-200ms | ✅ (template) | Up to 24h |
| **Cloud Hypervisor** | Hardware (KVM) | ~200ms | ✅ (template) | Configurable |
| **Full VM** (Fly Sprites) | Hardware | 1-2s | ✅ | Persistent |

---

## Recommendation for Crewship

### Primary: Sidecar Bind Mount (Warp Pattern)

**This is the best fit for Crewship's architecture.** Here's why:

1. **Zero image modification** — Users provide any Docker image, Crewship mounts tooling alongside it
2. **Instant updates** — Ship new sidecar/agent versions without touching user images
3. **Simplest implementation** — Docker bind mounts are well-understood, no custom build pipeline needed
4. **Maps to current architecture** — Crewship already uses Docker containers with a sidecar proxy

#### Implementation approach:
```bash
docker run \
  -v /opt/crewship/sidecar:/opt/crewship/sidecar:ro \
  -v /opt/crewship/tools:/opt/crewship/tools:ro \
  --entrypoint /opt/crewship/sidecar/entrypoint.sh \
  user-image:latest
```

The entrypoint wrapper would:
1. Add `/opt/crewship/sidecar` and `/opt/crewship/tools` to PATH
2. Set up the Unix socket for IPC (`/tmp/crewship.sock`)
3. Configure the internal auth token (`X-Internal-Token`)
4. Start the sidecar proxy in the background
5. Exec the user's original entrypoint (or the agent)

#### Requirements for user images:
- **Must be Linux** (glibc-based recommended, like Warp)
- **Must have a shell** (`/bin/sh` or `/bin/bash`)
- **Should have basic utilities** (but Crewship can mount them if missing)

#### What to mount via sidecar volume:
| Component | Path | Purpose |
|---|---|---|
| Sidecar proxy | `/opt/crewship/sidecar/` | HTTP-over-Unix-socket IPC proxy |
| Claude Code CLI | `/opt/crewship/tools/claude` | AI agent CLI |
| Git (if missing) | `/opt/crewship/tools/git` | Version control |
| Entrypoint wrapper | `/opt/crewship/sidecar/entrypoint.sh` | PATH setup, sidecar startup |
| CA certificates | `/opt/crewship/certs/` | TLS for outbound connections |

### Secondary Considerations

1. **Fallback to docker cp** — For environments where bind mounts aren't available (some cloud providers), use `docker create` → `docker cp` → `docker start` as a fallback.

2. **devcontainer Features compatibility** — Consider supporting `devcontainer.json` in the future, which would let users declare additional tooling using the industry-standard Features system. This is what Coder and DevPod do.

3. **Sysbox for Docker-in-Docker** — If agents need to run Docker commands inside their containers, integrate Sysbox as the container runtime instead of the default runc. This avoids `--privileged` mode.

4. **Image validation** — Like Warp, require glibc-based images and validate at create time. Provide clear error messages for incompatible images (Alpine/musl).

---

## Key URLs and References

| Resource | URL |
|---|---|
| Warp Namespace Architecture | https://www.warp.dev/blog/secure-cloud-sandboxes-for-ai-dev-with-namespace |
| Warp Environment Docs | https://docs.warp.dev/agent-platform/cloud-agents/environments |
| E2B Template Docs | https://e2b.dev/docs/sandbox-template |
| E2B How It Works | https://e2b.dev/docs/template/how-it-works |
| Coder Image Management | https://coder.com/docs/admin/templates/managing-templates/image-management |
| Coder Envbuilder | https://coder.com/blog/envbuilder-is-here |
| Coder Self-Contained Builds | https://coder.com/docs/v1/admin/workspace-management/self-contained-builds |
| devcontainer Spec | https://containers.dev/implementors/spec |
| devcontainer Features Ref | https://containers.dev/implementors/features |
| Gitpod Workspace Images | https://github.com/gitpod-io/workspace-images |
| Gitpod Workspace Image Docs | https://www.gitpod.io/docs/configure/workspaces/workspace-image |
| GitHub Codespaces Features | https://docs.github.com/en/codespaces/setting-up-your-project-for-codespaces/adding-features-to-a-devcontainer-json-file |
| Modal Images Docs | https://modal.com/docs/guide/images |
| Modal Sandbox Comparison | https://modal.com/blog/top-code-agent-sandbox-products |
| Daytona Image Builder | https://www.daytona.io/dotfiles/declarative-image-builder |
| DevPod Prebuild Docs | https://devpod.sh/docs/developing-in-workspaces/prebuild-a-workspace |
| OpenHands Runtime Arch | https://docs.openhands.dev/openhands/usage/architecture/runtime |
| OpenHands SDK Overview | https://docs.openhands.dev/sdk/arch/overview |
| Sysbox GitHub | https://github.com/nestybox/sysbox |
| K8s Sidecar Injection Guide | https://www.plural.sh/blog/sidecar-injection-guide/ |
| Sandbox Comparison (2026) | https://michaellivs.com/blog/sandbox-comparison-2026/ |
| LangChain Sandbox Patterns | https://blog.langchain.com/the-two-patterns-by-which-agents-connect-sandboxes/ |
| Anthropic sandbox-runtime | https://github.com/anthropic-experimental/sandbox-runtime |
