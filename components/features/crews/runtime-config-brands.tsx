"use client"

import type { IconType } from "react-icons"
import { Cloud } from "lucide-react"
import {
  SiPython, SiNodedotjs, SiGo, SiRust, SiRuby, SiPhp,
  SiOpenjdk, SiElixir, SiErlang, SiDeno, SiBun,
  SiDotnet, SiKotlin, SiScala, SiSwift, SiZig, SiCrystal,
  SiDocker, SiKubernetes, SiTerraform, SiAnsible, SiHelm,
  SiGooglecloud, SiDigitalocean,
  SiGithub, SiGitlab, SiGit,
  SiPostgresql, SiMysql, SiMariadb, SiRedis, SiMongodb, SiSqlite,
  SiHashicorp, SiVault, SiPulumi,
  SiVim, SiZsh, SiGnubash,
  SiPnpm, SiYarn, SiNpm,
  SiFirebase, SiSupabase,
  SiNginx, SiApache,
  SiFlutter, SiDart, SiElm,
  SiHugo, SiVite, SiWebpack,
  SiJulia, SiLua, SiPerl, SiR, SiHaskell,
  SiGraphql,
  SiAnthropic,
  SiVercel, SiCloudflare, SiFlydotio,
  SiOllama, SiSentry, SiDatadog, SiRailway, SiNetlify,
  // Base-image and devcontainer-feature brands. Without these the catalogue
  // rows that name an OS or a Python distribution drew the generic package
  // glyph — 1241 rows of identical grey, which is the same as no icon.
  SiUbuntu, SiDebian, SiAlpinelinux, SiLinux, SiRedhat, SiFedora, SiArchlinux,
  SiAnaconda, SiGithubcopilot, SiJupyter, SiXfce, SiRstudioide,
  SiPoetry, SiGradle, SiApachemaven, SiTypescript, SiJfrog, SiSnyk,
  SiGnuprivacyguard, SiCurl,
} from "react-icons/si"
// OpenAI/Heroku were removed from react-icons/si in 5.7.0 — vendored locally.
import { SiOpenai, SiHeroku } from "@/components/icons/si-fallback"

// Brand-icon and brand-color tables for the feature picker, plus the
// shared featureRefToTool helper that maps an OCI ref to a known slug.
// Extracted from runtime-config.tsx for readability — these are pure
// data and lookup helpers, no JSX.

