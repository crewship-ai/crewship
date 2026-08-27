import { DesignLayout } from "@/components/features/design/design-layout"

// /design — the create-surface unification proposal, running in the product.
//
// It owns its own chrome (SubBar + scrollable body at `h-[calc(100vh-48px)]`),
// the same shape /skills and /credentials use, so this wrapper stays thin.
//
// This page is scaffolding. It has no data, no API and no CLI command — the
// standing rule that every endpoint gets one does not apply, because there is
// no endpoint. Delete it, and components/features/design with it, once the
// audit table it carries has no rows left to fix.
export default function DesignPage() {
  return <DesignLayout />
}
