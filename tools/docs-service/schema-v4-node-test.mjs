import test from 'node:test';
import assert from 'node:assert/strict';
import { create, validate, validateJSON, load, snapshot, outline, hash, patch, anchor, resolve, markdown, find, project } from './schema.mjs';
import * as Y from 'yjs';

const json = value => JSON.parse(JSON.stringify(value));
const p = (id, content) => ({ type: 'paragraph', attrs: { id }, ...(content ? { content } : {}) });
const t = (text, ...marks) => ({ type: 'text', text, ...(marks.length ? { marks } : {}) });
const bold = { type: 'bold', attrs: {} }, italic = { type: 'italic', attrs: {} }, strike = { type: 'strike', attrs: {} }, code = { type: 'code', attrs: {} };
const link = { type: 'link', attrs: { href: 'https://example.invalid/' } };
const formatted = { type: 'doc', content: [
  p('marks', [t('Text '), t('bold', bold), t(', '), t('italic', italic), t(', '), t('struck', strike), t(', '), t('both', link, bold, italic), t(' and '), t('crew ls', code)]),
  { type: 'codeBlock', attrs: { id: 'py', language: 'python' }, content: [t('print("hi")\n\tpass')] },
  { type: 'codeBlock', attrs: { id: 'plain' }, content: [t('no language')] },
  { type: 'blockquote', attrs: { id: 'quote' }, content: [p('quote-p', [t('Quoted ', italic), t('text')])] },
  { type: 'taskList', attrs: { id: 'tasks' }, content: [
    { type: 'taskItem', attrs: { id: 'done', checked: true }, content: [p('done-p', [t('Done')])] },
    { type: 'taskItem', attrs: { id: 'open', checked: false }, content: [p('open-p', [t('Open')])] },
  ] },
] };

test('schema4 round-trips marks, code languages, quotes and task lists through Yjs', async () => {
  const doc = create(formatted, 4);
  const reloaded = load(snapshot(doc));
  const value = json(validate(reloaded, 4));
  // A code block without a language projects without the attribute, exactly as stored.
  assert.deepEqual(value, formatted);
  const blocks = await outline(reloaded, 4);
  assert.deepEqual(blocks.map(b => b.type), ['paragraph', 'codeBlock', 'codeBlock', 'blockquote', 'paragraph', 'taskList', 'taskItem', 'paragraph', 'taskItem', 'paragraph']);
  assert.equal(blocks.find(b => b.id === 'quote-p').parent, 'quote');
  doc.destroy(); reloaded.destroy();
});

test('schema4 content is refused by schemas 1-3, and schema 3 content keeps its exact projection and hashes at 4', async () => {
  for (const version of [1, 2, 3]) assert.throws(() => create(formatted, version));
  for (const block of [{ type: 'codeBlock', attrs: { id: 'c', language: 'go' }, content: [t('x')] }, { type: 'blockquote', attrs: { id: 'q' }, content: [p('qp')] }]) {
    assert.throws(() => create({ type: 'doc', content: [block] }, 3));
  }
  assert.throws(() => create({ type: 'doc', content: [p('b', [t('x', bold)])] }, 3));
  const v3 = { type: 'doc', content: [p('a', [t('prefix '), t('linked', link), t(' and '), t('code', code)]), { type: 'codeBlock', attrs: { id: 'cb' }, content: [t('literal')] }] };
  const doc = create(v3, 3), bytes = snapshot(doc);
  assert.deepEqual(validate(doc, 4), validate(doc, 3));
  assert.deepEqual((await outline(doc, 4)).map(b => [b.id, b.hash]), (await outline(doc, 3)).map(b => [b.id, b.hash]));
  assert.equal(snapshot(doc), bytes);
  assert.equal(markdown(validate(doc, 3), 3), markdown(validate(doc, 4), 4));
  doc.destroy();
});

test('schema4 rejects unsafe code block languages, task states and mark combinations', () => {
  for (const language of ['Python', 'py thon', '<script>', 'a'.repeat(33), '', 'js"onload', '-x', 7, ['go'], { a: 1 }]) {
    const value = structuredClone(formatted); value.content[1].attrs.language = language;
    assert.throws(() => create(value, 4), String(language));
  }
  for (const language of ['c++', 'c#', 'objective-c', 'go', 'latex', '1c']) {
    const value = structuredClone(formatted); value.content[1].attrs.language = language;
    create(value, 4).destroy();
  }
  for (const checked of ['true', 1, null]) {
    const value = structuredClone(formatted); value.content[4].content[0].attrs.checked = checked; assert.throws(() => create(value, 4));
  }
  for (const marks of [[code, bold], [link, code], [bold, bold], [{ type: 'underline', attrs: {} }], [{ type: 'bold', attrs: { level: 1 } }]]) {
    assert.throws(() => create({ type: 'doc', content: [p('x', [t('x', ...marks)])] }, 4), JSON.stringify(marks));
  }
  assert.throws(() => create({ type: 'doc', content: [{ type: 'codeBlock', attrs: { id: 'c' }, content: [t('x', bold)] }] }, 4));
  assert.throws(() => create({ type: 'doc', content: [{ type: 'taskList', attrs: { id: 'l' }, content: [{ type: 'listItem', attrs: { id: 'i' }, content: [p('ip')] }] }] }, 4));
});