const BRAND_ICONS: Record<string, IconType> = {
  // Operating systems — what a base image usually names.
  ubuntu: SiUbuntu,
  debian: SiDebian,
  bookworm: SiDebian,
  "debian-slim": SiDebian,
  alpine: SiAlpinelinux,
  "alpine-linux": SiAlpinelinux,
  linux: SiLinux,
  rhel: SiRedhat,
  redhat: SiRedhat,
  fedora: SiFedora,
  arch: SiArchlinux,
  archlinux: SiArchlinux,
  // devcontainer features that shipped without a mark.
  anaconda: SiAnaconda,
  conda: SiAnaconda,
  miniconda: SiAnaconda,
  copilot: SiGithubcopilot,
  "copilot-cli": SiGithubcopilot,
  jupyter: SiJupyter,
  jupyterlab: SiJupyter,
  "desktop-lite": SiXfce,
  rstudio: SiRstudioide,
  poetry: SiPoetry,
  gradle: SiGradle,
  maven: SiApachemaven,
  typescript: SiTypescript,
  ts: SiTypescript,
  jfrog: SiJfrog,
  artifactory: SiJfrog,
  snyk: SiSnyk,
  gpg: SiGnuprivacyguard,
  gnupg: SiGnuprivacyguard,
  curl: SiCurl,
  python: SiPython,
  node: SiNodedotjs,
  nodejs: SiNodedotjs,
  "node.js": SiNodedotjs,
  go: SiGo,
  golang: SiGo,
  rust: SiRust,
  ruby: SiRuby,
  php: SiPhp,
  java: SiOpenjdk,
  openjdk: SiOpenjdk,
  jdk: SiOpenjdk,
  elixir: SiElixir,
  erlang: SiErlang,
  deno: SiDeno,
  bun: SiBun,
  dotnet: SiDotnet,
  "dotnet-core": SiDotnet,
  kotlin: SiKotlin,
  scala: SiScala,
  swift: SiSwift,
  zig: SiZig,
  crystal: SiCrystal,
  docker: SiDocker,
  "docker-in-docker": SiDocker,
  "docker-outside-of-docker": SiDocker,
  "docker-from-docker": SiDocker,
  kubectl: SiKubernetes,
  "kubectl-helm-minikube": SiKubernetes,
  kubernetes: SiKubernetes,
  k8s: SiKubernetes,
  minikube: SiKubernetes,
  helm: SiHelm,
  terraform: SiTerraform,
  ansible: SiAnsible,
  gcloud: SiGooglecloud,
  "google-cloud": SiGooglecloud,
  "google-cloud-cli": SiGooglecloud,
  digitalocean: SiDigitalocean,
  doctl: SiDigitalocean,
  github: SiGithub,
  "github-cli": SiGithub,
  gh: SiGithub,
  gitlab: SiGitlab,
  "glab-cli": SiGitlab,
  git: SiGit,
  "git-lfs": SiGit,
  postgres: SiPostgresql,
  postgresql: SiPostgresql,
  psql: SiPostgresql,
  mysql: SiMysql,
  mariadb: SiMariadb,
  redis: SiRedis,
  "redis-cli": SiRedis,
  mongo: SiMongodb,
  mongodb: SiMongodb,
  mongosh: SiMongodb,
  sqlite: SiSqlite,
  sqlite3: SiSqlite,
  hashicorp: SiHashicorp,
  vault: SiVault,
  pulumi: SiPulumi,
  pnpm: SiPnpm,
  yarn: SiYarn,
  npm: SiNpm,
  vim: SiVim,
  neovim: SiVim,
  nvim: SiVim,
  zsh: SiZsh,
  bash: SiGnubash,
  sh: SiGnubash,
  firebase: SiFirebase,
  supabase: SiSupabase,
  nginx: SiNginx,
  apache: SiApache,
  httpd: SiApache,
  flutter: SiFlutter,
  dart: SiDart,
  elm: SiElm,
  hugo: SiHugo,
  vite: SiVite,
  webpack: SiWebpack,
  julia: SiJulia,
  lua: SiLua,
  perl: SiPerl,
  r: SiR,
  haskell: SiHaskell,
  ghc: SiHaskell,
  graphql: SiGraphql,
  openai: SiOpenai,
  anthropic: SiAnthropic,
  claude: SiAnthropic,
  // AWS family (no dedicated react-icons entry; use lucide Cloud fallback)
  aws: Cloud,
  "aws-cli": Cloud,
  awscli: Cloud,
  // Azure family (no dedicated react-icons entry; use lucide Cloud fallback)
  azure: Cloud,
  "azure-cli": Cloud,
  az: Cloud,
  // Heroku
  heroku: SiHeroku,
  "heroku-cli": SiHeroku,
  // Vercel
  vercel: SiVercel,
  "vercel-cli": SiVercel,
  // Cloudflare
  cloudflare: SiCloudflare,
  "cloudflare-cli": SiCloudflare,
  wrangler: SiCloudflare,
  flarectl: SiCloudflare,
  // Fly.io
  fly: SiFlydotio,
  "fly-cli": SiFlydotio,
  flyctl: SiFlydotio,
  // Ollama
  ollama: SiOllama,
  // Sentry
  sentry: SiSentry,
  "sentry-cli": SiSentry,
  // Datadog
  datadog: SiDatadog,
  "datadog-ci": SiDatadog,
  // Railway
  railway: SiRailway,
  // Netlify
  netlify: SiNetlify,
}


