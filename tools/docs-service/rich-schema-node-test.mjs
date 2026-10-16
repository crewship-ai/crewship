import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import { create, validate, validateJSON, load, snapshot, outline, hash, patch, anchor, resolve, markdown, find } from './schema.mjs';
const json = value => JSON.parse(JSON.stringify(value));
const paragraph = content => ({ type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'rich-text' }, content }] });
const link = { type: 'link', attrs: { href: 'https://example.invalid/docs?q=one' } };
const plain = paragraph([{ type: 'text', text: 'Legacy unchanged 🐎' }]);
const rich = paragraph([{ type: 'text', text: 'prefix ' }, { type: 'text', text: 'linked', marks: [link] }, { type: 'text', text: ' suffix' }]);
test('schema3 links and code are explicit; legacy schema2 and future images stay closed', () => {
 const doc = create(rich, 3); assert.deepEqual(json(validate(doc, 3)), rich); assert.throws(() => validate(doc, 2)); doc.destroy();
 const code = { type: 'doc', content: [{ type: 'codeBlock', attrs: { id: 'code' }, content: [{ type: 'text', text: '<script>literal()</script>\n```' }] }, { type: 'paragraph', attrs: { id: 'inline' }, content: [{ type: 'text', text: 'a`b', marks: [{ type: 'code', attrs: {} }] }] }] };
 const codeDoc = create(code, 3); assert.deepEqual(json(validate(codeDoc, 3)), code); assert.throws(() => create(code, 2)); codeDoc.destroy();
 assert.throws(() => create({ type: 'doc', content: [{ type: 'image', attrs: { id: 'future', asset_ref: 'ref', expected_sha256: 'a'.repeat(64) } }] }, 3));
});
test('schema3 validates closed link URLs and mark shapes before ProseMirror normalization', () => {
 for (const href of ['javascript:alert(1)', 'data:text/html,x', '//example.invalid/x', 'https://user:pass@example.invalid/', 'https://example.invalid/\n', ['https://example.invalid/']]) {
  const value = structuredClone(rich); value.content[0].content[1].marks[0].attrs.href = href; assert.throws(() => create(value, 3));
 }
 for (const mark of [{ type: 'bold' }, { ...link, attrs: { ...link.attrs, target: '_blank' } }, { type: 'code', attrs: { class: 'evil' } }]) {
  const value = structuredClone(rich); value.content[0].content[1].marks = [mark]; assert.throws(() => create(value, 3));
 }
 const mixed = structuredClone(rich); mixed.content[0].content[1].marks.push({ type: 'code' }); assert.throws(() => create(mixed, 3));
});
test('legacy JSON hash, binary and comment identities survive schema3 validation without normalization', async () => {
 const doc = create(plain, 2), bytes = snapshot(doc), before = await hash(validate(doc, 2)), comment = anchor(doc, 'rich-text', 0, 6, 'legacy-comment', 0);
 assert.deepEqual(validate(doc, 3), validate(doc, 2)); assert.equal(snapshot(doc), bytes); assert.equal(await hash(validate(doc, 3)), before);
 const reloaded = load(bytes); assert.deepEqual(resolve(reloaded, comment), resolve(doc, comment)); assert.equal((await outline(reloaded, 3))[0].hash, (await outline(doc, 2))[0].hash); doc.destroy(); reloaded.destroy();
});
test('schema3 validates visible and deleted Yjs formatting; removed unsafe marks cannot bypass closure', () => {
 const good = create(rich, 3), bytes = snapshot(good); assert.deepEqual(json(validate(load(bytes), 3)), rich);
 for (const attributes of [{ bold: {} }, { link: { href: 'javascript:alert(1)' } }, { link: { href: link.attrs.href, target: '_blank' } }]) {
  const doc = create(plain, 2), text = find(doc, 'rich-text').toArray()[0]; text.format(0, text.length, attributes); assert.throws(() => validate(doc, 3)); text.delete(0, text.length); assert.throws(() => validate(load(snapshot(doc)), 3)); doc.destroy();
 }
 good.destroy();
});
test('bounded semantic replacement creates plain text and preserves untouched linked characters and comments', () => {
 const doc = create(rich, 3), prefix = anchor(doc, 'rich-text', 0, 6, 'prefix', 0), suffix = anchor(doc, 'rich-text', 14, 20, 'suffix', 0);
 patch(doc, { op: 'replaceText', id: 'rich-text', text: 'prefix lNEWd suffix', range: { from: 8, to: 12 } }, 3);
 assert.deepEqual(json(validate(doc, 3)), paragraph([{ type: 'text', text: 'prefix ' }, { type: 'text', text: 'l', marks: [link] }, { type: 'text', text: 'NEW' }, { type: 'text', text: 'd', marks: [link] }, { type: 'text', text: ' suffix' }]));
 assert.equal(resolve(doc, prefix).status, 'attached'); assert.equal(resolve(doc, prefix).to, 6); assert.equal(resolve(doc, suffix).status, 'attached'); assert.equal(resolve(doc, suffix).quote, 'suffix'); doc.destroy();
});
test('schema3 Markdown emits literal code and safe links without changing legacy output', () => {
 assert.equal(markdown(plain), 'Legacy unchanged 🐎\n');
 assert.equal(markdown(rich), 'prefix [linked](<https://example.invalid/docs?q=one>) suffix\n');
 const json = { type: 'doc', content: [{ type: 'codeBlock', attrs: { id: 'code' }, content: [{ type: 'text', text: '<script>literal()</script>\n```' }] }] };
 assert.equal(markdown(json), '````\n<script>literal()</script>\n```\n````\n'); assert.throws(() => validateJSON(rich, 2));
});

test('code block comments use stable plain-text positions and retained formatting is forbidden', () => {
 const body = { type: 'doc', content: [{ type: 'codeBlock', attrs: { id: 'code' }, content: [{ type: 'text', text: 'literal <script> 🐎' }] }] };
 const doc = create(body, 3), comment = anchor(doc, 'code', 0, 7, 'code-comment', 0, 3);
 assert.equal(resolve(doc, comment, 3).status, 'attached'); assert.equal(resolve(doc, comment, 2).status, 'detached');
 const text = find(doc, 'code').toArray()[0]; text.format(0, text.length, { link: link.attrs }); assert.throws(() => validate(doc, 3)); text.delete(0, text.length);
 const reloaded = load(snapshot(doc)); assert.throws(() => validate(reloaded, 3)); reloaded.destroy(); doc.destroy();
});
test('marked repeated-text insert stays bounded and does not inherit a link', () => {
 const doc = create(paragraph([{type:'text',text:'aaaa',marks:[link]}]), 3), unchanged = anchor(doc,'rich-text',1,4,'unchanged',0,3);
 assert.deepEqual(patch(doc,{op:'replaceText',id:'rich-text',text:'aaaaa',range:{from:0,to:1}},3),{from:1,to:1,insert:'a'});
 assert.deepEqual(json(validate(doc,3)),paragraph([{type:'text',text:'a',marks:[link]},{type:'text',text:'a'},{type:'text',text:'aaa',marks:[link]}]));
 assert.equal(resolve(doc,unchanged,3).status,'attached'); assert.equal(resolve(doc,unchanged,3).from,2); assert.equal(resolve(doc,unchanged,3).to,5); doc.destroy();
});
