/// <reference types="vite/client" />
import * as React from "react"
import { createRoot } from "react-dom/client"
import "../../app/globals.css"
import fixture from "../../testdata/slides-schema-v1.json"
import type { SlideDeck } from "../../lib/slides/model"
import { SlidesEditor } from "../../components/features/slides/slides-editor"

function Fixture() {
  const [deck, setDeck] = React.useState<unknown>(structuredClone(fixture.deck))
  return <main className="mx-auto max-w-6xl p-3"><h1>Native Slides component fixture</h1><SlidesEditor deck={deck} onChange={setDeck} /><button onClick={() => setDeck({ ...fixture.deck, html: "<script>unexpected</script>" })}>Load unsupported fixture</button><output data-testid="deck" hidden>{JSON.stringify(deck as SlideDeck)}</output></main>
}
createRoot(document.getElementById("root")!).render(<Fixture />)
