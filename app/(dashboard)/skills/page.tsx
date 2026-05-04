import { SkillsBrowser } from "@/components/features/skills/skills-browser"

// Full-bleed: skills browser owns its own panels (PanelGroup with auto-
// saved sizes) and sits flush against the dashboard chrome — no padded
// shell, no extra page header. Mirrors the layout chrome pattern of
// CrewsLayout / OrchestrationLayout where the left rail butts up
// against the screen edge.
export default function SkillsPage() {
  return (
    <div className="h-full min-h-0 flex flex-col">
      <SkillsBrowser />
    </div>
  )
}
