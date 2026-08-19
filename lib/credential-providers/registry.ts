// Brand registry for credential providers. Backs:
//   • the Add Credential icon picker (browse + search)
//   • auto-detection from name keywords ("notion" → Notion brand)
//   • auto-detection from value prefixes ("ghp_" → GitHub PAT)
//   • render-time icon + brand colour for list rows and detail sheet
//
// Icons come from react-icons/si (Simple Icons, MIT-licensed). Brand
// colours are the official hex values published in Simple Icons
// metadata. Each row is tree-shakable — unused brands don't ship.
//
// Adding a brand: pick its `Si{Name}` from react-icons/si, copy the
// official hex from simpleicons.org, add a row below. Keywords drive
// the substring match in detectBrandFromName — keep them lowercase.
//
// Simple Icons has no mark for a growing list of household names —
// Microsoft, Amazon, LinkedIn, Adobe, Oracle, Canva and the rest were
// pulled upstream over trademark claims. Those rows carry a Lucide
// glyph plus the brand's real hex rather than a hand-drawn SVG: a
// wrong logo is worse than a generic one, a wrong colour worse still.
//
// ORDER IS BEHAVIOUR. detectBrandFromName takes the FIRST keyword hit
// walking this array top to bottom, so a child brand must sit above
// its umbrella ("azuredevops" contains "azure", "applepay" contains
// "apple"). Adding a row in the wrong place silently re-points every
// credential whose name contains the umbrella's keyword; the
// registry tests pin every keyword to its own entry.
//
// The generic fallback is GENERIC_BRAND at the bottom of this file and
// is deliberately NOT a registry row: it must never appear in the
// picker's grid, its search, or detection — it is what you get when
// nothing matched, not a brand you can choose by name.

import type { ComponentType, SVGProps } from "react"
// Lucide stands in for brands Simple Icons has no mark for (Microsoft,
// Amazon, LinkedIn and the rest were pulled upstream over trademark
// claims). The glyph is generic; the brand's own hex is not, which is
// what makes the tile recognisable. Never an invented SVG.
import {
  Key, KeyRound, ShieldCheck, User, Lock,
  Aperture, BarChart3, Briefcase, Building2, CalendarDays, Cloud,
  Database, FileSignature, FileText, FolderOpen, Gamepad2, GitBranch,
  Image, Landmark, LifeBuoy, LineChart, Mail, Megaphone,
  MonitorSmartphone, NotebookPen, Palette, Phone, Search, Send,
  Server, ShoppingCart, Sparkles, Table, Video, Waves, Zap,
} from "lucide-react"

import {
  // AI / inference
  SiAnthropic, SiGooglegemini, SiHuggingface,
  SiPerplexity, SiReplicate, SiOllama, SiElevenlabs,
  // Cloud / infra
  SiGooglecloud, SiCloudflare, SiVercel, SiNetlify,
  SiRailway, SiRender, SiDigitalocean,
  SiSupabase, SiFirebase, SiPlanetscale,
  // Source control / dev
  SiGithub, SiGitlab, SiBitbucket, SiCodeberg, SiJetbrains,
  SiReplit, SiGitpod, SiCodesandbox,
  // Communication
  SiDiscord, SiTelegram, SiWhatsapp, SiSignal,
  SiMailgun, SiResend, SiMailchimp,
  SiZoom,
  // Productivity / docs
  SiNotion, SiLinear, SiAsana, SiTrello, SiJira, SiAtlassian,
  SiAirtable, SiCoda, SiConfluence, SiMiro, SiFigma,
  SiTodoist, SiEvernote, SiBasecamp, SiClickup, SiObsidian,
  SiCalendly,
  // Auth
  SiAuth0, Si1Password, SiOkta, SiLastpass, SiBitwarden,
  SiDashlane, SiClerk,
  // Payments
  SiStripe, SiPaypal, SiSquare, SiKlarna, SiShopify,
  SiWoocommerce, SiVisa, SiMastercard,
  // Analytics / observability
  SiSentry, SiDatadog, SiNewrelic, SiPosthog, SiMixpanel,
  SiPlausibleanalytics, SiGoogleanalytics, SiHotjar,
  SiElastic, SiGrafana, SiPrometheus, SiPagerduty,
  // Search / vector DB
  SiBrave, SiAlgolia, SiMeilisearch, SiElasticsearch,
  SiOpensearch,
  // CI/CD
  SiGithubactions, SiCircleci, SiTravisci, SiJenkins,
  SiBuildkite, SiDocker, SiKubernetes, SiTerraform, SiPulumi,
  // Email / marketing
  SiSubstack,
  // File storage / CDN
  SiBackblaze, SiDropbox, SiGoogledrive, SiCloudinary,
  // Database
  SiPostgresql, SiMysql, SiMongodb, SiRedis, SiSqlite,
  SiCouchbase, SiCockroachlabs, SiMariadb, SiApachecassandra,
  SiSnowflake, SiDatabricks, SiGooglebigquery,
  // Misc dev tools
  SiPostman, SiInsomnia, SiNgrok, SiCypress, SiStorybook,
  SiSwagger, SiGraphql, SiPrisma,
  // Social / APIs
  SiX, SiReddit, SiYoutube, SiTwitch, SiFacebook,
  SiInstagram, SiTiktok, SiSpotify, SiPinterest, SiLine,
  // E-commerce
  SiBigcommerce, SiEtsy,
  // Maps / geo
  SiGooglemaps, SiMapbox, SiOpenstreetmap,
  // ML / data
  SiTensorflow, SiPytorch, SiPandas, SiNumpy, SiScikitlearn,
  SiKeras, SiMlflow, SiNvidia, SiIntel,
  // CRM / marketing
  SiZoho, SiHubspot, SiZapier, SiMake,
  // CMS / web
  SiWebflow, SiWordpress, SiGhost, SiContentful, SiSanity,
  SiStrapi, SiStoryblok,
  // AI / inference (expansion)
  SiMistralai, SiDeepseek, SiOpenrouter, SiGithubcopilot, SiLangchain,
  SiLmstudio, SiDeepgram, SiSuno,
  // Cloud / infra (expansion)
  SiAlibabacloud, SiYandexcloud, SiAkamai, SiFlydotio, SiVultr,
  SiHetzner, SiOvh, SiScaleway, SiUpcloud, SiContabo, SiEquinixmetal,
  SiOpenstack, SiVmware, SiProxmox, SiNutanix,
  // Source control (expansion)
  SiGitea, SiSourcehut,
  // Communication (expansion)
  SiGooglemeet, SiGooglechat, SiWebex, SiGotomeeting, SiMattermost,
  SiMatrix, SiJitsi, SiZulip, SiMessenger, SiWechat, SiKakaotalk,
  SiViber, SiVonage, SiLoom,
  // Productivity / docs (expansion)
  SiGooglecalendar, SiGooglesheets, SiGoogledocs, SiGoogleslides,
  SiGoogleforms, SiGooglekeep, SiSketch, SiFramer, SiTypeform,
  SiSurveymonkey,
  // Auth / security / VPN
  SiYubico, SiVaultwarden, SiVault, SiPassbolt, SiKeeper, SiProtonvpn,
  SiNordvpn, SiExpressvpn, SiTailscale, SiZerotier, SiWireguard,
  SiOpenvpn, SiCisco, SiFortinet, SiPaloaltonetworks, SiUbiquiti,
  SiPfsense, SiSnyk, SiQualys, SiSonarqubecloud,
  // Payments / finance / accounting
  SiAmericanexpress, SiDiscover, SiJcb, SiApplepay, SiGooglepay,
  SiVenmo, SiCashapp, SiZelle, SiPayoneer, SiAdyen, SiRazorpay,
  SiWise, SiRevolut, SiCoinbase, SiBinance, SiRobinhood, SiQuickbooks,
  SiXero, SiGusto, SiExpensify, SiBrex, SiEthereum, SiBitcoin,
  SiSolana, SiAlchemy,
  // Analytics / observability / BI
  SiSplunk, SiSumologic, SiDynatrace, SiRollbar, SiBetterstack,
  SiStatuspage, SiOpsgenie, SiGoogletagmanager, SiGooglesearchconsole,
  SiLooker, SiMetabase, SiApachesuperset, SiRedash, SiQlik, SiSemrush,
  SiSimilarweb,
  // Search (expansion)
  SiDuckduckgo,
  // CI/CD / DevOps (expansion)
  SiAnsible, SiHelm, SiArgo, SiRancher, SiRedhatopenshift, SiNomad,
  SiConsul, SiNginx, SiTeamcity, SiBamboo, SiDrone, SiSemaphoreci,
  SiBitrise, SiFastlane, SiExpo, SiCodecov, SiCoveralls, SiSaucelabs,
  SiPercy, SiChromatic, SiNpm, SiPypi, SiRubygems, SiNuget,
  SiPackagist,
  // File storage / CDN (expansion)
  SiBox, SiIcloud, SiGooglephotos, SiMega, SiNextcloud, SiOwncloud,
  SiSyncthing, SiFilezilla, SiWasabi, SiMinio, SiFastly,
  SiBunnydotnet, SiKeycdn, SiJsdelivr,
  // Database / streaming (expansion)
  SiNeon, SiTurso, SiUpstash, SiInfluxdb, SiTimescale, SiClickhouse,
  SiNeo4J, SiDuckdb, SiArangodb, SiSurrealdb, SiFauna, SiQdrant,
  SiApachekafka, SiRabbitmq, SiNatsdotio, SiApachespark,
  SiApacheairflow, SiAirbyte,
  // Email (expansion)
  SiGmail, SiProtonmail, SiBrevo, SiSparkpost, SiLoops,
  // Social (expansion)
  SiSnapchat, SiThreads, SiMastodon, SiBluesky, SiTumblr, SiMedium,
  SiQuora, SiStackoverflow, SiVimeo, SiDailymotion, SiFlickr,
  SiBehance, SiDribbble, SiUnsplash, SiPexels, SiGiphy, SiVk,
  SiSinaweibo, SiBilibili, SiNaver, SiBaidu,
  // Consumer / everyday apps
  SiEbay, SiAliexpress, SiIkea, SiUber, SiLyft, SiAirbnb,
  SiBookingdotcom, SiExpedia, SiDoordash, SiInstacart, SiDeliveroo,
  SiJusteat, SiGrab, SiYelp, SiTripadvisor, SiNetflix, SiHbomax,
  SiSoundcloud, SiDeezer, SiTidal, SiAudible, SiSteam, SiEpicgames,
  SiPlaystation, SiRoblox, SiDuolingo, SiCoursera, SiUdemy,
  SiKhanacademy, SiEdx, SiIndeed, SiGlassdoor, SiUpwork, SiFiverr,
  SiApplemusic, SiAppletv, SiAppstore, SiApple, SiGoogleplay,
  SiGooglechrome,
  // ML / data (expansion)
  SiWeightsandbiases, SiKaggle, SiGooglecolab, SiRoboflow,
  // CRM / support / marketing (expansion)
  SiZendesk, SiIntercom, SiHelpscout, SiLivechat, SiN8N, SiGoogleads,
  SiBuffer, SiHootsuite, SiGreenhouse, SiSap,
} from "react-icons/si"

// AWS doesn't have a usable Simple Icons entry, so we keep our own
// curated SVG. Same for Cursor and Factory.
import {
  AWSIcon, CursorIcon, FactoryIcon, CustomCLIIcon,
} from "@/components/icons/provider-icons"
// Slack/OpenAI/Twilio/Heroku/SendGrid/Salesforce were removed from
// react-icons/si in 5.7.0 — vendored locally with the same signature.
import {
  SiSlack, SiOpenai, SiTwilio, SiHeroku, SiSendgrid, SiSalesforce,
} from "@/components/icons/si-fallback"

export type IconComponent = ComponentType<SVGProps<SVGSVGElement>>

export interface BrandEntry {
  key: string         // stable upper-snake id, also stored in credentials.provider
  label: string       // display name
  hex: string         // official brand colour (used on light surfaces)
  // darkHex overrides hex on dark surfaces when the official brand is
  // black/near-black and would otherwise render invisible against the
  // app's dark theme. Vercel's own site flips its mark to white on
  // dark backgrounds for the same reason. Optional — when omitted the
  // canonical hex is used everywhere.
  darkHex?: string
  Icon: IconComponent
  category: BrandCategory
  keywords?: string[] // additional name-substring matches (lowercase)
  prefixes?: string[] // value prefixes for paste detection
  // cli flags providers that Crewship itself uses inside agent
  // containers (Claude Code, Codex CLI, Gemini CLI, Cursor CLI,
  // Factory Droid). These are first-class:
  //   • the value can be probed against the upstream API ("Test"
  //     button is shown only when cli is true)
  //   • they're future rotation-pool candidates: multiple keys of
  //     the same brand form a pool that the sidecar cycles through
  //     when one hits a rate-limit or quota wall
  //
  // Brands without cli are passive secrets — encrypt, store, hand to
  // the agent as an env var, no further interaction.
  cli?: boolean
}

