import { Suspense } from "react"
import { SkillsLayout } from "@/components/features/skills/skills-layout"

// The Skills page owns its chrome (SubBar, explorer, content pane) and sets
// the viewport height itself, like Routines and Issues. Suspense because the
// open skill and filters live in the query string (useSearchParams).
export default function SkillsPage() {
  return (
    <Suspense>
      <SkillsLayout />
    </Suspense>
  )
}
