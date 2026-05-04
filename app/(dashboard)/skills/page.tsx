import { SkillsBrowser } from "@/components/features/skills/skills-browser"

// Skills browser uses the orchestration-style 3-panel layout: left
// filters / centre virtualised grid / right detail. PageShell from the
// other dashboard pages would force a header bar that fights the
// browser's own left-panel title row, so we render a minimal wrapper
// instead. The browser owns its own scroll containers.
export default function SkillsPage() {
  return (
    <div className="flex flex-col gap-3 p-4 md:p-6 h-full">
      <SkillsBrowser />
    </div>
  )
}
