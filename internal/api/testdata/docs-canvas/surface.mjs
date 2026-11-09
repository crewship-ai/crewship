// Test helper for the schema 6 real-engine CLI test (#5149): with the engine's
// own pinned Yjs and surface model, either builds the human update that places
// blocks on the Canvas, or reports the placements of a read document state.
// Input (stdin, JSON): { snapshot, updates, place?: { [blockId]: {x,y,w,h,z} } }.
import { pathToFileURL } from 'node:url';
import { join } from 'node:path';

const root = process.argv[2];
const { load, decode, encode } = await import(pathToFileURL(join(root, 'tools/docs-service/schema.mjs')).href);
const { createSurface } = await import(pathToFileURL(join(root, 'tools/docs-service/surface-core.mjs')).href);
const Y = await import(pathToFileURL(join(root, 'tools/docs-service/node_modules/yjs/dist/yjs.mjs')).href);
const surface = createSurface({ Y });

let input = '';
for await (const chunk of process.stdin) input += chunk;
const state = JSON.parse(input);
const doc = load(state.snapshot);
for (const update of state.updates ?? []) Y.applyUpdate(doc, decode(update));
if (state.place) {
  const vector = Y.encodeStateVector(doc);
  for (const [id, geometry] of Object.entries(state.place)) surface.place(doc, id, geometry, 'test');
  process.stdout.write(JSON.stringify({ update: encode(Y.encodeStateAsUpdate(doc, vector)) }));
} else {
  process.stdout.write(JSON.stringify({ placements: surface.project(doc).placements }));
}
doc.destroy();
