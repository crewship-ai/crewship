// Run through crewship-heavy from repository root (Node24 and Go required).
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { canonicalSlideDeck, canonicalSlideElement, slideDeckHashDomain, slideElementHashDomain, SLIDES_MAX_TABLE_CELLS, SLIDES_MAX_TABLE_CELLS_PER_SLIDE, SLIDES_IMAGE_MAX_DIMENSION, SLIDES_IMAGE_MAX_ASSET_PIXELS, SLIDES_IMAGE_MAX_UPLOAD_BYTES, SLIDES_IMAGE_MAX_STORED_BYTES } from '../lib/slides/model.ts'
const hash = (domain, json) => createHash('sha256').update(domain + json, 'utf8').digest('hex')
for (const version of [1, 2]) {
 const path = `testdata/slides-schema-v${version}.json`
 const fixture = JSON.parse(readFileSync(path, 'utf8'))
 const canonical = canonicalSlideDeck(fixture.deck)
 assert.equal(canonical, fixture.canonical_json)
 const deckHash = hash(slideDeckHashDomain(version), canonical)
 assert.equal(deckHash, fixture.hash)
 const elements = Object.fromEntries(fixture.deck.slides.flatMap(slide => slide.elements.map(element => [element.id, hash(slideElementHashDomain(element), canonicalSlideElement(element))])))
 const go = JSON.parse(execFileSync('go', ['run', './scripts/slide-model-contract', path], { encoding: 'utf8' }))
 assert.deepEqual(go, { canonical_json: canonical, hash: deckHash, elements, raster_limits: { dimension: SLIDES_IMAGE_MAX_DIMENSION, pixels: SLIDES_IMAGE_MAX_ASSET_PIXELS, upload_bytes: SLIDES_IMAGE_MAX_UPLOAD_BYTES, stored_bytes: SLIDES_IMAGE_MAX_STORED_BYTES }, table_cell_limits: { deck: SLIDES_MAX_TABLE_CELLS, slide: SLIDES_MAX_TABLE_CELLS_PER_SLIDE } })
 console.log(`Slides schema${version} actual Go/TS canonical+hash agreement: ${fixture.deck.slides.length} slides, ${Object.keys(elements).length} elements`)
}
