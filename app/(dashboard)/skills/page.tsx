import { SkillsBrowser } from "@/components/features/skills/skills-browser"

// Skills browser owns its own chrome (toolbar + 3-panel resizable
// layout), mirroring OrchestrationLayout's `h-[calc(100vh-48px)]`
// outer + 48px app toolbar offset. The browser already sets that
// viewport height internally so this wrapper is intentionally thin —
// changing it to padded would re-introduce the cropping bug visible
// in the round-1 screenshot.
export default function SkillsPage() {
  return <SkillsBrowser />
}
