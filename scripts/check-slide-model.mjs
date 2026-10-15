// Run through crewship-heavy from repository root (Node24 and Go required).
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { canonicalSlideDeck, canonicalSlideElement, SLIDES_HASH_DOMAIN } from '../lib/slides/model.ts'
const fixture = JSON.parse(readFileSync('testdata/slides-schema-v1.json', 'utf8'))
const hash = json => createHash('sha256').update(SLIDES_HASH_DOMAIN + json, 'utf8').digest('hex')
const canonical = canonicalSlideDeck(fixture.deck)
assert.equal(canonical, fixture.canonical_json)
assert.equal(hash(canonical), fixture.hash)
const elements = Object.fromEntries(fixture.deck.slides.flatMap(slide => slide.elements.map(element => [element.id, hash(canonicalSlideElement(element))])))
const go = JSON.parse(execFileSync('go', ['run', './scripts/slide-model-contract'], { encoding: 'utf8' }))
assert.deepEqual(go, { canonical_json: canonical, hash: hash(canonical), elements })
console.log(`Slides schema1 actual Go/TS canonical+hash agreement: ${fixture.deck.slides.length} slides, ${Object.keys(elements).length} elements`)