// brandColor returns the hex to use for rendering. The app is dark by
// default, so any black brand picks up its darkHex automatically; if a
// brand has no darkHex its hex is used as-is.
//
// INVARIANT: app is dark-by-default; revisit this helper's signature
// when a theme toggle ships — at that point this needs an isDark arg
// or a useTheme() call. Search for "INVARIANT: app is dark" before
// adding light-theme support.
export function brandColor(b: BrandEntry): string {
  return b.darkHex ?? b.hex
}

export type BrandCategory =
  | "AI"
  | "Cloud"
  | "DevOps"
  | "Source"
  | "Comms"
  | "Productivity"
  | "Auth"
  | "Payments"
  | "Analytics"
  | "Search"
  | "Database"
  | "Storage"
  | "Email"
  | "Social"
  | "Marketing"
  | "Maps"
  | "ML"
  | "Other"

// REGISTRY ────────────────────────────────────────────────────────────
// Order within a category is roughly popularity-first to give the
// picker a sensible default top row.

export const BRAND_REGISTRY: BrandEntry[] = [
  // ─── AI / inference ─────────────────────────────────────────────
  { key: "ANTHROPIC", label: "Anthropic", hex: "#D97757", Icon: SiAnthropic as IconComponent, category: "AI", keywords: ["anthropic", "claude"], prefixes: ["sk-ant-"], cli: true },
  { key: "OPENAI", label: "OpenAI", hex: "#412991", Icon: SiOpenai as IconComponent, category: "AI", keywords: ["openai", "chatgpt", "gpt", "oai_"], prefixes: ["sk-proj-", "sk-svcacct-"], cli: true },
  { key: "GOOGLE", label: "Google AI / Gemini", hex: "#4285F4", Icon: SiGooglegemini as IconComponent, category: "AI", keywords: ["google", "gemini", "googleai", "vertex"], prefixes: ["AIza"], cli: true },
  { key: "HUGGINGFACE", label: "Hugging Face", hex: "#FFD21E", Icon: SiHuggingface as IconComponent, category: "AI", keywords: ["huggingface", "hf_"], prefixes: ["hf_"] },
  { key: "PERPLEXITY", label: "Perplexity", hex: "#1FB8CD", Icon: SiPerplexity as IconComponent, category: "AI", keywords: ["perplexity", "pplx"], prefixes: ["pplx-"] },
  { key: "REPLICATE", label: "Replicate", hex: "#000000", darkHex: "#FFFFFF", Icon: SiReplicate as IconComponent, category: "AI", keywords: ["replicate"], prefixes: ["r8_"] },
  { key: "OLLAMA", label: "Ollama", hex: "#000000", darkHex: "#FFFFFF", Icon: SiOllama as IconComponent, category: "AI", keywords: ["ollama"] },
  { key: "ELEVENLABS", label: "ElevenLabs", hex: "#000000", darkHex: "#FFFFFF", Icon: SiElevenlabs as IconComponent, category: "AI", keywords: ["elevenlabs", "eleven_labs", "11labs"] },
  { key: "CURSOR", label: "Cursor", hex: "#000000", darkHex: "#FFFFFF", Icon: CursorIcon, category: "AI", keywords: ["cursor"], cli: true },
  { key: "FACTORY", label: "Factory Droid", hex: "#000000", darkHex: "#FFFFFF", Icon: FactoryIcon, category: "AI", keywords: ["factory", "droid"], cli: true },

  // ─── AI / inference (expansion) ────────────────────────────────
  { key: "MISTRAL", label: "Mistral AI", hex: "#FA520F", Icon: SiMistralai as IconComponent, category: "AI", keywords: ["mistral"] },
  { key: "DEEPSEEK", label: "DeepSeek", hex: "#5786FE", Icon: SiDeepseek as IconComponent, category: "AI", keywords: ["deepseek"] },
  { key: "OPENROUTER", label: "OpenRouter", hex: "#94A3B8", Icon: SiOpenrouter as IconComponent, category: "AI", keywords: ["openrouter"], prefixes: ["sk-or-"] },
  // No "grok" keyword: "ngrok" contains it, and AI is walked before
  // DevOps, so every ngrok token would arrive wearing an xAI badge.
  { key: "XAI", label: "xAI / Grok", hex: "#000000", darkHex: "#FFFFFF", Icon: Sparkles as unknown as IconComponent, category: "AI", keywords: ["xai", "x.ai"] },
  { key: "GROQ", label: "Groq", hex: "#F55036", Icon: Zap as unknown as IconComponent, category: "AI", keywords: ["groq"], prefixes: ["gsk_"] },
  { key: "COHERE", label: "Cohere", hex: "#39594D", darkHex: "#7BAF9B", Icon: Waves as unknown as IconComponent, category: "AI", keywords: ["cohere"] },
  { key: "MIDJOURNEY", label: "Midjourney", hex: "#000000", darkHex: "#FFFFFF", Icon: Image as unknown as IconComponent, category: "AI", keywords: ["midjourney"] },
  { key: "COPILOT", label: "GitHub Copilot", hex: "#000000", darkHex: "#FFFFFF", Icon: SiGithubcopilot as IconComponent, category: "AI", keywords: ["copilot", "githubcopilot"] },
  { key: "LANGCHAIN", label: "LangChain", hex: "#7FC8FF", Icon: SiLangchain as IconComponent, category: "AI", keywords: ["langchain"] },
  { key: "LMSTUDIO", label: "LM Studio", hex: "#000000", darkHex: "#FFFFFF", Icon: SiLmstudio as IconComponent, category: "AI", keywords: ["lmstudio", "lm studio"] },
  { key: "DEEPGRAM", label: "Deepgram", hex: "#13EF93", Icon: SiDeepgram as IconComponent, category: "AI", keywords: ["deepgram"] },
  { key: "SUNO", label: "Suno", hex: "#000000", darkHex: "#FFFFFF", Icon: SiSuno as IconComponent, category: "AI", keywords: ["suno"] },

  // ─── Cloud / infra ──────────────────────────────────────────────
  // "amazon" moved off AWS and onto the retail brand below: it was the
  // only spelling by which the consumer account could ever be detected,
  // whereas AWS still answers to "aws", "amazonaws" and — decisively —
  // its AKIA/ASIA key prefixes, which beat any name guess.
  { key: "AWS", label: "AWS", hex: "#FF9900", Icon: AWSIcon, category: "Cloud", keywords: ["aws", "amazonaws"], prefixes: ["AKIA", "ASIA"] },
  { key: "GCP", label: "Google Cloud", hex: "#4285F4", Icon: SiGooglecloud as IconComponent, category: "Cloud", keywords: ["gcp", "googlecloud", "gcloud"] },
  { key: "CLOUDFLARE", label: "Cloudflare", hex: "#F38020", Icon: SiCloudflare as IconComponent, category: "Cloud", keywords: ["cloudflare", "cf_"] },
  { key: "VERCEL", label: "Vercel", hex: "#000000", darkHex: "#FFFFFF", Icon: SiVercel as IconComponent, category: "Cloud", keywords: ["vercel"] },
  { key: "NETLIFY", label: "Netlify", hex: "#00C7B7", Icon: SiNetlify as IconComponent, category: "Cloud", keywords: ["netlify"] },
  { key: "RAILWAY", label: "Railway", hex: "#0B0D0E", darkHex: "#FFFFFF", Icon: SiRailway as IconComponent, category: "Cloud", keywords: ["railway"] },
  { key: "RENDER", label: "Render", hex: "#46E3B7", Icon: SiRender as IconComponent, category: "Cloud", keywords: ["render"] },
  { key: "DIGITALOCEAN", label: "DigitalOcean", hex: "#0080FF", Icon: SiDigitalocean as IconComponent, category: "Cloud", keywords: ["digitalocean", "do_"] },
  { key: "HEROKU", label: "Heroku", hex: "#430098", darkHex: "#A98DD0", Icon: SiHeroku as IconComponent, category: "Cloud", keywords: ["heroku"] },
  { key: "SUPABASE", label: "Supabase", hex: "#3ECF8E", Icon: SiSupabase as IconComponent, category: "Cloud", keywords: ["supabase"] },
  { key: "FIREBASE", label: "Firebase", hex: "#DD2C00", Icon: SiFirebase as IconComponent, category: "Cloud", keywords: ["firebase", "fcm"] },
  { key: "PLANETSCALE", label: "PlanetScale", hex: "#000000", darkHex: "#FFFFFF", Icon: SiPlanetscale as IconComponent, category: "Cloud", keywords: ["planetscale"] },

  // ─── Cloud / infra (expansion) ─────────────────────────────────
  { key: "AZURE_DEVOPS", label: "Azure DevOps", hex: "#0078D7", Icon: GitBranch as unknown as IconComponent, category: "DevOps", keywords: ["azuredevops", "azure devops", "azure_devops", "vsts"] },
  { key: "AZURE", label: "Microsoft Azure", hex: "#0078D4", Icon: Cloud as unknown as IconComponent, category: "Cloud", keywords: ["azure"] },
  { key: "IBM_CLOUD", label: "IBM Cloud", hex: "#1261FE", Icon: Cloud as unknown as IconComponent, category: "Cloud", keywords: ["ibmcloud", "ibm cloud"] },
  { key: "IBM", label: "IBM", hex: "#052FAD", darkHex: "#6E96FF", Icon: Building2 as unknown as IconComponent, category: "Cloud", keywords: ["ibm"] },
  { key: "ORACLE", label: "Oracle Cloud", hex: "#F80000", Icon: Database as unknown as IconComponent, category: "Cloud", keywords: ["oracle"] },
  { key: "ALIBABA_CLOUD", label: "Alibaba Cloud", hex: "#FF6A00", Icon: SiAlibabacloud as IconComponent, category: "Cloud", keywords: ["alibabacloud", "aliyun"] },
  { key: "TENCENT", label: "Tencent", hex: "#EB1923", Icon: Cloud as unknown as IconComponent, category: "Cloud", keywords: ["tencent"] },
  { key: "YANDEX_CLOUD", label: "Yandex Cloud", hex: "#5282FF", Icon: SiYandexcloud as IconComponent, category: "Cloud", keywords: ["yandex"] },
  { key: "LINODE", label: "Linode", hex: "#00A95C", Icon: Server as unknown as IconComponent, category: "Cloud", keywords: ["linode"] },
  { key: "AKAMAI", label: "Akamai", hex: "#0096D6", Icon: SiAkamai as IconComponent, category: "Cloud", keywords: ["akamai"] },
  { key: "FLYIO", label: "Fly.io", hex: "#24175B", darkHex: "#7A64D8", Icon: SiFlydotio as IconComponent, category: "Cloud", keywords: ["fly.io", "flyio"] },
  { key: "VULTR", label: "Vultr", hex: "#007BFC", Icon: SiVultr as IconComponent, category: "Cloud", keywords: ["vultr"] },
  { key: "HETZNER", label: "Hetzner", hex: "#D50C2D", Icon: SiHetzner as IconComponent, category: "Cloud", keywords: ["hetzner"] },
  { key: "OVH", label: "OVHcloud", hex: "#123F6D", darkHex: "#599DE4", Icon: SiOvh as IconComponent, category: "Cloud", keywords: ["ovh"] },
  { key: "SCALEWAY", label: "Scaleway", hex: "#4F0599", darkHex: "#9E43F9", Icon: SiScaleway as IconComponent, category: "Cloud", keywords: ["scaleway"] },
  { key: "UPCLOUD", label: "UpCloud", hex: "#7B00FF", Icon: SiUpcloud as IconComponent, category: "Cloud", keywords: ["upcloud"] },
  { key: "CONTABO", label: "Contabo", hex: "#00AAEB", Icon: SiContabo as IconComponent, category: "Cloud", keywords: ["contabo"] },
  { key: "EQUINIX", label: "Equinix Metal", hex: "#ED2224", Icon: SiEquinixmetal as IconComponent, category: "Cloud", keywords: ["equinix"] },
  { key: "OPENSTACK", label: "OpenStack", hex: "#ED1944", Icon: SiOpenstack as IconComponent, category: "Cloud", keywords: ["openstack"] },
  { key: "VMWARE", label: "VMware", hex: "#607078", Icon: SiVmware as IconComponent, category: "Cloud", keywords: ["vmware", "vsphere"] },
  { key: "PROXMOX", label: "Proxmox", hex: "#E57000", Icon: SiProxmox as IconComponent, category: "Cloud", keywords: ["proxmox"] },
  { key: "NUTANIX", label: "Nutanix", hex: "#024DA1", Icon: SiNutanix as IconComponent, category: "Cloud", keywords: ["nutanix"] },

  // ─── Source control / dev ───────────────────────────────────────
  { key: "GITHUB", label: "GitHub", hex: "#181717", darkHex: "#FFFFFF", Icon: SiGithub as IconComponent, category: "Source", keywords: ["github", "gh_"], prefixes: ["ghp_", "gho_", "ghs_", "github_pat_", "ghu_"] },
  { key: "GITLAB", label: "GitLab", hex: "#FC6D26", Icon: SiGitlab as IconComponent, category: "Source", keywords: ["gitlab", "gl_"], prefixes: ["glpat-"] },
  { key: "BITBUCKET", label: "Bitbucket", hex: "#0052CC", Icon: SiBitbucket as IconComponent, category: "Source", keywords: ["bitbucket"] },
  { key: "CODEBERG", label: "Codeberg", hex: "#2185D0", Icon: SiCodeberg as IconComponent, category: "Source", keywords: ["codeberg"] },
  { key: "JETBRAINS", label: "JetBrains", hex: "#000000", darkHex: "#FFFFFF", Icon: SiJetbrains as IconComponent, category: "Source", keywords: ["jetbrains", "intellij", "pycharm", "webstorm"] },
  { key: "REPLIT", label: "Replit", hex: "#F26207", Icon: SiReplit as IconComponent, category: "Source", keywords: ["replit"] },
  { key: "GITPOD", label: "Gitpod", hex: "#FFAE33", Icon: SiGitpod as IconComponent, category: "Source", keywords: ["gitpod"] },
  { key: "CODESANDBOX", label: "CodeSandbox", hex: "#151515", darkHex: "#FFFFFF", Icon: SiCodesandbox as IconComponent, category: "Source", keywords: ["codesandbox"] },

  // ─── Source control (expansion) ────────────────────────────────
  { key: "GITEA", label: "Gitea", hex: "#609926", Icon: SiGitea as IconComponent, category: "Source", keywords: ["gitea"] },
  { key: "SOURCEHUT", label: "SourceHut", hex: "#000000", darkHex: "#FFFFFF", Icon: SiSourcehut as IconComponent, category: "Source", keywords: ["sourcehut", "sr.ht"] },

  // ─── Communication ──────────────────────────────────────────────
  { key: "SLACK", label: "Slack", hex: "#4A154B", darkHex: "#ECB22E", Icon: SiSlack as IconComponent, category: "Comms", keywords: ["slack"], prefixes: ["xoxb-", "xoxp-", "xoxa-"] },
  { key: "DISCORD", label: "Discord", hex: "#5865F2", Icon: SiDiscord as IconComponent, category: "Comms", keywords: ["discord"] },
  { key: "TELEGRAM", label: "Telegram", hex: "#26A5E4", Icon: SiTelegram as IconComponent, category: "Comms", keywords: ["telegram"] },
  { key: "WHATSAPP", label: "WhatsApp", hex: "#25D366", Icon: SiWhatsapp as IconComponent, category: "Comms", keywords: ["whatsapp"] },
  { key: "SIGNAL", label: "Signal", hex: "#3A76F0", Icon: SiSignal as IconComponent, category: "Comms", keywords: ["signal"] },
  { key: "TWILIO", label: "Twilio", hex: "#F22F46", Icon: SiTwilio as IconComponent, category: "Comms", keywords: ["twilio"] },
  { key: "SENDGRID", label: "SendGrid", hex: "#1A82E2", Icon: SiSendgrid as IconComponent, category: "Comms", keywords: ["sendgrid"], prefixes: ["SG."] },
  { key: "MAILGUN", label: "Mailgun", hex: "#F06B66", Icon: SiMailgun as IconComponent, category: "Comms", keywords: ["mailgun"] },
  { key: "RESEND", label: "Resend", hex: "#000000", darkHex: "#FFFFFF", Icon: SiResend as IconComponent, category: "Comms", keywords: ["resend"], prefixes: ["re_"] },
  { key: "MAILCHIMP", label: "Mailchimp", hex: "#FFE01B", Icon: SiMailchimp as IconComponent, category: "Comms", keywords: ["mailchimp"] },
  { key: "ZOOM", label: "Zoom", hex: "#0B5CFF", Icon: SiZoom as IconComponent, category: "Comms", keywords: ["zoom"] },
  // Not the bare word "line": it is a substring of "linear", and Comms
  // is walked before Productivity, so LINE was quietly stealing every
  // Linear credential. The picker still finds this row by label.
  { key: "LINE", label: "LINE", hex: "#00C300", Icon: SiLine as IconComponent, category: "Comms", keywords: ["linebot", "line messaging", "linemessaging"] },

  // ─── Communication (expansion) ─────────────────────────────────
  { key: "MSTEAMS", label: "Microsoft Teams", hex: "#6264A7", Icon: Video as unknown as IconComponent, category: "Comms", keywords: ["msteams", "microsoftteams", "microsoft_teams", "microsoft teams", "teams"] },
  { key: "GOOGLE_MEET", label: "Google Meet", hex: "#00897B", Icon: SiGooglemeet as IconComponent, category: "Comms", keywords: ["googlemeet", "gmeet"] },
  { key: "GOOGLE_CHAT", label: "Google Chat", hex: "#34A853", Icon: SiGooglechat as IconComponent, category: "Comms", keywords: ["googlechat"] },
  { key: "WEBEX", label: "Webex", hex: "#000000", darkHex: "#FFFFFF", Icon: SiWebex as IconComponent, category: "Comms", keywords: ["webex"] },
  { key: "GOTOMEETING", label: "GoTo Meeting", hex: "#F68D2E", Icon: SiGotomeeting as IconComponent, category: "Comms", keywords: ["gotomeeting"] },
  { key: "SKYPE", label: "Skype", hex: "#00AFF0", Icon: Phone as unknown as IconComponent, category: "Comms", keywords: ["skype"] },
  { key: "MATTERMOST", label: "Mattermost", hex: "#0058CC", Icon: SiMattermost as IconComponent, category: "Comms", keywords: ["mattermost"] },
  { key: "MATRIX", label: "Matrix", hex: "#000000", darkHex: "#FFFFFF", Icon: SiMatrix as IconComponent, category: "Comms", keywords: ["matrix"] },
  { key: "JITSI", label: "Jitsi", hex: "#97979A", Icon: SiJitsi as IconComponent, category: "Comms", keywords: ["jitsi"] },
  { key: "ZULIP", label: "Zulip", hex: "#6492FE", Icon: SiZulip as IconComponent, category: "Comms", keywords: ["zulip"] },
  { key: "MESSENGER", label: "Messenger", hex: "#0866FF", Icon: SiMessenger as IconComponent, category: "Comms", keywords: ["messenger"] },
  { key: "WECHAT", label: "WeChat", hex: "#07C160", Icon: SiWechat as IconComponent, category: "Comms", keywords: ["wechat"] },
  { key: "KAKAOTALK", label: "KakaoTalk", hex: "#FFCD00", Icon: SiKakaotalk as IconComponent, category: "Comms", keywords: ["kakao"] },
  { key: "VIBER", label: "Viber", hex: "#7360F2", Icon: SiViber as IconComponent, category: "Comms", keywords: ["viber"] },
  { key: "VONAGE", label: "Vonage", hex: "#000000", darkHex: "#FFFFFF", Icon: SiVonage as IconComponent, category: "Comms", keywords: ["vonage", "nexmo"] },
  { key: "LOOM", label: "Loom", hex: "#625DF5", Icon: SiLoom as IconComponent, category: "Comms", keywords: ["loom"] },

  // ─── Productivity / docs ────────────────────────────────────────
  { key: "NOTION", label: "Notion", hex: "#000000", darkHex: "#FFFFFF", Icon: SiNotion as IconComponent, category: "Productivity", keywords: ["notion"], prefixes: ["secret_", "ntn_"] },
  { key: "LINEAR", label: "Linear", hex: "#5E6AD2", Icon: SiLinear as IconComponent, category: "Productivity", keywords: ["linear", "lin_"], prefixes: ["lin_api_", "lin_oauth_"] },
  { key: "ASANA", label: "Asana", hex: "#F06A6A", Icon: SiAsana as IconComponent, category: "Productivity", keywords: ["asana"] },
  { key: "TRELLO", label: "Trello", hex: "#0052CC", Icon: SiTrello as IconComponent, category: "Productivity", keywords: ["trello"] },
  { key: "JIRA", label: "Jira", hex: "#0052CC", Icon: SiJira as IconComponent, category: "Productivity", keywords: ["jira"] },
  { key: "ATLASSIAN", label: "Atlassian", hex: "#0052CC", Icon: SiAtlassian as IconComponent, category: "Productivity", keywords: ["atlassian"] },
  { key: "AIRTABLE", label: "Airtable", hex: "#18BFFF", Icon: SiAirtable as IconComponent, category: "Productivity", keywords: ["airtable"] },
  { key: "CODA", label: "Coda", hex: "#F46A54", Icon: SiCoda as IconComponent, category: "Productivity", keywords: ["coda"] },
  { key: "CONFLUENCE", label: "Confluence", hex: "#172B4D", darkHex: "#4C9AFF", Icon: SiConfluence as IconComponent, category: "Productivity", keywords: ["confluence"] },
  { key: "MIRO", label: "Miro", hex: "#050038", darkHex: "#FFD02F", Icon: SiMiro as IconComponent, category: "Productivity", keywords: ["miro"] },
  { key: "FIGMA", label: "Figma", hex: "#F24E1E", Icon: SiFigma as IconComponent, category: "Productivity", keywords: ["figma"] },
  { key: "TODOIST", label: "Todoist", hex: "#E44332", Icon: SiTodoist as IconComponent, category: "Productivity", keywords: ["todoist"] },
  { key: "EVERNOTE", label: "Evernote", hex: "#00A82D", Icon: SiEvernote as IconComponent, category: "Productivity", keywords: ["evernote"] },
  { key: "BASECAMP", label: "Basecamp", hex: "#1D2D35", darkHex: "#FFFFFF", Icon: SiBasecamp as IconComponent, category: "Productivity", keywords: ["basecamp"] },
  { key: "CLICKUP", label: "ClickUp", hex: "#7B68EE", Icon: SiClickup as IconComponent, category: "Productivity", keywords: ["clickup"] },
  { key: "OBSIDIAN", label: "Obsidian", hex: "#7C3AED", Icon: SiObsidian as IconComponent, category: "Productivity", keywords: ["obsidian"] },
  { key: "CALENDLY", label: "Calendly", hex: "#006BFF", Icon: SiCalendly as IconComponent, category: "Productivity", keywords: ["calendly"] },

  // ─── Productivity / docs (expansion) ───────────────────────────
  { key: "MICROSOFT_365", label: "Microsoft 365", hex: "#D83B01", Icon: FileText as unknown as IconComponent, category: "Productivity", keywords: ["microsoft365", "microsoft_365", "microsoft 365", "m365", "office365"] },
  { key: "SHAREPOINT", label: "SharePoint", hex: "#0078D4", Icon: FolderOpen as unknown as IconComponent, category: "Productivity", keywords: ["sharepoint"] },
  { key: "ONENOTE", label: "OneNote", hex: "#7719AA", darkHex: "#B06AD6", Icon: NotebookPen as unknown as IconComponent, category: "Productivity", keywords: ["onenote"] },
  { key: "EXCEL", label: "Microsoft Excel", hex: "#217346", darkHex: "#4CC38A", Icon: Table as unknown as IconComponent, category: "Productivity", keywords: ["excel"] },
  { key: "GOOGLE_CALENDAR", label: "Google Calendar", hex: "#4285F4", Icon: SiGooglecalendar as IconComponent, category: "Productivity", keywords: ["googlecalendar", "gcal"] },
  { key: "GOOGLE_SHEETS", label: "Google Sheets", hex: "#34A853", Icon: SiGooglesheets as IconComponent, category: "Productivity", keywords: ["googlesheets", "gsheets"] },
  { key: "GOOGLE_DOCS", label: "Google Docs", hex: "#4285F4", Icon: SiGoogledocs as IconComponent, category: "Productivity", keywords: ["googledocs", "gdocs"] },
  { key: "GOOGLE_SLIDES", label: "Google Slides", hex: "#FBBC04", Icon: SiGoogleslides as IconComponent, category: "Productivity", keywords: ["googleslides"] },
  { key: "GOOGLE_FORMS", label: "Google Forms", hex: "#7248B9", Icon: SiGoogleforms as IconComponent, category: "Productivity", keywords: ["googleforms"] },
  { key: "GOOGLE_KEEP", label: "Google Keep", hex: "#FFBB00", Icon: SiGooglekeep as IconComponent, category: "Productivity", keywords: ["googlekeep"] },
  { key: "MONDAY", label: "monday.com", hex: "#FF3D57", Icon: CalendarDays as unknown as IconComponent, category: "Productivity", keywords: ["monday"] },
  { key: "DOCUSIGN", label: "DocuSign", hex: "#FFCC22", Icon: FileSignature as unknown as IconComponent, category: "Productivity", keywords: ["docusign"] },
  { key: "CANVA", label: "Canva", hex: "#00C4CC", Icon: Palette as unknown as IconComponent, category: "Productivity", keywords: ["canva"] },
  { key: "ADOBE", label: "Adobe", hex: "#FF0000", Icon: Aperture as unknown as IconComponent, category: "Productivity", keywords: ["adobe", "creativecloud"] },
  { key: "SKETCH", label: "Sketch", hex: "#F7B500", Icon: SiSketch as IconComponent, category: "Productivity", keywords: ["sketch"] },
  { key: "FRAMER", label: "Framer", hex: "#0055FF", Icon: SiFramer as IconComponent, category: "Productivity", keywords: ["framer"] },
  { key: "TYPEFORM", label: "Typeform", hex: "#262627", darkHex: "#FFFFFF", Icon: SiTypeform as IconComponent, category: "Productivity", keywords: ["typeform"] },
  { key: "SURVEYMONKEY", label: "SurveyMonkey", hex: "#00BF6F", Icon: SiSurveymonkey as IconComponent, category: "Productivity", keywords: ["surveymonkey"] },

  // ─── Auth ───────────────────────────────────────────────────────
  { key: "AUTH0", label: "Auth0", hex: "#EB5424", Icon: SiAuth0 as IconComponent, category: "Auth", keywords: ["auth0"] },
  { key: "OKTA", label: "Okta", hex: "#007DC1", Icon: SiOkta as IconComponent, category: "Auth", keywords: ["okta"] },
  { key: "ONEPASSWORD", label: "1Password", hex: "#0572EC", Icon: Si1Password as IconComponent, category: "Auth", keywords: ["1password", "onepassword"], prefixes: ["ops_"] },
  { key: "LASTPASS", label: "LastPass", hex: "#D32D27", Icon: SiLastpass as IconComponent, category: "Auth", keywords: ["lastpass"] },
  { key: "BITWARDEN", label: "Bitwarden", hex: "#175DDC", Icon: SiBitwarden as IconComponent, category: "Auth", keywords: ["bitwarden"] },
  { key: "DASHLANE", label: "Dashlane", hex: "#0E353D", darkHex: "#22D3EE", Icon: SiDashlane as IconComponent, category: "Auth", keywords: ["dashlane"] },
  { key: "CLERK", label: "Clerk", hex: "#6C47FF", Icon: SiClerk as IconComponent, category: "Auth", keywords: ["clerk"], prefixes: ["sk_test_clerk_", "pk_test_clerk_"] },

  // ─── Auth / security / VPN ─────────────────────────────────────
  { key: "YUBICO", label: "YubiKey", hex: "#84BD00", Icon: SiYubico as IconComponent, category: "Auth", keywords: ["yubico", "yubikey"] },
  { key: "VAULTWARDEN", label: "Vaultwarden", hex: "#000000", darkHex: "#FFFFFF", Icon: SiVaultwarden as IconComponent, category: "Auth", keywords: ["vaultwarden"] },
  { key: "HASHICORP_VAULT", label: "HashiCorp Vault", hex: "#FFEC6E", Icon: SiVault as IconComponent, category: "Auth", keywords: ["hashicorpvault", "vault"] },
  { key: "PASSBOLT", label: "Passbolt", hex: "#D40101", Icon: SiPassbolt as IconComponent, category: "Auth", keywords: ["passbolt"] },
  { key: "KEEPER_SECURITY", label: "Keeper Security", hex: "#FFC700", Icon: SiKeeper as IconComponent, category: "Auth", keywords: ["keepersecurity"] },
  { key: "PROTONVPN", label: "Proton VPN", hex: "#66DEB1", Icon: SiProtonvpn as IconComponent, category: "Auth", keywords: ["protonvpn"] },
  { key: "NORDVPN", label: "NordVPN", hex: "#4687FF", Icon: SiNordvpn as IconComponent, category: "Auth", keywords: ["nordvpn"] },
  { key: "EXPRESSVPN", label: "ExpressVPN", hex: "#DA3940", Icon: SiExpressvpn as IconComponent, category: "Auth", keywords: ["expressvpn"] },
  { key: "TAILSCALE", label: "Tailscale", hex: "#242424", darkHex: "#FFFFFF", Icon: SiTailscale as IconComponent, category: "Auth", keywords: ["tailscale"], prefixes: ["tskey-"] },
  { key: "ZEROTIER", label: "ZeroTier", hex: "#FFB441", Icon: SiZerotier as IconComponent, category: "Auth", keywords: ["zerotier"] },
  { key: "WIREGUARD", label: "WireGuard", hex: "#88171A", darkHex: "#E3595D", Icon: SiWireguard as IconComponent, category: "Auth", keywords: ["wireguard"] },
  { key: "OPENVPN", label: "OpenVPN", hex: "#EA7E20", Icon: SiOpenvpn as IconComponent, category: "Auth", keywords: ["openvpn"] },
  { key: "CISCO", label: "Cisco", hex: "#1BA0D7", Icon: SiCisco as IconComponent, category: "Auth", keywords: ["cisco"] },
  { key: "FORTINET", label: "Fortinet", hex: "#EE3124", Icon: SiFortinet as IconComponent, category: "Auth", keywords: ["fortinet"] },
  { key: "PALOALTO", label: "Palo Alto Networks", hex: "#F04E23", Icon: SiPaloaltonetworks as IconComponent, category: "Auth", keywords: ["paloalto"] },
  { key: "UBIQUITI", label: "Ubiquiti", hex: "#0559C9", Icon: SiUbiquiti as IconComponent, category: "Auth", keywords: ["ubiquiti", "unifi"] },
  { key: "PFSENSE", label: "pfSense", hex: "#212121", darkHex: "#FFFFFF", Icon: SiPfsense as IconComponent, category: "Auth", keywords: ["pfsense"] },
  { key: "SNYK", label: "Snyk", hex: "#4C4A73", Icon: SiSnyk as IconComponent, category: "Auth", keywords: ["snyk"] },
  { key: "QUALYS", label: "Qualys", hex: "#ED2E26", Icon: SiQualys as IconComponent, category: "Auth", keywords: ["qualys"] },
  { key: "SONARQUBE", label: "SonarQube", hex: "#126ED3", Icon: SiSonarqubecloud as IconComponent, category: "Auth", keywords: ["sonarqube", "sonarcloud"] },

  // ─── Payments ───────────────────────────────────────────────────
  { key: "STRIPE", label: "Stripe", hex: "#635BFF", Icon: SiStripe as IconComponent, category: "Payments", keywords: ["stripe"], prefixes: ["sk_test_", "sk_live_", "pk_test_", "pk_live_", "rk_"] },
  { key: "PAYPAL", label: "PayPal", hex: "#003087", Icon: SiPaypal as IconComponent, category: "Payments", keywords: ["paypal"] },
  { key: "SQUARE", label: "Square", hex: "#3E4348", Icon: SiSquare as IconComponent, category: "Payments", keywords: ["square"] },
  { key: "KLARNA", label: "Klarna", hex: "#FFA8CD", Icon: SiKlarna as IconComponent, category: "Payments", keywords: ["klarna"] },
  { key: "SHOPIFY", label: "Shopify", hex: "#7AB55C", Icon: SiShopify as IconComponent, category: "Payments", keywords: ["shopify"], prefixes: ["shpat_", "shpca_", "shppa_"] },
  { key: "WOOCOMMERCE", label: "WooCommerce", hex: "#7F54B3", Icon: SiWoocommerce as IconComponent, category: "Payments", keywords: ["woocommerce"], prefixes: ["ck_", "cs_"] },
  { key: "VISA", label: "Visa", hex: "#1A1F71", darkHex: "#F7B600", Icon: SiVisa as IconComponent, category: "Payments", keywords: ["visa"] },
  { key: "MASTERCARD", label: "Mastercard", hex: "#EB001B", Icon: SiMastercard as IconComponent, category: "Payments", keywords: ["mastercard"] },

  // ─── Payments / finance / accounting ───────────────────────────
  { key: "AMEX", label: "American Express", hex: "#2E77BC", Icon: SiAmericanexpress as IconComponent, category: "Payments", keywords: ["americanexpress", "amex"] },
  { key: "DISCOVER", label: "Discover", hex: "#FF6000", Icon: SiDiscover as IconComponent, category: "Payments", keywords: ["discovercard"] },
  { key: "JCB", label: "JCB", hex: "#0B4EA2", Icon: SiJcb as IconComponent, category: "Payments", keywords: ["jcb"] },
  { key: "APPLE_PAY", label: "Apple Pay", hex: "#000000", darkHex: "#FFFFFF", Icon: SiApplepay as IconComponent, category: "Payments", keywords: ["applepay", "apple_pay", "apple pay"] },
  { key: "GOOGLE_PAY", label: "Google Pay", hex: "#4285F4", Icon: SiGooglepay as IconComponent, category: "Payments", keywords: ["googlepay", "gpay"] },
  { key: "VENMO", label: "Venmo", hex: "#008CFF", Icon: SiVenmo as IconComponent, category: "Payments", keywords: ["venmo"] },
  { key: "CASHAPP", label: "Cash App", hex: "#00C244", Icon: SiCashapp as IconComponent, category: "Payments", keywords: ["cashapp"] },
  { key: "ZELLE", label: "Zelle", hex: "#6D1ED4", Icon: SiZelle as IconComponent, category: "Payments", keywords: ["zelle"] },
  { key: "PAYONEER", label: "Payoneer", hex: "#FF4800", Icon: SiPayoneer as IconComponent, category: "Payments", keywords: ["payoneer"] },
  { key: "ADYEN", label: "Adyen", hex: "#0ABF53", Icon: SiAdyen as IconComponent, category: "Payments", keywords: ["adyen"] },
  { key: "RAZORPAY", label: "Razorpay", hex: "#0C2451", darkHex: "#5688E6", Icon: SiRazorpay as IconComponent, category: "Payments", keywords: ["razorpay"], prefixes: ["rzp_test_", "rzp_live_"] },
  { key: "WISE", label: "Wise", hex: "#9FE870", Icon: SiWise as IconComponent, category: "Payments", keywords: ["wise"] },
  { key: "REVOLUT", label: "Revolut", hex: "#191C1F", darkHex: "#FFFFFF", Icon: SiRevolut as IconComponent, category: "Payments", keywords: ["revolut"] },
  { key: "COINBASE", label: "Coinbase", hex: "#0052FF", Icon: SiCoinbase as IconComponent, category: "Payments", keywords: ["coinbase"] },
  { key: "BINANCE", label: "Binance", hex: "#F0B90B", Icon: SiBinance as IconComponent, category: "Payments", keywords: ["binance"] },
  { key: "ROBINHOOD", label: "Robinhood", hex: "#CCFF00", Icon: SiRobinhood as IconComponent, category: "Payments", keywords: ["robinhood"] },
  { key: "QUICKBOOKS", label: "QuickBooks", hex: "#2CA01C", Icon: SiQuickbooks as IconComponent, category: "Payments", keywords: ["quickbooks"] },
  { key: "XERO", label: "Xero", hex: "#13B5EA", Icon: SiXero as IconComponent, category: "Payments", keywords: ["xero"] },
  { key: "GUSTO", label: "Gusto", hex: "#F45D48", Icon: SiGusto as IconComponent, category: "Payments", keywords: ["gusto"] },
  { key: "EXPENSIFY", label: "Expensify", hex: "#0185FF", Icon: SiExpensify as IconComponent, category: "Payments", keywords: ["expensify"] },
  { key: "BREX", label: "Brex", hex: "#212121", darkHex: "#FFFFFF", Icon: SiBrex as IconComponent, category: "Payments", keywords: ["brex"] },
  { key: "PLAID", label: "Plaid", hex: "#000000", darkHex: "#FFFFFF", Icon: Landmark as unknown as IconComponent, category: "Payments", keywords: ["plaid"] },
  { key: "ETHEREUM", label: "Ethereum", hex: "#3C3C3D", darkHex: "#FFFFFF", Icon: SiEthereum as IconComponent, category: "Payments", keywords: ["ethereum"] },
  { key: "BITCOIN", label: "Bitcoin", hex: "#F7931A", Icon: SiBitcoin as IconComponent, category: "Payments", keywords: ["bitcoin"] },
  { key: "SOLANA", label: "Solana", hex: "#9945FF", Icon: SiSolana as IconComponent, category: "Payments", keywords: ["solana"] },
  { key: "ALCHEMY", label: "Alchemy", hex: "#0C0C0E", darkHex: "#FFFFFF", Icon: SiAlchemy as IconComponent, category: "Payments", keywords: ["alchemy"] },

  // ─── Analytics / observability ──────────────────────────────────
  { key: "SENTRY", label: "Sentry", hex: "#362D59", Icon: SiSentry as IconComponent, category: "Analytics", keywords: ["sentry"] },
  { key: "DATADOG", label: "Datadog", hex: "#632CA6", Icon: SiDatadog as IconComponent, category: "Analytics", keywords: ["datadog", "dd_api"] },
  { key: "NEWRELIC", label: "New Relic", hex: "#1CE783", Icon: SiNewrelic as IconComponent, category: "Analytics", keywords: ["newrelic", "nr_"], prefixes: ["NRAK-", "NRBR-", "NRAA-"] },
  { key: "POSTHOG", label: "PostHog", hex: "#1D4AFF", Icon: SiPosthog as IconComponent, category: "Analytics", keywords: ["posthog"], prefixes: ["phc_", "phx_", "phs_"] },
  { key: "MIXPANEL", label: "Mixpanel", hex: "#7856FF", Icon: SiMixpanel as IconComponent, category: "Analytics", keywords: ["mixpanel"] },
  { key: "PLAUSIBLE", label: "Plausible", hex: "#5850EC", Icon: SiPlausibleanalytics as IconComponent, category: "Analytics", keywords: ["plausible"] },
  { key: "GA", label: "Google Analytics", hex: "#E37400", Icon: SiGoogleanalytics as IconComponent, category: "Analytics", keywords: ["googleanalytics", "ga_"] },
  { key: "HOTJAR", label: "Hotjar", hex: "#FF3C00", Icon: SiHotjar as IconComponent, category: "Analytics", keywords: ["hotjar"] },
  { key: "ELASTIC", label: "Elastic", hex: "#005571", Icon: SiElastic as IconComponent, category: "Analytics", keywords: ["elastic"] },
  { key: "GRAFANA", label: "Grafana", hex: "#F46800", Icon: SiGrafana as IconComponent, category: "Analytics", keywords: ["grafana"] },
  { key: "PROMETHEUS", label: "Prometheus", hex: "#E6522C", Icon: SiPrometheus as IconComponent, category: "Analytics", keywords: ["prometheus"] },
  { key: "PAGERDUTY", label: "PagerDuty", hex: "#06AC38", Icon: SiPagerduty as IconComponent, category: "Analytics", keywords: ["pagerduty"] },

  // ─── Analytics / observability / BI ────────────────────────────
  { key: "SPLUNK", label: "Splunk", hex: "#000000", darkHex: "#FFFFFF", Icon: SiSplunk as IconComponent, category: "Analytics", keywords: ["splunk"] },
  { key: "SUMOLOGIC", label: "Sumo Logic", hex: "#000099", darkHex: "#3D3DFF", Icon: SiSumologic as IconComponent, category: "Analytics", keywords: ["sumologic"] },
  { key: "DYNATRACE", label: "Dynatrace", hex: "#1496FF", Icon: SiDynatrace as IconComponent, category: "Analytics", keywords: ["dynatrace"] },
  { key: "ROLLBAR", label: "Rollbar", hex: "#3569F3", Icon: SiRollbar as IconComponent, category: "Analytics", keywords: ["rollbar"] },
  { key: "BETTERSTACK", label: "Better Stack", hex: "#000000", darkHex: "#FFFFFF", Icon: SiBetterstack as IconComponent, category: "Analytics", keywords: ["betterstack", "logtail"] },
  { key: "STATUSPAGE", label: "Statuspage", hex: "#172B4D", darkHex: "#6A91D2", Icon: SiStatuspage as IconComponent, category: "Analytics", keywords: ["statuspage"] },
  { key: "OPSGENIE", label: "Opsgenie", hex: "#172B4D", darkHex: "#6A91D2", Icon: SiOpsgenie as IconComponent, category: "Analytics", keywords: ["opsgenie"] },
  { key: "GTM", label: "Google Tag Manager", hex: "#246FDB", Icon: SiGoogletagmanager as IconComponent, category: "Analytics", keywords: ["googletagmanager", "gtm"] },
  { key: "SEARCH_CONSOLE", label: "Google Search Console", hex: "#458CF5", Icon: SiGooglesearchconsole as IconComponent, category: "Analytics", keywords: ["searchconsole"] },
  { key: "POWERBI", label: "Power BI", hex: "#F2C811", Icon: BarChart3 as unknown as IconComponent, category: "Analytics", keywords: ["powerbi"] },
  { key: "TABLEAU", label: "Tableau", hex: "#E97627", Icon: LineChart as unknown as IconComponent, category: "Analytics", keywords: ["tableau"] },
  { key: "LOOKER", label: "Looker", hex: "#4285F4", Icon: SiLooker as IconComponent, category: "Analytics", keywords: ["looker"] },
  { key: "METABASE", label: "Metabase", hex: "#509EE3", Icon: SiMetabase as IconComponent, category: "Analytics", keywords: ["metabase"] },
  { key: "SUPERSET", label: "Apache Superset", hex: "#20A6C9", Icon: SiApachesuperset as IconComponent, category: "Analytics", keywords: ["superset"] },
  { key: "REDASH", label: "Redash", hex: "#FF7964", Icon: SiRedash as IconComponent, category: "Analytics", keywords: ["redash"] },
  { key: "QLIK", label: "Qlik", hex: "#009848", Icon: SiQlik as IconComponent, category: "Analytics", keywords: ["qlik"] },
  { key: "SEMRUSH", label: "Semrush", hex: "#FF642D", Icon: SiSemrush as IconComponent, category: "Analytics", keywords: ["semrush"] },
  { key: "SIMILARWEB", label: "Similarweb", hex: "#092540", darkHex: "#559FE7", Icon: SiSimilarweb as IconComponent, category: "Analytics", keywords: ["similarweb"] },

  // ─── Search ─────────────────────────────────────────────────────
  { key: "BRAVE", label: "Brave Search", hex: "#FB542B", Icon: SiBrave as IconComponent, category: "Search", keywords: ["brave"], prefixes: ["BSA"] },
  { key: "ALGOLIA", label: "Algolia", hex: "#003DFF", Icon: SiAlgolia as IconComponent, category: "Search", keywords: ["algolia"] },
  { key: "MEILISEARCH", label: "Meilisearch", hex: "#FF5CAA", Icon: SiMeilisearch as IconComponent, category: "Search", keywords: ["meilisearch", "meili_"] },
  { key: "ELASTICSEARCH", label: "Elasticsearch", hex: "#005571", Icon: SiElasticsearch as IconComponent, category: "Search", keywords: ["elasticsearch"] },
  { key: "OPENSEARCH", label: "OpenSearch", hex: "#005EB8", Icon: SiOpensearch as IconComponent, category: "Search", keywords: ["opensearch"] },

  // ─── Search (expansion) ────────────────────────────────────────
  { key: "BING", label: "Microsoft Bing", hex: "#258FFA", Icon: Search as unknown as IconComponent, category: "Search", keywords: ["bing"] },
  { key: "DUCKDUCKGO", label: "DuckDuckGo", hex: "#DE5833", Icon: SiDuckduckgo as IconComponent, category: "Search", keywords: ["duckduckgo"] },

  // ─── CI/CD / DevOps ─────────────────────────────────────────────
  { key: "GH_ACTIONS", label: "GitHub Actions", hex: "#2088FF", Icon: SiGithubactions as IconComponent, category: "DevOps", keywords: ["githubactions", "actions"] },
  { key: "CIRCLECI", label: "CircleCI", hex: "#343434", Icon: SiCircleci as IconComponent, category: "DevOps", keywords: ["circleci"] },
  { key: "TRAVIS", label: "Travis CI", hex: "#3EAAAF", Icon: SiTravisci as IconComponent, category: "DevOps", keywords: ["travis"] },
  { key: "JENKINS", label: "Jenkins", hex: "#D24939", Icon: SiJenkins as IconComponent, category: "DevOps", keywords: ["jenkins"] },
  { key: "BUILDKITE", label: "Buildkite", hex: "#14CC80", Icon: SiBuildkite as IconComponent, category: "DevOps", keywords: ["buildkite"] },
  { key: "DOCKER", label: "Docker Hub", hex: "#2496ED", Icon: SiDocker as IconComponent, category: "DevOps", keywords: ["docker", "dockerhub"], prefixes: ["dckr_pat_"] },
  { key: "KUBERNETES", label: "Kubernetes", hex: "#326CE5", Icon: SiKubernetes as IconComponent, category: "DevOps", keywords: ["kubernetes", "k8s"] },
  { key: "TERRAFORM", label: "Terraform Cloud", hex: "#7B42BC", Icon: SiTerraform as IconComponent, category: "DevOps", keywords: ["terraform"] },
  { key: "PULUMI", label: "Pulumi", hex: "#8A3391", Icon: SiPulumi as IconComponent, category: "DevOps", keywords: ["pulumi"], prefixes: ["pul-"] },
  { key: "POSTMAN", label: "Postman", hex: "#FF6C37", Icon: SiPostman as IconComponent, category: "DevOps", keywords: ["postman"], prefixes: ["PMAK-"] },
  { key: "INSOMNIA", label: "Insomnia", hex: "#4000BF", darkHex: "#9580FF", Icon: SiInsomnia as IconComponent, category: "DevOps", keywords: ["insomnia"] },
  { key: "NGROK", label: "ngrok", hex: "#1F1E37", darkHex: "#FFFFFF", Icon: SiNgrok as IconComponent, category: "DevOps", keywords: ["ngrok"] },
  { key: "CYPRESS", label: "Cypress", hex: "#69D3A7", Icon: SiCypress as IconComponent, category: "DevOps", keywords: ["cypress"] },
  { key: "STORYBOOK", label: "Storybook", hex: "#FF4785", Icon: SiStorybook as IconComponent, category: "DevOps", keywords: ["storybook"] },
  { key: "SWAGGER", label: "Swagger / OpenAPI", hex: "#85EA2D", Icon: SiSwagger as IconComponent, category: "DevOps", keywords: ["swagger", "openapi"] },
  { key: "GRAPHQL", label: "GraphQL", hex: "#E10098", Icon: SiGraphql as IconComponent, category: "DevOps", keywords: ["graphql"] },
  { key: "PRISMA", label: "Prisma Cloud", hex: "#2D3748", Icon: SiPrisma as IconComponent, category: "DevOps", keywords: ["prisma"] },

  // ─── CI/CD / DevOps (expansion) ────────────────────────────────
  { key: "ANSIBLE", label: "Ansible", hex: "#EE0000", Icon: SiAnsible as IconComponent, category: "DevOps", keywords: ["ansible"] },
  { key: "HELM", label: "Helm", hex: "#0F1689", darkHex: "#5059EC", Icon: SiHelm as IconComponent, category: "DevOps", keywords: ["helm"] },
  { key: "ARGOCD", label: "Argo CD", hex: "#EF7B4D", Icon: SiArgo as IconComponent, category: "DevOps", keywords: ["argocd", "argo"] },
  { key: "RANCHER", label: "Rancher", hex: "#0075A8", Icon: SiRancher as IconComponent, category: "DevOps", keywords: ["rancher"] },
  { key: "OPENSHIFT", label: "OpenShift", hex: "#EE0000", Icon: SiRedhatopenshift as IconComponent, category: "DevOps", keywords: ["openshift"] },
  { key: "NOMAD", label: "Nomad", hex: "#00CA8E", Icon: SiNomad as IconComponent, category: "DevOps", keywords: ["nomad"] },
  { key: "CONSUL", label: "Consul", hex: "#F24C53", Icon: SiConsul as IconComponent, category: "DevOps", keywords: ["consul"] },
  { key: "NGINX", label: "NGINX", hex: "#009639", Icon: SiNginx as IconComponent, category: "DevOps", keywords: ["nginx"] },
  { key: "TEAMCITY", label: "TeamCity", hex: "#000000", darkHex: "#FFFFFF", Icon: SiTeamcity as IconComponent, category: "DevOps", keywords: ["teamcity"] },
  { key: "BAMBOO", label: "Bamboo", hex: "#0052CC", Icon: SiBamboo as IconComponent, category: "DevOps", keywords: ["bamboo"] },
  { key: "DRONE", label: "Drone CI", hex: "#212121", darkHex: "#FFFFFF", Icon: SiDrone as IconComponent, category: "DevOps", keywords: ["droneci"] },
  { key: "SEMAPHORE", label: "Semaphore CI", hex: "#19A974", Icon: SiSemaphoreci as IconComponent, category: "DevOps", keywords: ["semaphoreci"] },
  { key: "BITRISE", label: "Bitrise", hex: "#683D87", Icon: SiBitrise as IconComponent, category: "DevOps", keywords: ["bitrise"] },
  { key: "FASTLANE", label: "fastlane", hex: "#00F200", Icon: SiFastlane as IconComponent, category: "DevOps", keywords: ["fastlane"] },
  { key: "EXPO", label: "Expo", hex: "#1C2024", darkHex: "#FFFFFF", Icon: SiExpo as IconComponent, category: "DevOps", keywords: ["expo.dev", "expo_"] },
  { key: "CODECOV", label: "Codecov", hex: "#F01F7A", Icon: SiCodecov as IconComponent, category: "DevOps", keywords: ["codecov"] },
  { key: "COVERALLS", label: "Coveralls", hex: "#3F5767", Icon: SiCoveralls as IconComponent, category: "DevOps", keywords: ["coveralls"] },
  { key: "BROWSERSTACK", label: "BrowserStack", hex: "#E66F32", Icon: MonitorSmartphone as unknown as IconComponent, category: "DevOps", keywords: ["browserstack"] },
  { key: "SAUCELABS", label: "Sauce Labs", hex: "#3DDC91", Icon: SiSaucelabs as IconComponent, category: "DevOps", keywords: ["saucelabs"] },
  { key: "PERCY", label: "Percy", hex: "#9E66BF", Icon: SiPercy as IconComponent, category: "DevOps", keywords: ["percy"] },
  { key: "CHROMATIC", label: "Chromatic", hex: "#FC521F", Icon: SiChromatic as IconComponent, category: "DevOps", keywords: ["chromatic"] },
  { key: "NPM", label: "npm", hex: "#CB3837", Icon: SiNpm as IconComponent, category: "DevOps", keywords: ["npmjs", "npm_"], prefixes: ["npm_"] },
  { key: "PYPI", label: "PyPI", hex: "#3775A9", Icon: SiPypi as IconComponent, category: "DevOps", keywords: ["pypi"], prefixes: ["pypi-"] },
  { key: "RUBYGEMS", label: "RubyGems", hex: "#E9573F", Icon: SiRubygems as IconComponent, category: "DevOps", keywords: ["rubygems"] },
  { key: "NUGET", label: "NuGet", hex: "#004880", darkHex: "#3DAAFF", Icon: SiNuget as IconComponent, category: "DevOps", keywords: ["nuget"] },
  { key: "PACKAGIST", label: "Packagist", hex: "#F28D1A", Icon: SiPackagist as IconComponent, category: "DevOps", keywords: ["packagist"] },

  // ─── File storage / CDN ─────────────────────────────────────────
  { key: "BACKBLAZE", label: "Backblaze", hex: "#E72C2A", Icon: SiBackblaze as IconComponent, category: "Storage", keywords: ["backblaze", "b2_"] },
  { key: "DROPBOX", label: "Dropbox", hex: "#0061FF", Icon: SiDropbox as IconComponent, category: "Storage", keywords: ["dropbox"] },
  // Above GDRIVE, whose "drive" keyword is a substring of "onedrive".
  { key: "ONEDRIVE", label: "OneDrive", hex: "#0078D4", Icon: Cloud as unknown as IconComponent, category: "Storage", keywords: ["onedrive"] },
  { key: "GDRIVE", label: "Google Drive", hex: "#4285F4", Icon: SiGoogledrive as IconComponent, category: "Storage", keywords: ["googledrive", "drive"] },
  { key: "CLOUDINARY", label: "Cloudinary", hex: "#3448C5", Icon: SiCloudinary as IconComponent, category: "Storage", keywords: ["cloudinary"] },

  // ─── File storage / CDN (expansion) ────────────────────────────
  { key: "BOX", label: "Box", hex: "#0061D5", Icon: SiBox as IconComponent, category: "Storage", keywords: ["box.com", "boxapi"] },
  { key: "ICLOUD", label: "iCloud", hex: "#3693F3", Icon: SiIcloud as IconComponent, category: "Storage", keywords: ["icloud"] },
  { key: "GOOGLE_PHOTOS", label: "Google Photos", hex: "#4285F4", Icon: SiGooglephotos as IconComponent, category: "Storage", keywords: ["googlephotos"] },
  { key: "MEGA", label: "MEGA", hex: "#D9272E", Icon: SiMega as IconComponent, category: "Storage", keywords: ["mega.nz"] },
  { key: "NEXTCLOUD", label: "Nextcloud", hex: "#0082C9", Icon: SiNextcloud as IconComponent, category: "Storage", keywords: ["nextcloud"] },
  { key: "OWNCLOUD", label: "ownCloud", hex: "#041E42", darkHex: "#4890F4", Icon: SiOwncloud as IconComponent, category: "Storage", keywords: ["owncloud"] },
  { key: "SYNCTHING", label: "Syncthing", hex: "#0891D1", Icon: SiSyncthing as IconComponent, category: "Storage", keywords: ["syncthing"] },
  { key: "FILEZILLA", label: "FileZilla", hex: "#BF0000", Icon: SiFilezilla as IconComponent, category: "Storage", keywords: ["filezilla"] },
  { key: "WASABI", label: "Wasabi", hex: "#01CD3E", Icon: SiWasabi as IconComponent, category: "Storage", keywords: ["wasabi"] },
  { key: "MINIO", label: "MinIO", hex: "#C72E49", Icon: SiMinio as IconComponent, category: "Storage", keywords: ["minio"] },
  { key: "FASTLY", label: "Fastly", hex: "#FF282D", Icon: SiFastly as IconComponent, category: "Storage", keywords: ["fastly"] },
  { key: "BUNNY", label: "Bunny.net", hex: "#FFAA49", Icon: SiBunnydotnet as IconComponent, category: "Storage", keywords: ["bunny.net", "bunnycdn"] },
  { key: "KEYCDN", label: "KeyCDN", hex: "#047AED", Icon: SiKeycdn as IconComponent, category: "Storage", keywords: ["keycdn"] },
  { key: "JSDELIVR", label: "jsDelivr", hex: "#E84D3D", Icon: SiJsdelivr as IconComponent, category: "Storage", keywords: ["jsdelivr"] },

  // ─── Database ───────────────────────────────────────────────────
  { key: "POSTGRES", label: "PostgreSQL", hex: "#4169E1", Icon: SiPostgresql as IconComponent, category: "Database", keywords: ["postgres", "postgresql"] },
  { key: "MYSQL", label: "MySQL", hex: "#4479A1", Icon: SiMysql as IconComponent, category: "Database", keywords: ["mysql"] },
  { key: "MONGODB", label: "MongoDB", hex: "#47A248", Icon: SiMongodb as IconComponent, category: "Database", keywords: ["mongodb", "mongo"] },
  { key: "REDIS", label: "Redis", hex: "#FF4438", Icon: SiRedis as IconComponent, category: "Database", keywords: ["redis"] },
  { key: "SQLITE", label: "SQLite", hex: "#003B57", darkHex: "#0EA5E9", Icon: SiSqlite as IconComponent, category: "Database", keywords: ["sqlite"] },
  { key: "COUCHBASE", label: "Couchbase", hex: "#EA2328", Icon: SiCouchbase as IconComponent, category: "Database", keywords: ["couchbase"] },
  { key: "COCKROACH", label: "CockroachDB", hex: "#6933FF", Icon: SiCockroachlabs as IconComponent, category: "Database", keywords: ["cockroach"] },
  { key: "MARIADB", label: "MariaDB", hex: "#003545", darkHex: "#00A0BC", Icon: SiMariadb as IconComponent, category: "Database", keywords: ["mariadb"] },
  { key: "CASSANDRA", label: "Cassandra", hex: "#1287B1", Icon: SiApachecassandra as IconComponent, category: "Database", keywords: ["cassandra"] },
  { key: "SNOWFLAKE", label: "Snowflake", hex: "#29B5E8", Icon: SiSnowflake as IconComponent, category: "Database", keywords: ["snowflake"] },
  { key: "DATABRICKS", label: "Databricks", hex: "#FF3621", Icon: SiDatabricks as IconComponent, category: "Database", keywords: ["databricks"] },
  { key: "BIGQUERY", label: "BigQuery", hex: "#669DF6", Icon: SiGooglebigquery as IconComponent, category: "Database", keywords: ["bigquery"] },

  // ─── Database / streaming (expansion) ──────────────────────────
  { key: "MSSQL", label: "Microsoft SQL Server", hex: "#CC2927", Icon: Database as unknown as IconComponent, category: "Database", keywords: ["mssql", "sqlserver"] },
  { key: "DYNAMODB", label: "Amazon DynamoDB", hex: "#4053D6", darkHex: "#8A97FF", Icon: Database as unknown as IconComponent, category: "Database", keywords: ["dynamodb"] },
  { key: "NEON", label: "Neon", hex: "#34D59A", Icon: SiNeon as IconComponent, category: "Database", keywords: ["neon"] },
  { key: "TURSO", label: "Turso", hex: "#4FF8D2", Icon: SiTurso as IconComponent, category: "Database", keywords: ["turso"] },
  { key: "UPSTASH", label: "Upstash", hex: "#00E9A3", Icon: SiUpstash as IconComponent, category: "Database", keywords: ["upstash"] },
  { key: "INFLUXDB", label: "InfluxDB", hex: "#22ADF6", Icon: SiInfluxdb as IconComponent, category: "Database", keywords: ["influxdb"] },
  { key: "TIMESCALE", label: "Timescale", hex: "#FDB515", Icon: SiTimescale as IconComponent, category: "Database", keywords: ["timescale"] },
  { key: "CLICKHOUSE", label: "ClickHouse", hex: "#FFCC01", Icon: SiClickhouse as IconComponent, category: "Database", keywords: ["clickhouse"] },
  { key: "NEO4J", label: "Neo4j", hex: "#4581C3", Icon: SiNeo4J as IconComponent, category: "Database", keywords: ["neo4j"] },
  { key: "DUCKDB", label: "DuckDB", hex: "#FFF000", Icon: SiDuckdb as IconComponent, category: "Database", keywords: ["duckdb"] },
  { key: "ARANGODB", label: "ArangoDB", hex: "#DDDF72", Icon: SiArangodb as IconComponent, category: "Database", keywords: ["arangodb"] },
  { key: "SURREALDB", label: "SurrealDB", hex: "#FF00A0", Icon: SiSurrealdb as IconComponent, category: "Database", keywords: ["surrealdb"] },
  { key: "FAUNA", label: "Fauna", hex: "#3A1AB6", darkHex: "#7355E7", Icon: SiFauna as IconComponent, category: "Database", keywords: ["fauna"] },
  { key: "QDRANT", label: "Qdrant", hex: "#DC244C", Icon: SiQdrant as IconComponent, category: "Database", keywords: ["qdrant"] },
  { key: "KAFKA", label: "Apache Kafka", hex: "#231F20", darkHex: "#FFFFFF", Icon: SiApachekafka as IconComponent, category: "Database", keywords: ["kafka"] },
  { key: "RABBITMQ", label: "RabbitMQ", hex: "#FF6600", Icon: SiRabbitmq as IconComponent, category: "Database", keywords: ["rabbitmq"] },
  { key: "NATS", label: "NATS", hex: "#27AAE1", Icon: SiNatsdotio as IconComponent, category: "Database", keywords: ["nats.io"] },
  { key: "SPARK", label: "Apache Spark", hex: "#E25A1C", Icon: SiApachespark as IconComponent, category: "Database", keywords: ["apachespark"] },
  { key: "AIRFLOW", label: "Apache Airflow", hex: "#017CEE", Icon: SiApacheairflow as IconComponent, category: "Database", keywords: ["airflow"] },
  { key: "AIRBYTE", label: "Airbyte", hex: "#615EFF", Icon: SiAirbyte as IconComponent, category: "Database", keywords: ["airbyte"] },

  // ─── Email / marketing ──────────────────────────────────────────
  { key: "SUBSTACK", label: "Substack", hex: "#FF6719", Icon: SiSubstack as IconComponent, category: "Email", keywords: ["substack"] },

  // ─── Email (expansion) ─────────────────────────────────────────
  { key: "GMAIL", label: "Gmail", hex: "#EA4335", Icon: SiGmail as IconComponent, category: "Email", keywords: ["gmail"] },
  { key: "OUTLOOK", label: "Outlook", hex: "#0078D4", Icon: Mail as unknown as IconComponent, category: "Email", keywords: ["outlook"] },
  { key: "PROTONMAIL", label: "Proton Mail", hex: "#6D4AFF", Icon: SiProtonmail as IconComponent, category: "Email", keywords: ["protonmail"] },
  { key: "BREVO", label: "Brevo", hex: "#0B996E", Icon: SiBrevo as IconComponent, category: "Email", keywords: ["brevo", "sendinblue"] },
  { key: "POSTMARK", label: "Postmark", hex: "#FFDE00", Icon: Send as unknown as IconComponent, category: "Email", keywords: ["postmark"] },
  { key: "KLAVIYO", label: "Klaviyo", hex: "#000000", darkHex: "#FFFFFF", Icon: Megaphone as unknown as IconComponent, category: "Email", keywords: ["klaviyo"] },
  { key: "SPARKPOST", label: "SparkPost", hex: "#FA6423", Icon: SiSparkpost as IconComponent, category: "Email", keywords: ["sparkpost"] },
  { key: "LOOPS", label: "Loops", hex: "#FC5200", Icon: SiLoops as IconComponent, category: "Email", keywords: ["loops.so"] },

  // ─── Social / APIs ──────────────────────────────────────────────
  { key: "X", label: "X (Twitter)", hex: "#000000", darkHex: "#FFFFFF", Icon: SiX as IconComponent, category: "Social", keywords: ["twitter", "x.com"] },
  { key: "REDDIT", label: "Reddit", hex: "#FF4500", Icon: SiReddit as IconComponent, category: "Social", keywords: ["reddit"] },
  { key: "YOUTUBE", label: "YouTube", hex: "#FF0000", Icon: SiYoutube as IconComponent, category: "Social", keywords: ["youtube"] },
  { key: "TWITCH", label: "Twitch", hex: "#9146FF", Icon: SiTwitch as IconComponent, category: "Social", keywords: ["twitch"] },
  { key: "FACEBOOK", label: "Facebook / Meta", hex: "#0866FF", Icon: SiFacebook as IconComponent, category: "Social", keywords: ["facebook", "meta", "fb_"] },
  { key: "INSTAGRAM", label: "Instagram", hex: "#E4405F", Icon: SiInstagram as IconComponent, category: "Social", keywords: ["instagram"] },
  { key: "TIKTOK", label: "TikTok", hex: "#000000", darkHex: "#FFFFFF", Icon: SiTiktok as IconComponent, category: "Social", keywords: ["tiktok"] },
  { key: "SPOTIFY", label: "Spotify", hex: "#1ED760", Icon: SiSpotify as IconComponent, category: "Social", keywords: ["spotify"] },
  { key: "PINTEREST", label: "Pinterest", hex: "#BD081C", Icon: SiPinterest as IconComponent, category: "Social", keywords: ["pinterest"] },

  // ─── Social (expansion) ────────────────────────────────────────
  { key: "LINKEDIN", label: "LinkedIn", hex: "#0A66C2", Icon: Briefcase as unknown as IconComponent, category: "Social", keywords: ["linkedin"] },
  { key: "SNAPCHAT", label: "Snapchat", hex: "#FFFC00", Icon: SiSnapchat as IconComponent, category: "Social", keywords: ["snapchat"] },
  { key: "THREADS", label: "Threads", hex: "#000000", darkHex: "#FFFFFF", Icon: SiThreads as IconComponent, category: "Social", keywords: ["threads"] },
  { key: "MASTODON", label: "Mastodon", hex: "#6364FF", Icon: SiMastodon as IconComponent, category: "Social", keywords: ["mastodon"] },
  { key: "BLUESKY", label: "Bluesky", hex: "#1185FE", Icon: SiBluesky as IconComponent, category: "Social", keywords: ["bluesky"] },
  { key: "TUMBLR", label: "Tumblr", hex: "#36465D", darkHex: "#8499B8", Icon: SiTumblr as IconComponent, category: "Social", keywords: ["tumblr"] },
  { key: "MEDIUM", label: "Medium", hex: "#000000", darkHex: "#FFFFFF", Icon: SiMedium as IconComponent, category: "Social", keywords: ["medium"] },
  { key: "QUORA", label: "Quora", hex: "#B92B27", Icon: SiQuora as IconComponent, category: "Social", keywords: ["quora"] },
  { key: "STACKOVERFLOW", label: "Stack Overflow", hex: "#F58025", Icon: SiStackoverflow as IconComponent, category: "Social", keywords: ["stackoverflow"] },
  { key: "VIMEO", label: "Vimeo", hex: "#1AB7EA", Icon: SiVimeo as IconComponent, category: "Social", keywords: ["vimeo"] },
  { key: "DAILYMOTION", label: "Dailymotion", hex: "#0A0A0A", darkHex: "#FFFFFF", Icon: SiDailymotion as IconComponent, category: "Social", keywords: ["dailymotion"] },
  { key: "FLICKR", label: "Flickr", hex: "#0063DC", Icon: SiFlickr as IconComponent, category: "Social", keywords: ["flickr"] },
  { key: "BEHANCE", label: "Behance", hex: "#1769FF", Icon: SiBehance as IconComponent, category: "Social", keywords: ["behance"] },
  { key: "DRIBBBLE", label: "Dribbble", hex: "#EA4C89", Icon: SiDribbble as IconComponent, category: "Social", keywords: ["dribbble"] },
  { key: "UNSPLASH", label: "Unsplash", hex: "#000000", darkHex: "#FFFFFF", Icon: SiUnsplash as IconComponent, category: "Social", keywords: ["unsplash"] },
  { key: "PEXELS", label: "Pexels", hex: "#05A081", Icon: SiPexels as IconComponent, category: "Social", keywords: ["pexels"] },
  { key: "GIPHY", label: "Giphy", hex: "#FF6666", Icon: SiGiphy as IconComponent, category: "Social", keywords: ["giphy"] },
  { key: "VK", label: "VK", hex: "#0077FF", Icon: SiVk as IconComponent, category: "Social", keywords: ["vk.com"] },
  { key: "WEIBO", label: "Weibo", hex: "#E6162D", Icon: SiSinaweibo as IconComponent, category: "Social", keywords: ["weibo"] },
  { key: "BILIBILI", label: "Bilibili", hex: "#00A1D6", Icon: SiBilibili as IconComponent, category: "Social", keywords: ["bilibili"] },
  { key: "NAVER", label: "Naver", hex: "#03C75A", Icon: SiNaver as IconComponent, category: "Social", keywords: ["naver"] },
  { key: "BAIDU", label: "Baidu", hex: "#2932E1", Icon: SiBaidu as IconComponent, category: "Social", keywords: ["baidu"] },

  // ─── E-commerce ─────────────────────────────────────────────────
  { key: "BIGCOMMERCE", label: "BigCommerce", hex: "#121118", darkHex: "#FFFFFF", Icon: SiBigcommerce as IconComponent, category: "Other", keywords: ["bigcommerce"] },
  { key: "ETSY", label: "Etsy", hex: "#F1641E", Icon: SiEtsy as IconComponent, category: "Other", keywords: ["etsy"] },

  // ─── Consumer / everyday apps ──────────────────────────────────
  { key: "EBAY", label: "eBay", hex: "#E53238", Icon: SiEbay as IconComponent, category: "Other", keywords: ["ebay"] },
  { key: "ALIEXPRESS", label: "AliExpress", hex: "#FF4747", Icon: SiAliexpress as IconComponent, category: "Other", keywords: ["aliexpress"] },
  { key: "WALMART", label: "Walmart", hex: "#0071CE", Icon: ShoppingCart as unknown as IconComponent, category: "Other", keywords: ["walmart"] },
  { key: "IKEA", label: "IKEA", hex: "#0058A3", Icon: SiIkea as IconComponent, category: "Other", keywords: ["ikea"] },
  { key: "UBER", label: "Uber", hex: "#000000", darkHex: "#FFFFFF", Icon: SiUber as IconComponent, category: "Other", keywords: ["uber"] },
  { key: "LYFT", label: "Lyft", hex: "#FF00BF", Icon: SiLyft as IconComponent, category: "Other", keywords: ["lyft"] },
  { key: "AIRBNB", label: "Airbnb", hex: "#FF5A5F", Icon: SiAirbnb as IconComponent, category: "Other", keywords: ["airbnb"] },
  { key: "BOOKING", label: "Booking.com", hex: "#003A9A", darkHex: "#3D86FF", Icon: SiBookingdotcom as IconComponent, category: "Other", keywords: ["booking.com"] },
  { key: "EXPEDIA", label: "Expedia", hex: "#191E3B", darkHex: "#7782C5", Icon: SiExpedia as IconComponent, category: "Other", keywords: ["expedia"] },
  { key: "DOORDASH", label: "DoorDash", hex: "#FF3008", Icon: SiDoordash as IconComponent, category: "Other", keywords: ["doordash"] },
  { key: "INSTACART", label: "Instacart", hex: "#43B02A", Icon: SiInstacart as IconComponent, category: "Other", keywords: ["instacart"] },
  { key: "DELIVEROO", label: "Deliveroo", hex: "#00CCBC", Icon: SiDeliveroo as IconComponent, category: "Other", keywords: ["deliveroo"] },
  { key: "JUSTEAT", label: "Just Eat", hex: "#FF8000", Icon: SiJusteat as IconComponent, category: "Other", keywords: ["justeat"] },
  { key: "GRAB", label: "Grab", hex: "#00B14F", Icon: SiGrab as IconComponent, category: "Other", keywords: ["grab.com"] },
  { key: "YELP", label: "Yelp", hex: "#FF1A1A", Icon: SiYelp as IconComponent, category: "Other", keywords: ["yelp"] },
  { key: "TRIPADVISOR", label: "Tripadvisor", hex: "#34E0A1", Icon: SiTripadvisor as IconComponent, category: "Other", keywords: ["tripadvisor"] },
  { key: "NETFLIX", label: "Netflix", hex: "#E50914", Icon: SiNetflix as IconComponent, category: "Other", keywords: ["netflix"] },
  { key: "HBOMAX", label: "HBO Max", hex: "#000000", darkHex: "#FFFFFF", Icon: SiHbomax as IconComponent, category: "Other", keywords: ["hbomax"] },
  { key: "SOUNDCLOUD", label: "SoundCloud", hex: "#FF5500", Icon: SiSoundcloud as IconComponent, category: "Other", keywords: ["soundcloud"] },
  { key: "DEEZER", label: "Deezer", hex: "#A238FF", Icon: SiDeezer as IconComponent, category: "Other", keywords: ["deezer"] },
  { key: "TIDAL", label: "Tidal", hex: "#000000", darkHex: "#FFFFFF", Icon: SiTidal as IconComponent, category: "Other", keywords: ["tidal"] },
  { key: "AUDIBLE", label: "Audible", hex: "#F8991C", Icon: SiAudible as IconComponent, category: "Other", keywords: ["audible"] },
  { key: "STEAM", label: "Steam", hex: "#000000", darkHex: "#FFFFFF", Icon: SiSteam as IconComponent, category: "Other", keywords: ["steam"] },
  { key: "EPIC_GAMES", label: "Epic Games", hex: "#313131", darkHex: "#FFFFFF", Icon: SiEpicgames as IconComponent, category: "Other", keywords: ["epicgames"] },
  { key: "PLAYSTATION", label: "PlayStation", hex: "#0070D1", Icon: SiPlaystation as IconComponent, category: "Other", keywords: ["playstation", "psn"] },
  { key: "XBOX", label: "Xbox", hex: "#107C10", darkHex: "#4CD44C", Icon: Gamepad2 as unknown as IconComponent, category: "Other", keywords: ["xbox"] },
  { key: "NINTENDO", label: "Nintendo", hex: "#E60012", Icon: Gamepad2 as unknown as IconComponent, category: "Other", keywords: ["nintendo"] },
  { key: "ROBLOX", label: "Roblox", hex: "#000000", darkHex: "#FFFFFF", Icon: SiRoblox as IconComponent, category: "Other", keywords: ["roblox"] },
  { key: "DUOLINGO", label: "Duolingo", hex: "#58CC02", Icon: SiDuolingo as IconComponent, category: "Other", keywords: ["duolingo"] },
  { key: "COURSERA", label: "Coursera", hex: "#0056D2", Icon: SiCoursera as IconComponent, category: "Other", keywords: ["coursera"] },
  { key: "UDEMY", label: "Udemy", hex: "#A435F0", Icon: SiUdemy as IconComponent, category: "Other", keywords: ["udemy"] },
  { key: "KHANACADEMY", label: "Khan Academy", hex: "#14BF96", Icon: SiKhanacademy as IconComponent, category: "Other", keywords: ["khanacademy"] },
  { key: "EDX", label: "edX", hex: "#02262B", darkHex: "#46E1F6", Icon: SiEdx as IconComponent, category: "Other", keywords: ["edx"] },
  { key: "INDEED", label: "Indeed", hex: "#003A9B", darkHex: "#3D86FF", Icon: SiIndeed as IconComponent, category: "Other", keywords: ["indeed"] },
  { key: "GLASSDOOR", label: "Glassdoor", hex: "#00A162", Icon: SiGlassdoor as IconComponent, category: "Other", keywords: ["glassdoor"] },
  { key: "UPWORK", label: "Upwork", hex: "#6FDA44", Icon: SiUpwork as IconComponent, category: "Other", keywords: ["upwork"] },
  { key: "FIVERR", label: "Fiverr", hex: "#1DBF73", Icon: SiFiverr as IconComponent, category: "Other", keywords: ["fiverr"] },
  { key: "APPLE_MUSIC", label: "Apple Music", hex: "#FA243C", Icon: SiApplemusic as IconComponent, category: "Other", keywords: ["applemusic", "apple_music", "apple music"] },
  { key: "APPLE_TV", label: "Apple TV", hex: "#000000", darkHex: "#FFFFFF", Icon: SiAppletv as IconComponent, category: "Other", keywords: ["appletv", "apple_tv", "apple tv"] },
  { key: "APP_STORE", label: "App Store", hex: "#0D96F6", Icon: SiAppstore as IconComponent, category: "Other", keywords: ["appstore", "app_store", "app store"] },
  { key: "APPLE", label: "Apple", hex: "#000000", darkHex: "#FFFFFF", Icon: SiApple as IconComponent, category: "Other", keywords: ["apple"] },
  { key: "GOOGLE_PLAY", label: "Google Play", hex: "#414141", darkHex: "#FFFFFF", Icon: SiGoogleplay as IconComponent, category: "Other", keywords: ["googleplay"] },
  { key: "CHROME", label: "Google Chrome", hex: "#4285F4", Icon: SiGooglechrome as IconComponent, category: "Other", keywords: ["chrome"] },
  { key: "WINDOWS", label: "Windows", hex: "#0078D4", Icon: MonitorSmartphone as unknown as IconComponent, category: "Other", keywords: ["windows"] },
  { key: "MICROSOFT", label: "Microsoft", hex: "#5E5E5E", darkHex: "#C7C7C7", Icon: Building2 as unknown as IconComponent, category: "Other", keywords: ["microsoft"] },
  { key: "AMAZON", label: "Amazon", hex: "#FF9900", Icon: ShoppingCart as unknown as IconComponent, category: "Other", keywords: ["amazon"] },

  // ─── Maps / geo ─────────────────────────────────────────────────
  { key: "GMAPS", label: "Google Maps", hex: "#4285F4", Icon: SiGooglemaps as IconComponent, category: "Maps", keywords: ["googlemaps", "maps"] },
  { key: "MAPBOX", label: "Mapbox", hex: "#000000", darkHex: "#FFFFFF", Icon: SiMapbox as IconComponent, category: "Maps", keywords: ["mapbox"], prefixes: ["pk.eyJ", "sk.eyJ"] },
  { key: "OSM", label: "OpenStreetMap", hex: "#7EBC6F", Icon: SiOpenstreetmap as IconComponent, category: "Maps", keywords: ["openstreetmap", "osm"] },

  // ─── ML / data ──────────────────────────────────────────────────
  { key: "TENSORFLOW", label: "TensorFlow", hex: "#FF6F00", Icon: SiTensorflow as IconComponent, category: "ML", keywords: ["tensorflow"] },
  { key: "PYTORCH", label: "PyTorch", hex: "#EE4C2C", Icon: SiPytorch as IconComponent, category: "ML", keywords: ["pytorch"] },
  { key: "PANDAS", label: "Pandas", hex: "#150458", darkHex: "#E70488", Icon: SiPandas as IconComponent, category: "ML", keywords: ["pandas"] },
  { key: "NUMPY", label: "NumPy", hex: "#013243", darkHex: "#4DABCF", Icon: SiNumpy as IconComponent, category: "ML", keywords: ["numpy"] },
  { key: "SKLEARN", label: "scikit-learn", hex: "#F7931E", Icon: SiScikitlearn as IconComponent, category: "ML", keywords: ["scikit", "sklearn"] },
  { key: "KERAS", label: "Keras", hex: "#D00000", Icon: SiKeras as IconComponent, category: "ML", keywords: ["keras"] },
  { key: "MLFLOW", label: "MLflow", hex: "#0194E2", Icon: SiMlflow as IconComponent, category: "ML", keywords: ["mlflow"] },
  { key: "NVIDIA", label: "NVIDIA", hex: "#76B900", Icon: SiNvidia as IconComponent, category: "ML", keywords: ["nvidia"], prefixes: ["nvapi-"] },
  { key: "INTEL", label: "Intel", hex: "#0071C5", Icon: SiIntel as IconComponent, category: "ML", keywords: ["intel"] },

  // ─── ML / data (expansion) ─────────────────────────────────────
  { key: "WANDB", label: "Weights & Biases", hex: "#FFBE00", Icon: SiWeightsandbiases as IconComponent, category: "ML", keywords: ["wandb", "weightsandbiases"] },
  { key: "KAGGLE", label: "Kaggle", hex: "#20BEFF", Icon: SiKaggle as IconComponent, category: "ML", keywords: ["kaggle"] },
  { key: "COLAB", label: "Google Colab", hex: "#F9AB00", Icon: SiGooglecolab as IconComponent, category: "ML", keywords: ["colab"] },
  { key: "ROBOFLOW", label: "Roboflow", hex: "#6706CE", Icon: SiRoboflow as IconComponent, category: "ML", keywords: ["roboflow"] },

  // ─── CRM / marketing ────────────────────────────────────────────
  { key: "ZOHO", label: "Zoho", hex: "#C8202F", Icon: SiZoho as IconComponent, category: "Marketing", keywords: ["zoho"] },
  { key: "SALESFORCE", label: "Salesforce", hex: "#00A1E0", Icon: SiSalesforce as IconComponent, category: "Marketing", keywords: ["salesforce"] },
  { key: "HUBSPOT", label: "HubSpot", hex: "#FF7A59", Icon: SiHubspot as IconComponent, category: "Marketing", keywords: ["hubspot"] },
  { key: "ZAPIER", label: "Zapier", hex: "#FF4F00", Icon: SiZapier as IconComponent, category: "Marketing", keywords: ["zapier"] },
  { key: "MAKE", label: "Make", hex: "#6D00CC", darkHex: "#A66DFF", Icon: SiMake as IconComponent, category: "Marketing", keywords: ["make.com", "integromat"] },

  // ─── CRM / support / marketing (expansion) ─────────────────────
  { key: "ZENDESK", label: "Zendesk", hex: "#03363D", darkHex: "#46E1F6", Icon: SiZendesk as IconComponent, category: "Marketing", keywords: ["zendesk"] },
  { key: "INTERCOM", label: "Intercom", hex: "#6AFDEF", Icon: SiIntercom as IconComponent, category: "Marketing", keywords: ["intercom"] },
  { key: "HELPSCOUT", label: "Help Scout", hex: "#1292EE", Icon: SiHelpscout as IconComponent, category: "Marketing", keywords: ["helpscout"] },
  { key: "LIVECHAT", label: "LiveChat", hex: "#FF5100", Icon: SiLivechat as IconComponent, category: "Marketing", keywords: ["livechat"] },
  { key: "FRESHDESK", label: "Freshdesk", hex: "#25C16F", Icon: LifeBuoy as unknown as IconComponent, category: "Marketing", keywords: ["freshdesk", "freshworks"] },
  { key: "N8N", label: "n8n", hex: "#EA4B71", Icon: SiN8N as IconComponent, category: "Marketing", keywords: ["n8n"] },
  { key: "GOOGLE_ADS", label: "Google Ads", hex: "#4285F4", Icon: SiGoogleads as IconComponent, category: "Marketing", keywords: ["googleads"] },
  { key: "BUFFER", label: "Buffer", hex: "#231F20", darkHex: "#FFFFFF", Icon: SiBuffer as IconComponent, category: "Marketing", keywords: ["buffer"] },
  { key: "HOOTSUITE", label: "Hootsuite", hex: "#FF4C46", Icon: SiHootsuite as IconComponent, category: "Marketing", keywords: ["hootsuite"] },
  { key: "GREENHOUSE", label: "Greenhouse", hex: "#24A47F", Icon: SiGreenhouse as IconComponent, category: "Marketing", keywords: ["greenhouse"] },
  { key: "SAP", label: "SAP", hex: "#0FAAFF", Icon: SiSap as IconComponent, category: "Marketing", keywords: ["sap"] },

  // ─── CMS / web ──────────────────────────────────────────────────
  { key: "WEBFLOW", label: "Webflow", hex: "#146EF5", Icon: SiWebflow as IconComponent, category: "Other", keywords: ["webflow"] },
  { key: "WORDPRESS", label: "WordPress", hex: "#21759B", Icon: SiWordpress as IconComponent, category: "Other", keywords: ["wordpress"] },
  { key: "GHOST", label: "Ghost", hex: "#15171A", darkHex: "#FFFFFF", Icon: SiGhost as IconComponent, category: "Other", keywords: ["ghost"] },
  { key: "CONTENTFUL", label: "Contentful", hex: "#2478CC", Icon: SiContentful as IconComponent, category: "Other", keywords: ["contentful"] },
  { key: "SANITY", label: "Sanity", hex: "#F03E2F", Icon: SiSanity as IconComponent, category: "Other", keywords: ["sanity"] },
  { key: "STRAPI", label: "Strapi", hex: "#4945FF", Icon: SiStrapi as IconComponent, category: "Other", keywords: ["strapi"] },
  { key: "STORYBLOK", label: "Storyblok", hex: "#09B3AF", Icon: SiStoryblok as IconComponent, category: "Other", keywords: ["storyblok"] },

  // ─── Crewship internals (CLI tooling) ───────────────────────────
  { key: "CUSTOM_CLI", label: "Custom CLI", hex: "#9CA3AF", Icon: CustomCLIIcon as IconComponent, category: "Other", keywords: ["custom_cli"] },

  // ─── Crewship vault types ───────────────────────────────────────
  // Generic, brand-less entries for credentials whose provider is the
  // vault itself (no upstream service). Icons come from lucide so they
  // visually distinguish themselves from the Simple Icons brand marks
  // — the user reads them as "generic secret, not a third-party brand".
  // Hex sits in the muted-grey range used by Lucide's stroke colour
  // palette so the icon doesn't shout for attention in the list row.
  { key: "VAULT_USERPASS", label: "Username + Password", hex: "#9CA3AF", Icon: User as unknown as IconComponent, category: "Auth", keywords: ["userpass", "login"] },
  { key: "VAULT_SSH_KEY", label: "SSH Key", hex: "#9CA3AF", Icon: KeyRound as unknown as IconComponent, category: "Auth", keywords: ["ssh"] },
  { key: "VAULT_CERTIFICATE", label: "TLS Certificate", hex: "#9CA3AF", Icon: ShieldCheck as unknown as IconComponent, category: "Auth", keywords: ["certificate", "tls", "mtls", "pem"] },
  { key: "VAULT_GENERIC", label: "Generic Secret", hex: "#9CA3AF", Icon: Lock as unknown as IconComponent, category: "Auth", keywords: ["secret", "webhook"] },
]

