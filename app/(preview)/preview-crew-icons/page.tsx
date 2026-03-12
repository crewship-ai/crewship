"use client"

import { useState } from "react"
import {
  Code, Shield, Megaphone, BarChart3, Users, Rocket, Brain, Palette,
  Briefcase, Globe, Zap, Heart, Database, Lock, Bug, Network,
  Bot, Package, Lightbulb, Wrench, Truck, GraduationCap,
} from "lucide-react"
import { cn } from "@/lib/utils"

/* ── Crew icon definitions ───────────────────────────── */

const CREW_ICONS = [
  { name: "Engineering", icon: Code },
  { name: "Security", icon: Shield },
  { name: "Marketing", icon: Megaphone },
  { name: "Analytics", icon: BarChart3 },
  { name: "People", icon: Users },
  { name: "Growth", icon: Rocket },
  { name: "AI / ML", icon: Brain },
  { name: "Design", icon: Palette },
  { name: "Business", icon: Briefcase },
  { name: "Global Ops", icon: Globe },
  { name: "Automation", icon: Zap },
  { name: "Support", icon: Heart },
  { name: "Data", icon: Database },
  { name: "Compliance", icon: Lock },
  { name: "QA", icon: Bug },
  { name: "DevOps", icon: Network },
  { name: "AI Agents", icon: Bot },
  { name: "Logistics", icon: Truck },
  { name: "R&D", icon: Lightbulb },
  { name: "Maintenance", icon: Wrench },
  { name: "Packaging", icon: Package },
  { name: "Education", icon: GraduationCap },
]

/* ── Variant A: Subtle muted gradients ──────────────── */

const GRADIENT_PALETTES = [
  { from: "from-blue-500/15", to: "to-indigo-500/15", text: "text-blue-600 dark:text-blue-400" },
  { from: "from-emerald-500/15", to: "to-teal-500/15", text: "text-emerald-600 dark:text-emerald-400" },
  { from: "from-violet-500/15", to: "to-purple-500/15", text: "text-violet-600 dark:text-violet-400" },
  { from: "from-amber-500/15", to: "to-orange-500/15", text: "text-amber-600 dark:text-amber-400" },
  { from: "from-rose-500/15", to: "to-pink-500/15", text: "text-rose-600 dark:text-rose-400" },
  { from: "from-cyan-500/15", to: "to-sky-500/15", text: "text-cyan-600 dark:text-cyan-400" },
  { from: "from-lime-500/15", to: "to-green-500/15", text: "text-lime-600 dark:text-lime-400" },
  { from: "from-fuchsia-500/15", to: "to-pink-500/15", text: "text-fuchsia-600 dark:text-fuchsia-400" },
]

function GradientIcon({ icon: Icon, name, index }: { icon: typeof Code; name: string; index: number }) {
  const p = GRADIENT_PALETTES[index % GRADIENT_PALETTES.length]
  return (
    <div className="flex items-center gap-3">
      <div className={cn("h-10 w-10 rounded-xl bg-gradient-to-br flex items-center justify-center shrink-0", p.from, p.to)}>
        <Icon className={cn("h-5 w-5", p.text)} />
      </div>
      <span className="text-sm font-medium">{name}</span>
    </div>
  )
}

/* ── Variant B: Monochrome on muted ─────────────────── */

const MONO_COLORS = [
  "text-foreground/70",
  "text-primary",
  "text-foreground/60",
  "text-primary/80",
  "text-foreground/50",
  "text-primary/70",
  "text-foreground/70",
  "text-primary/90",
]

function MonoIcon({ icon: Icon, name, index }: { icon: typeof Code; name: string; index: number }) {
  const c = MONO_COLORS[index % MONO_COLORS.length]
  return (
    <div className="flex items-center gap-3">
      <div className="h-10 w-10 rounded-xl bg-muted/60 border border-border/50 flex items-center justify-center shrink-0">
        <Icon className={cn("h-5 w-5", c)} />
      </div>
      <span className="text-sm font-medium">{name}</span>
    </div>
  )
}

/* ── Variant C: Glass pills ──────────────────────────── */

const GLASS_ACCENTS = [
  { ring: "ring-blue-500/20", text: "text-blue-600 dark:text-blue-400" },
  { ring: "ring-emerald-500/20", text: "text-emerald-600 dark:text-emerald-400" },
  { ring: "ring-violet-500/20", text: "text-violet-600 dark:text-violet-400" },
  { ring: "ring-amber-500/20", text: "text-amber-600 dark:text-amber-400" },
  { ring: "ring-rose-500/20", text: "text-rose-600 dark:text-rose-400" },
  { ring: "ring-cyan-500/20", text: "text-cyan-600 dark:text-cyan-400" },
  { ring: "ring-lime-500/20", text: "text-lime-600 dark:text-lime-400" },
  { ring: "ring-fuchsia-500/20", text: "text-fuchsia-600 dark:text-fuchsia-400" },
]

function GlassIcon({ icon: Icon, name, index }: { icon: typeof Code; name: string; index: number }) {
  const a = GLASS_ACCENTS[index % GLASS_ACCENTS.length]
  return (
    <div className="flex items-center gap-3">
      <div className={cn("h-10 w-10 rounded-xl bg-card/80 backdrop-blur-sm ring-1 flex items-center justify-center shrink-0", a.ring)}>
        <Icon className={cn("h-5 w-5", a.text)} />
      </div>
      <span className="text-sm font-medium">{name}</span>
    </div>
  )
}

