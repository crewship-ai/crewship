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

import type { ComponentType, SVGProps } from "react"
import { Key } from "lucide-react"

import {
  // AI / inference
  SiAnthropic, SiOpenai, SiGooglegemini, SiHuggingface,
  SiPerplexity, SiReplicate, SiOllama, SiElevenlabs,
  // Cloud / infra
  SiGooglecloud, SiCloudflare, SiVercel, SiNetlify,
  SiRailway, SiRender, SiDigitalocean, SiHeroku,
  SiSupabase, SiFirebase, SiPlanetscale,
  // Source control / dev
  SiGithub, SiGitlab, SiBitbucket, SiCodeberg, SiJetbrains,
  SiReplit, SiGitpod, SiCodesandbox,
  // Communication
  SiSlack, SiDiscord, SiTelegram, SiWhatsapp, SiSignal,
  SiTwilio, SiSendgrid, SiMailgun, SiResend, SiMailchimp,
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
  SiZoho, SiSalesforce, SiHubspot, SiZapier, SiMake,
  // CMS / web
  SiWebflow, SiWordpress, SiGhost, SiContentful, SiSanity,
  SiStrapi, SiStoryblok,
} from "react-icons/si"

// AWS doesn't have a usable Simple Icons entry, so we keep our own
// curated SVG. Same for Cursor and Factory.
import {
  AWSIcon, CursorIcon, FactoryIcon, CustomCLIIcon,
} from "@/components/icons/provider-icons"

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

  // ─── Cloud / infra ──────────────────────────────────────────────
  { key: "AWS", label: "AWS", hex: "#FF9900", Icon: AWSIcon, category: "Cloud", keywords: ["aws", "amazon"], prefixes: ["AKIA", "ASIA"] },
  { key: "GCP", label: "Google Cloud", hex: "#4285F4", Icon: SiGooglecloud as IconComponent, category: "Cloud", keywords: ["gcp", "googlecloud", "gcloud"] },
  { key: "CLOUDFLARE", label: "Cloudflare", hex: "#F38020", Icon: SiCloudflare as IconComponent, category: "Cloud", keywords: ["cloudflare", "cf_"] },
  { key: "VERCEL", label: "Vercel", hex: "#000000", darkHex: "#FFFFFF", Icon: SiVercel as IconComponent, category: "Cloud", keywords: ["vercel"] },
  { key: "NETLIFY", label: "Netlify", hex: "#00C7B7", Icon: SiNetlify as IconComponent, category: "Cloud", keywords: ["netlify"] },
  { key: "RAILWAY", label: "Railway", hex: "#0B0D0E", darkHex: "#FFFFFF", Icon: SiRailway as IconComponent, category: "Cloud", keywords: ["railway"] },
  { key: "RENDER", label: "Render", hex: "#46E3B7", Icon: SiRender as IconComponent, category: "Cloud", keywords: ["render"] },
  { key: "DIGITALOCEAN", label: "DigitalOcean", hex: "#0080FF", Icon: SiDigitalocean as IconComponent, category: "Cloud", keywords: ["digitalocean", "do_"] },
  { key: "HEROKU", label: "Heroku", hex: "#430098", Icon: SiHeroku as IconComponent, category: "Cloud", keywords: ["heroku"] },
  { key: "SUPABASE", label: "Supabase", hex: "#3ECF8E", Icon: SiSupabase as IconComponent, category: "Cloud", keywords: ["supabase"] },
  { key: "FIREBASE", label: "Firebase", hex: "#DD2C00", Icon: SiFirebase as IconComponent, category: "Cloud", keywords: ["firebase", "fcm"] },
  { key: "PLANETSCALE", label: "PlanetScale", hex: "#000000", darkHex: "#FFFFFF", Icon: SiPlanetscale as IconComponent, category: "Cloud", keywords: ["planetscale"] },

  // ─── Source control / dev ───────────────────────────────────────
  { key: "GITHUB", label: "GitHub", hex: "#181717", Icon: SiGithub as IconComponent, category: "Source", keywords: ["github", "gh_"], prefixes: ["ghp_", "gho_", "ghs_", "github_pat_", "ghu_"] },
  { key: "GITLAB", label: "GitLab", hex: "#FC6D26", Icon: SiGitlab as IconComponent, category: "Source", keywords: ["gitlab", "gl_"], prefixes: ["glpat-"] },
  { key: "BITBUCKET", label: "Bitbucket", hex: "#0052CC", Icon: SiBitbucket as IconComponent, category: "Source", keywords: ["bitbucket"] },
  { key: "CODEBERG", label: "Codeberg", hex: "#2185D0", Icon: SiCodeberg as IconComponent, category: "Source", keywords: ["codeberg"] },
  { key: "JETBRAINS", label: "JetBrains", hex: "#000000", darkHex: "#FFFFFF", Icon: SiJetbrains as IconComponent, category: "Source", keywords: ["jetbrains", "intellij", "pycharm", "webstorm"] },
  { key: "REPLIT", label: "Replit", hex: "#F26207", Icon: SiReplit as IconComponent, category: "Source", keywords: ["replit"] },
  { key: "GITPOD", label: "Gitpod", hex: "#FFAE33", Icon: SiGitpod as IconComponent, category: "Source", keywords: ["gitpod"] },
  { key: "CODESANDBOX", label: "CodeSandbox", hex: "#151515", darkHex: "#FFFFFF", Icon: SiCodesandbox as IconComponent, category: "Source", keywords: ["codesandbox"] },

  // ─── Communication ──────────────────────────────────────────────
  { key: "SLACK", label: "Slack", hex: "#4A154B", Icon: SiSlack as IconComponent, category: "Comms", keywords: ["slack"], prefixes: ["xoxb-", "xoxp-", "xoxa-"] },
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
  { key: "LINE", label: "LINE", hex: "#00C300", Icon: SiLine as IconComponent, category: "Comms", keywords: ["line"] },

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
  { key: "MIRO", label: "Miro", hex: "#050038", Icon: SiMiro as IconComponent, category: "Productivity", keywords: ["miro"] },
  { key: "FIGMA", label: "Figma", hex: "#F24E1E", Icon: SiFigma as IconComponent, category: "Productivity", keywords: ["figma"] },
  { key: "TODOIST", label: "Todoist", hex: "#E44332", Icon: SiTodoist as IconComponent, category: "Productivity", keywords: ["todoist"] },
  { key: "EVERNOTE", label: "Evernote", hex: "#00A82D", Icon: SiEvernote as IconComponent, category: "Productivity", keywords: ["evernote"] },
  { key: "BASECAMP", label: "Basecamp", hex: "#1D2D35", darkHex: "#FFFFFF", Icon: SiBasecamp as IconComponent, category: "Productivity", keywords: ["basecamp"] },
  { key: "CLICKUP", label: "ClickUp", hex: "#7B68EE", Icon: SiClickup as IconComponent, category: "Productivity", keywords: ["clickup"] },
  { key: "OBSIDIAN", label: "Obsidian", hex: "#7C3AED", Icon: SiObsidian as IconComponent, category: "Productivity", keywords: ["obsidian"] },
  { key: "CALENDLY", label: "Calendly", hex: "#006BFF", Icon: SiCalendly as IconComponent, category: "Productivity", keywords: ["calendly"] },

  // ─── Auth ───────────────────────────────────────────────────────
  { key: "AUTH0", label: "Auth0", hex: "#EB5424", Icon: SiAuth0 as IconComponent, category: "Auth", keywords: ["auth0"] },
  { key: "OKTA", label: "Okta", hex: "#007DC1", Icon: SiOkta as IconComponent, category: "Auth", keywords: ["okta"] },
  { key: "ONEPASSWORD", label: "1Password", hex: "#0572EC", Icon: Si1Password as IconComponent, category: "Auth", keywords: ["1password", "onepassword"], prefixes: ["ops_"] },
  { key: "LASTPASS", label: "LastPass", hex: "#D32D27", Icon: SiLastpass as IconComponent, category: "Auth", keywords: ["lastpass"] },
  { key: "BITWARDEN", label: "Bitwarden", hex: "#175DDC", Icon: SiBitwarden as IconComponent, category: "Auth", keywords: ["bitwarden"] },
  { key: "DASHLANE", label: "Dashlane", hex: "#0E353D", darkHex: "#22D3EE", Icon: SiDashlane as IconComponent, category: "Auth", keywords: ["dashlane"] },
  { key: "CLERK", label: "Clerk", hex: "#6C47FF", Icon: SiClerk as IconComponent, category: "Auth", keywords: ["clerk"], prefixes: ["sk_test_clerk_", "pk_test_clerk_"] },

  // ─── Payments ───────────────────────────────────────────────────
  { key: "STRIPE", label: "Stripe", hex: "#635BFF", Icon: SiStripe as IconComponent, category: "Payments", keywords: ["stripe"], prefixes: ["sk_test_", "sk_live_", "pk_test_", "pk_live_", "rk_"] },
  { key: "PAYPAL", label: "PayPal", hex: "#003087", Icon: SiPaypal as IconComponent, category: "Payments", keywords: ["paypal"] },
  { key: "SQUARE", label: "Square", hex: "#3E4348", Icon: SiSquare as IconComponent, category: "Payments", keywords: ["square"] },
  { key: "KLARNA", label: "Klarna", hex: "#FFA8CD", Icon: SiKlarna as IconComponent, category: "Payments", keywords: ["klarna"] },
  { key: "SHOPIFY", label: "Shopify", hex: "#7AB55C", Icon: SiShopify as IconComponent, category: "Payments", keywords: ["shopify"], prefixes: ["shpat_", "shpca_", "shppa_"] },
  { key: "WOOCOMMERCE", label: "WooCommerce", hex: "#7F54B3", Icon: SiWoocommerce as IconComponent, category: "Payments", keywords: ["woocommerce"], prefixes: ["ck_", "cs_"] },
  { key: "VISA", label: "Visa", hex: "#1A1F71", Icon: SiVisa as IconComponent, category: "Payments", keywords: ["visa"] },
  { key: "MASTERCARD", label: "Mastercard", hex: "#EB001B", Icon: SiMastercard as IconComponent, category: "Payments", keywords: ["mastercard"] },

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

  // ─── Search ─────────────────────────────────────────────────────
  { key: "BRAVE", label: "Brave Search", hex: "#FB542B", Icon: SiBrave as IconComponent, category: "Search", keywords: ["brave"], prefixes: ["BSA"] },
  { key: "ALGOLIA", label: "Algolia", hex: "#003DFF", Icon: SiAlgolia as IconComponent, category: "Search", keywords: ["algolia"] },
  { key: "MEILISEARCH", label: "Meilisearch", hex: "#FF5CAA", Icon: SiMeilisearch as IconComponent, category: "Search", keywords: ["meilisearch", "meili_"] },
  { key: "ELASTICSEARCH", label: "Elasticsearch", hex: "#005571", Icon: SiElasticsearch as IconComponent, category: "Search", keywords: ["elasticsearch", "es_"] },
  { key: "OPENSEARCH", label: "OpenSearch", hex: "#005EB8", Icon: SiOpensearch as IconComponent, category: "Search", keywords: ["opensearch"] },

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
  { key: "INSOMNIA", label: "Insomnia", hex: "#4000BF", Icon: SiInsomnia as IconComponent, category: "DevOps", keywords: ["insomnia"] },
  { key: "NGROK", label: "ngrok", hex: "#1F1E37", darkHex: "#FFFFFF", Icon: SiNgrok as IconComponent, category: "DevOps", keywords: ["ngrok"] },
  { key: "CYPRESS", label: "Cypress", hex: "#69D3A7", Icon: SiCypress as IconComponent, category: "DevOps", keywords: ["cypress"] },
  { key: "STORYBOOK", label: "Storybook", hex: "#FF4785", Icon: SiStorybook as IconComponent, category: "DevOps", keywords: ["storybook"] },
  { key: "SWAGGER", label: "Swagger / OpenAPI", hex: "#85EA2D", Icon: SiSwagger as IconComponent, category: "DevOps", keywords: ["swagger", "openapi"] },
  { key: "GRAPHQL", label: "GraphQL", hex: "#E10098", Icon: SiGraphql as IconComponent, category: "DevOps", keywords: ["graphql"] },
  { key: "PRISMA", label: "Prisma Cloud", hex: "#2D3748", Icon: SiPrisma as IconComponent, category: "DevOps", keywords: ["prisma"] },

  // ─── File storage / CDN ─────────────────────────────────────────
  { key: "BACKBLAZE", label: "Backblaze", hex: "#E72C2A", Icon: SiBackblaze as IconComponent, category: "Storage", keywords: ["backblaze", "b2_"] },
  { key: "DROPBOX", label: "Dropbox", hex: "#0061FF", Icon: SiDropbox as IconComponent, category: "Storage", keywords: ["dropbox"] },
  { key: "GDRIVE", label: "Google Drive", hex: "#4285F4", Icon: SiGoogledrive as IconComponent, category: "Storage", keywords: ["googledrive", "drive"] },
  { key: "CLOUDINARY", label: "Cloudinary", hex: "#3448C5", Icon: SiCloudinary as IconComponent, category: "Storage", keywords: ["cloudinary"] },

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

  // ─── Email / marketing ──────────────────────────────────────────
  { key: "SUBSTACK", label: "Substack", hex: "#FF6719", Icon: SiSubstack as IconComponent, category: "Email", keywords: ["substack"] },

  // ─── Social / APIs ──────────────────────────────────────────────
  { key: "X", label: "X (Twitter)", hex: "#000000", darkHex: "#FFFFFF", Icon: SiX as IconComponent, category: "Social", keywords: ["twitter", "x.com", "x_"] },
  { key: "REDDIT", label: "Reddit", hex: "#FF4500", Icon: SiReddit as IconComponent, category: "Social", keywords: ["reddit"] },
  { key: "YOUTUBE", label: "YouTube", hex: "#FF0000", Icon: SiYoutube as IconComponent, category: "Social", keywords: ["youtube"] },
  { key: "TWITCH", label: "Twitch", hex: "#9146FF", Icon: SiTwitch as IconComponent, category: "Social", keywords: ["twitch"] },
  { key: "FACEBOOK", label: "Facebook / Meta", hex: "#0866FF", Icon: SiFacebook as IconComponent, category: "Social", keywords: ["facebook", "meta", "fb_"] },
  { key: "INSTAGRAM", label: "Instagram", hex: "#E4405F", Icon: SiInstagram as IconComponent, category: "Social", keywords: ["instagram", "ig_"] },
  { key: "TIKTOK", label: "TikTok", hex: "#000000", darkHex: "#FFFFFF", Icon: SiTiktok as IconComponent, category: "Social", keywords: ["tiktok"] },
  { key: "SPOTIFY", label: "Spotify", hex: "#1ED760", Icon: SiSpotify as IconComponent, category: "Social", keywords: ["spotify"] },
  { key: "PINTEREST", label: "Pinterest", hex: "#BD081C", Icon: SiPinterest as IconComponent, category: "Social", keywords: ["pinterest"] },

  // ─── E-commerce ─────────────────────────────────────────────────
  { key: "BIGCOMMERCE", label: "BigCommerce", hex: "#121118", darkHex: "#FFFFFF", Icon: SiBigcommerce as IconComponent, category: "Other", keywords: ["bigcommerce"] },
  { key: "ETSY", label: "Etsy", hex: "#F1641E", Icon: SiEtsy as IconComponent, category: "Other", keywords: ["etsy"] },

  // ─── Maps / geo ─────────────────────────────────────────────────
  { key: "GMAPS", label: "Google Maps", hex: "#4285F4", Icon: SiGooglemaps as IconComponent, category: "Maps", keywords: ["googlemaps", "maps"] },
  { key: "MAPBOX", label: "Mapbox", hex: "#000000", darkHex: "#FFFFFF", Icon: SiMapbox as IconComponent, category: "Maps", keywords: ["mapbox"], prefixes: ["pk.eyJ", "sk.eyJ"] },
  { key: "OSM", label: "OpenStreetMap", hex: "#7EBC6F", Icon: SiOpenstreetmap as IconComponent, category: "Maps", keywords: ["openstreetmap", "osm"] },

  // ─── ML / data ──────────────────────────────────────────────────
  { key: "TENSORFLOW", label: "TensorFlow", hex: "#FF6F00", Icon: SiTensorflow as IconComponent, category: "ML", keywords: ["tensorflow"] },
  { key: "PYTORCH", label: "PyTorch", hex: "#EE4C2C", Icon: SiPytorch as IconComponent, category: "ML", keywords: ["pytorch"] },
  { key: "PANDAS", label: "Pandas", hex: "#150458", Icon: SiPandas as IconComponent, category: "ML", keywords: ["pandas"] },
  { key: "NUMPY", label: "NumPy", hex: "#013243", darkHex: "#4DABCF", Icon: SiNumpy as IconComponent, category: "ML", keywords: ["numpy"] },
  { key: "SKLEARN", label: "scikit-learn", hex: "#F7931E", Icon: SiScikitlearn as IconComponent, category: "ML", keywords: ["scikit", "sklearn"] },
  { key: "KERAS", label: "Keras", hex: "#D00000", Icon: SiKeras as IconComponent, category: "ML", keywords: ["keras"] },
  { key: "MLFLOW", label: "MLflow", hex: "#0194E2", Icon: SiMlflow as IconComponent, category: "ML", keywords: ["mlflow"] },
  { key: "NVIDIA", label: "NVIDIA", hex: "#76B900", Icon: SiNvidia as IconComponent, category: "ML", keywords: ["nvidia"], prefixes: ["nvapi-"] },
  { key: "INTEL", label: "Intel", hex: "#0071C5", Icon: SiIntel as IconComponent, category: "ML", keywords: ["intel"] },

  // ─── CRM / marketing ────────────────────────────────────────────
  { key: "ZOHO", label: "Zoho", hex: "#C8202F", Icon: SiZoho as IconComponent, category: "Marketing", keywords: ["zoho"] },
  { key: "SALESFORCE", label: "Salesforce", hex: "#00A1E0", Icon: SiSalesforce as IconComponent, category: "Marketing", keywords: ["salesforce"] },
  { key: "HUBSPOT", label: "HubSpot", hex: "#FF7A59", Icon: SiHubspot as IconComponent, category: "Marketing", keywords: ["hubspot"] },
  { key: "ZAPIER", label: "Zapier", hex: "#FF4F00", Icon: SiZapier as IconComponent, category: "Marketing", keywords: ["zapier"] },
  { key: "MAKE", label: "Make", hex: "#6D00CC", Icon: SiMake as IconComponent, category: "Marketing", keywords: ["make.com", "integromat"] },

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