test('schema4 binary validation refuses retained unsafe attributes and formats', () => {
  const attempts = [
    doc => find(doc, 'py').setAttribute('language', '<script>'),
    doc => find(doc, 'py').setAttribute('onclick', 'x'),
    doc => find(doc, 'done').setAttribute('checked', 'yes'),
    doc => { const text = find(doc, 'open-p').toArray()[0]; text.format(0, 2, { underline: {} }); },
    doc => { const text = find(doc, 'open-p').toArray()[0]; text.format(0, 2, { code: {}, bold: {} }); },
    doc => { const text = find(doc, 'py').toArray()[0]; text.format(0, 2, { bold: {} }); },
  ];
  for (const attempt of attempts) {
    const doc = create(formatted, 4); attempt(doc);
    assert.throws(() => validate(load(snapshot(doc)), 4));
    doc.destroy();
  }
  // A removed language is retained Yjs history and must still have been valid.
  const doc = create(formatted, 4); find(doc, 'py').setAttribute('language', 'go'); find(doc, 'py').setAttribute('language', 'rust');
  assert.equal(json(validate(load(snapshot(doc)), 4)).content[1].attrs.language, 'rust'); doc.destroy();
});

test('schema4 projection orders several marks deterministically regardless of Yjs insertion order', async () => {
  const one = create({ type: 'doc', content: [p('x', [t('word')])] }, 4), two = create({ type: 'doc', content: [p('x', [t('word')])] }, 4);
  find(one, 'x').toArray()[0].format(0, 4, { italic: {} }); find(one, 'x').toArray()[0].format(0, 4, { bold: {} });
  find(two, 'x').toArray()[0].format(0, 4, { bold: {} }); find(two, 'x').toArray()[0].format(0, 4, { italic: {} });
  assert.deepEqual(validate(one, 4), validate(two, 4));
  assert.equal((await outline(one, 4))[0].hash, (await outline(two, 4))[0].hash);
  assert.deepEqual(project(one).content[0].content[0].marks.map(m => m.type), ['bold', 'italic']);
  one.destroy(); two.destroy();
});

test('schema4 semantic replacement inserts plain text and comments stay anchored in quotes and task items', () => {
  const doc = create(formatted, 4);
  const thread = anchor(doc, 'done-p', 0, 4, 'task-thread', 0, 4), quoted = anchor(doc, 'quote-p', 0, 6, 'quote-thread', 0, 4);
  patch(doc, { op: 'replaceText', id: 'marks', text: 'Text boldER, italic, struck, both and crew ls' }, 4);
  const marks = json(validate(doc, 4)).content[0].content;
  assert.deepEqual(marks.slice(1, 3), [t('bold', bold), t('ER, ')]);
  assert.equal(resolve(doc, thread, 4).status, 'attached'); assert.equal(resolve(doc, quoted, 4).quote, 'Quoted');
  doc.destroy();
});

test('schema4 Markdown exports emphasis, languages, quotes and checklists; schema3 output is unchanged', () => {
  assert.equal(markdown(formatted, 4), [
    'Text **bold**, *italic*, ~~struck~~, [***both***](<https://example.invalid/>) and `crew ls`',
    '', '```python', 'print("hi")\n\tpass', '```', '', '```', 'no language', '```', '', '> *Quoted* text', '', '- [x] Done', '- [ ] Open', '',
  ].join('\n'));
  assert.equal(markdown({ type: 'doc', content: [p('a', [t('a ~ b')])] }, 3), 'a ~ b\n');
  assert.equal(markdown({ type: 'doc', content: [p('a', [t('a ~ b')])] }, 4), 'a \\~ b\n');
  assert.equal(markdown({ type: 'doc', content: [p('a', [t(' spaced ', bold)])] }, 4), ' **spaced**\n');
  const quoteInTable = { type: 'doc', content: [{ type: 'table', attrs: { id: 'tb' }, content: [{ type: 'tableRow', attrs: { id: 'r' }, content: [{ type: 'tableCell', attrs: { id: 'c' }, content: [{ type: 'blockquote', attrs: { id: 'q' }, content: [p('qp', [t('x')])] }] }] }] }] };
  validateJSON(quoteInTable, 4);
  assert.throws(() => markdown(quoteInTable, 4), error => error.code === 'unsupported_markdown');
});

test('schema4 hashes are canonical JSON hashes of the projected subtree', async () => {
  const doc = create(formatted, 4), blocks = await outline(doc, 4), value = validate(doc, 4);
  assert.equal(blocks.find(b => b.id === 'py').hash, await hash(value.content[1]));
  assert.ok(Y.encodeStateAsUpdate(doc).length > 0); doc.destroy();
});