// ── Lookup helpers ─────────────────────────────────────────────────

const KEY_INDEX = new Map<string, BrandEntry>(
  BRAND_REGISTRY.map((b) => [b.key, b]),
)

// Generic catch-all when nothing matches: a Lucide key icon with no
// brand colour.
export const GENERIC_BRAND: BrandEntry = {
  key: "NONE",
  label: "Generic secret",
  hex: "#9CA3AF",
  Icon: Key as unknown as IconComponent,
  category: "Other",
}

export function getBrand(key: string | null | undefined): BrandEntry {
  if (!key) return GENERIC_BRAND
  return KEY_INDEX.get(key) ?? GENERIC_BRAND
}

// detectBrandFromName matches name substrings (case-insensitive) against
// every entry's keywords list. First hit wins; ordering of BRAND_REGISTRY
// puts heavier-traffic brands earlier so collisions resolve naturally.
export function detectBrandFromName(name: string): BrandEntry | null {
  const lower = (name ?? "").toLowerCase()
  if (!lower) return null
  for (const b of BRAND_REGISTRY) {
    if (!b.keywords) continue
    for (const k of b.keywords) {
      if (lower.includes(k)) return b
    }
  }
  return null
}

// detectBrandFromValue matches value prefixes (case-sensitive — token
// shapes are deterministic). Returns null when no prefix matches.
export function detectBrandFromValue(value: string): BrandEntry | null {
  const v = (value ?? "").trim()
  if (v.length < 4) return null
  for (const b of BRAND_REGISTRY) {
    if (!b.prefixes) continue
    for (const p of b.prefixes) {
      if (v.startsWith(p)) return b
    }
  }
  return null
}

// Categories in stable display order for the picker.
export const BRAND_CATEGORIES: BrandCategory[] = [
  "AI", "Cloud", "DevOps", "Source", "Comms",
  "Productivity", "Auth", "Payments", "Analytics",
  "Search", "Database", "Storage", "Email",
  "Social", "Marketing", "Maps", "ML", "Other",
]