/* ── Crew card mockup ────────────────────────────────── */

function MockCrewCard({
  name,
  icon: Icon,
  index,
  variant,
  agents,
  members,
}: {
  name: string
  icon: typeof Code
  index: number
  variant: "gradient" | "mono" | "glass"
  agents: number
  members: number
}) {
  const IconComponent = variant === "gradient" ? GradientIcon : variant === "mono" ? MonoIcon : GlassIcon

  return (
    <div className="border rounded-xl p-4 bg-card hover:border-primary/30 transition-colors">
      <IconComponent icon={Icon} name={name} index={index} />
      <p className="text-xs text-muted-foreground mt-2 ml-[52px]">
        Automated workflows for {name.toLowerCase()} tasks
      </p>
      <div className="mt-3 pt-3 border-t flex items-center gap-4 text-xs text-muted-foreground">
        <span className="flex items-center gap-1">
          <Bot className="h-3 w-3" />
          {agents} agents
        </span>
        <span className="flex items-center gap-1">
          <Users className="h-3 w-3" />
          {members} members
        </span>
      </div>
    </div>
  )
}

/* ── Page ─────────────────────────────────────────────── */

export default function PreviewCrewIconsPage() {
  const [variant, setVariant] = useState<"gradient" | "mono" | "glass">("gradient")

  return (
    <div className="min-h-screen bg-background p-6 md:p-10">
      <div className="max-w-5xl mx-auto space-y-8">
        <div>
          <h1 className="text-xl font-bold">Crew Icons -- Lucide + Theme Colors</h1>
          <p className="text-sm text-muted-foreground mt-1">
            Three approaches for crew icons using Lucide (already in project). All respect light/dark theme.
          </p>
        </div>

        {/* Variant picker */}
        <div className="flex gap-2">
          {(["gradient", "mono", "glass"] as const).map((v) => (
            <button
              key={v}
              onClick={() => setVariant(v)}
              className={cn(
                "px-4 py-2 rounded-lg text-sm font-medium transition-colors capitalize",
                variant === v
                  ? "bg-primary text-primary-foreground"
                  : "bg-muted text-muted-foreground hover:text-foreground"
              )}
            >
              {v === "gradient" ? "A) Subtle Gradients" : v === "mono" ? "B) Monochrome" : "C) Glass"}
            </button>
          ))}
        </div>

        {/* Icon grid -- all 22 icons */}
        <div>
          <h2 className="text-sm font-semibold mb-3">All Available Icons</h2>
          <div className="grid grid-cols-2 sm:grid-cols-4 md:grid-cols-6 gap-3">
            {CREW_ICONS.map(({ name, icon: Icon }, i) => {
              const Comp = variant === "gradient" ? GradientIcon : variant === "mono" ? MonoIcon : GlassIcon
              return (
                <div key={name} className="border rounded-lg p-3 bg-card/50">
                  <Comp icon={Icon} name={name} index={i} />
                </div>
              )
            })}
          </div>
        </div>

        {/* Crew cards mockup */}
        <div>
          <h2 className="text-sm font-semibold mb-3">Crew Cards</h2>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
            {CREW_ICONS.slice(0, 6).map(({ name, icon }, i) => (
              <MockCrewCard
                key={name}
                name={name}
                icon={icon}
                index={i}
                variant={variant}
                agents={Math.floor(Math.random() * 8) + 2}
                members={Math.floor(Math.random() * 5) + 1}
              />
            ))}
          </div>
        </div>

        {/* Inline comparison */}
        <div>
          <h2 className="text-sm font-semibold mb-3">Side-by-Side Comparison</h2>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
            {(["gradient", "mono", "glass"] as const).map((v) => (
              <div key={v} className="space-y-2">
                <h3 className="text-xs font-semibold uppercase text-muted-foreground tracking-wider">
                  {v === "gradient" ? "A) Subtle Gradients" : v === "mono" ? "B) Monochrome" : "C) Glass"}
                </h3>
                <div className="border rounded-xl p-4 bg-card space-y-3">
                  {CREW_ICONS.slice(0, 5).map(({ name, icon: Icon }, i) => {
                    const Comp = v === "gradient" ? GradientIcon : v === "mono" ? MonoIcon : GlassIcon
                    return <Comp key={name} icon={Icon} name={name} index={i} />
                  })}
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* Color palette selection preview */}
        <div>
          <h2 className="text-sm font-semibold mb-3">Color Picker (proposed replacement)</h2>
          <p className="text-xs text-muted-foreground mb-4">
            Instead of 12 bright pastels, use 8 subtle theme-compatible tones:
          </p>
          <div className="flex gap-3">
            {GRADIENT_PALETTES.map((p, i) => (
              <div
                key={i}
                className={cn("h-10 w-10 rounded-xl bg-gradient-to-br flex items-center justify-center border border-border/30 cursor-pointer hover:scale-110 transition-transform", p.from, p.to)}
              >
                <Code className={cn("h-5 w-5", p.text)} />
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
