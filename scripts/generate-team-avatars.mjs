// Deterministic demo portraits using Crewship's existing avatar package.
// Run: node scripts/generate-team-avatars.mjs
import { createAvatar } from '@dicebear/core'
import * as avataaars from '@dicebear/avataaars'
import sharp from 'sharp'
import { mkdir } from 'node:fs/promises'
const directory = new URL('../cmd/crewship/seeddata/team-avatars/', import.meta.url)
await mkdir(directory, { recursive: true })
const people = [
  ['thomas', 'shortFlat', 'bfdbfe', 'blazerAndShirt'],
  ['paul', 'shortCurly', 'ddd6fe', 'collarAndSweater'],
  ['peter', 'shaggy', 'a7f3d0', 'hoodie'],
  ['anna', 'bob', 'fecdd3', 'shirtScoopNeck'],
  ['sofia', 'curly', 'fed7aa', 'overall'],
  ['emma', 'straight01', 'a5f3fc', 'blazerAndSweater'],
]
for (const [key, top, backgroundColor, clothing] of people) {
  const svg = createAvatar(avataaars, {
    seed: `crewship-team-demo-${key}`, size: 128, top: [top], backgroundColor: [backgroundColor],
    clothing: [clothing], eyes: ['default'], mouth: ['smile'], facialHairProbability: 0, accessoriesProbability: 0,
  }).toString()
  await sharp(Buffer.from(svg)).png().toFile(new URL(`${key}.png`, directory).pathname)
}