function getBrandIcon(tool: string): IconType | null {
  if (!tool) return null
  const key = tool.toLowerCase()
  if (BRAND_ICONS[key]) return BRAND_ICONS[key]
  // Try stripping common suffixes/prefixes
  const stripped = key.replace(/-cli$|^cli-/, "").replace(/\d+$/, "")
  return BRAND_ICONS[stripped] || null
}

// ---- Brand color map -----------------------------------------------------
//
// Official brand colors taken from each project's style guide / brand
// page (cross-referenced with Simple Icons, which mirrors the same
// official hex values). Pure-black logos (GitHub, HashiCorp, Vercel,
// Ollama, Railway, Crystal) are rendered as #FFFFFF on this dark
// theme — that's the brand-recommended dark-mode treatment.

const BRAND_COLORS: Record<string, string> = {
  // Official brand colours, same source as the rest of this table: an icon
  // tinted grey is a shape, and shapes at 14px all look alike.
  bookworm: "#A81D33",
  "debian-slim": "#A81D33",
  "alpine-linux": "#0D597F",
  linux: "#FCC624",
  rhel: "#EE0000",
  redhat: "#EE0000",
  fedora: "#51A2DA",
  arch: "#1793D1",
  archlinux: "#1793D1",
  conda: "#44A833",
  miniconda: "#44A833",
  copilot: "#8957E5",
  "copilot-cli": "#8957E5",
  jupyter: "#F37626",
  jupyterlab: "#F37626",
  "desktop-lite": "#2284F2",
  rstudio: "#75AADB",
  poetry: "#60A5FA",
  gradle: "#02303A",
  maven: "#C71A36",
  typescript: "#3178C6",
  ts: "#3178C6",
  jfrog: "#41BF47",
  artifactory: "#41BF47",
  snyk: "#4C4A73",
  gpg: "#0093DD",
  gnupg: "#0093DD",
  curl: "#073551",
  python: "#3776AB",
  node: "#339933",
  nodejs: "#339933",
  "node.js": "#339933",
  go: "#00ADD8",
  golang: "#00ADD8",
  rust: "#DEA584",
  ruby: "#CC342D",
  php: "#777BB4",
  java: "#ED8B00",
  openjdk: "#ED8B00",
  jdk: "#ED8B00",
  elixir: "#4B275F",
  erlang: "#A90533",
  deno: "#70FFAF",
  bun: "#FBF0DF",
  dotnet: "#512BD4",
  "dotnet-core": "#512BD4",
  kotlin: "#7F52FF",
  scala: "#DC322F",
  swift: "#F05138",
  zig: "#F7A41D",
  crystal: "#FFFFFF",
  docker: "#2496ED",
  "docker-in-docker": "#2496ED",
  "docker-outside-of-docker": "#2496ED",
  "docker-from-docker": "#2496ED",
  kubectl: "#326CE5",
  "kubectl-helm-minikube": "#326CE5",
  kubernetes: "#326CE5",
  k8s: "#326CE5",
  minikube: "#326CE5",
  helm: "#0F1689",
  terraform: "#7B42BC",
  ansible: "#EE0000",
  gcloud: "#4285F4",
  "google-cloud": "#4285F4",
  "google-cloud-cli": "#4285F4",
  digitalocean: "#0080FF",
  doctl: "#0080FF",
  github: "#FFFFFF",
  "github-cli": "#FFFFFF",
  gh: "#FFFFFF",
  gitlab: "#FC6D26",
  "glab-cli": "#FC6D26",
  git: "#F05032",
  "git-lfs": "#F05032",
  postgres: "#4169E1",
  postgresql: "#4169E1",
  psql: "#4169E1",
  mysql: "#4479A1",
  mariadb: "#003545",
  redis: "#DC382D",
  "redis-cli": "#DC382D",
  mongo: "#47A248",
  mongodb: "#47A248",
  mongosh: "#47A248",
  sqlite: "#003B57",
  sqlite3: "#003B57",
  hashicorp: "#FFFFFF",
  vault: "#FFEC6E",
  pulumi: "#8A3391",
  pnpm: "#F69220",
  yarn: "#2C8EBB",
  npm: "#CB3837",
  vim: "#019733",
  neovim: "#019733",
  nvim: "#019733",
  zsh: "#FFFFFF",
  bash: "#4EAA25",
  sh: "#4EAA25",
  firebase: "#FFCA28",
  supabase: "#3ECF8E",
  nginx: "#009639",
  apache: "#D22128",
  httpd: "#D22128",
  flutter: "#02569B",
  dart: "#0175C2",
  elm: "#1293D8",
  hugo: "#FF4088",
  vite: "#646CFF",
  webpack: "#8DD6F9",
  julia: "#9558B2",
  lua: "#2C2D72",
  perl: "#39457E",
  r: "#276DC3",
  haskell: "#5D4F85",
  ghc: "#5D4F85",
  graphql: "#E10098",
  openai: "#412991",
  anthropic: "#D97757",
  claude: "#D97757",
  aws: "#FF9900",
  "aws-cli": "#FF9900",
  awscli: "#FF9900",
  azure: "#0078D4",
  "azure-cli": "#0078D4",
  az: "#0078D4",
  heroku: "#430098",
  "heroku-cli": "#430098",
  vercel: "#FFFFFF",
  "vercel-cli": "#FFFFFF",
  cloudflare: "#F38020",
  "cloudflare-cli": "#F38020",
  wrangler: "#F38020",
  flarectl: "#F38020",
  fly: "#7B3FE4",
  "fly-cli": "#7B3FE4",
  flyctl: "#7B3FE4",
  ollama: "#FFFFFF",
  sentry: "#362D59",
  "sentry-cli": "#362D59",
  datadog: "#632CA6",
  "datadog-ci": "#632CA6",
  railway: "#FFFFFF",
  netlify: "#00C7B7",
  // Base image distros
  debian: "#A81D33",
  ubuntu: "#E95420",
  alpinelinux: "#0D597F",
  alpine: "#0D597F",
  // Anaconda: official green
  anaconda: "#44A833",
}


function getBrandColor(tool: string): string | null {
  if (!tool) return null
  const key = tool.toLowerCase()
  if (BRAND_COLORS[key]) return BRAND_COLORS[key]
  const stripped = key.replace(/-cli$|^cli-/, "").replace(/\d+$/, "")
  return BRAND_COLORS[stripped] || null
}


/**
 * The brand hiding in an image reference.
 *
 * `debian:bookworm-slim` is a Debian image and
 * `mcr.microsoft.com/devcontainers/javascript-node:22-bookworm` is a Node one,
 * but neither yields its brand by taking the last segment: the first gives
 * "bookworm-slim" and the second "javascript-node". Try the repository name,
 * then each hyphen-separated word inside it, then the TAG — devcontainers
 * publishes `.../base:ubuntu-24.04`, where the only thing naming the distro is
 * the tag — then the remaining path segments. First hit wins. Returns "" when
 * none does, which is the honest answer for ghcr.io/acme/thing.
 */
function imageBrandKey(image: string): string {
  const withoutDigest = image.split("@")[0]
  const [path, tag = ""] = [withoutDigest.split(":")[0], withoutDigest.split(":")[1]]
  const segments = path.split("/").filter(Boolean)
  const last = segments[segments.length - 1] ?? ""
  const candidates = [
    last,
    ...last.split("-"),
    ...tag.split(/[-.]/),
    ...segments.slice(0, -1).reverse(),
  ]
  for (const c of candidates) {
    if (c && (BRAND_ICONS[c.toLowerCase()] || BRAND_COLORS[c.toLowerCase()])) return c.toLowerCase()
  }
  return ""
}

function featureRefToTool(ref: string): string {
  // ghcr.io/devcontainers/features/python:1 -> "python"
  const withoutTag = ref.split(":")[0]
  const parts = withoutTag.split("/")
  return parts[parts.length - 1] || ""
}

// ---- Constants ------------------------------------------------------------


export {
  BRAND_ICONS,
  BRAND_COLORS,
  getBrandIcon,
  getBrandColor,
  imageBrandKey,
  featureRefToTool,
}
