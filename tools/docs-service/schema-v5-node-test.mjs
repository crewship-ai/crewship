import test from 'node:test';
import assert from 'node:assert/strict';
import { create, validate, validateJSON, load, snapshot, outline, hash, patch, anchor, resolve, markdown, find, project, PALETTE, LATEST_VERSION, FORMATTED_VERSION } from './schema.mjs';
import * as Y from 'yjs';

const json = value => JSON.parse(JSON.stringify(value));
const p = (id, content) => ({ type: 'paragraph', attrs: { id }, ...(content ? { content } : {}) });
const t = (text, ...marks) => ({ type: 'text', text, ...(marks.length ? { marks } : {}) });
const bold = { type: 'bold', attrs: {} }, italic = { type: 'italic', attrs: {} }, strike = { type: 'strike', attrs: {} }, code = { type: 'code', attrs: {} };
const underline = { type: 'underline', attrs: {} };
const color = name => ({ type: 'textColor', attrs: { color: name } });
const highlight = name => ({ type: 'highlight', attrs: { color: name } });
const link = { type: 'link', attrs: { href: 'https://example.invalid/' } };
// Marks are listed in the schema's rank order, as the projection returns them.
const formatted = { type: 'doc', content: [
  { type: 'heading', attrs: { id: 'h3', level: 3 }, content: [t('Third level', underline)] },
  p('marks', [t('Plain '), t('under', underline), t(', '), t('red', color('red')), t(', '), t('marked', highlight('yellow')), t(', '),
    t('all', link, bold, italic, strike, underline, color('blue'), highlight('green')), t(' and '), t('crew ls', code)]),
  { type: 'taskList', attrs: { id: 'tasks' }, content: [
    { type: 'taskItem', attrs: { id: 'done', checked: true }, content: [p('done-p', [t('Done', highlight('gray'))])] },
  ] },
] };

test('schema5 is the latest schema and its palette is a closed list of names', () => {
  assert.equal(LATEST_VERSION, 5); assert.equal(FORMATTED_VERSION, 4);
  assert.deepEqual([...PALETTE], ['gray', 'red', 'orange', 'yellow', 'green', 'blue', 'purple', 'pink']);
  assert.ok(Object.isFrozen(PALETTE));
});

test('schema5 round-trips underline, text colour and highlight through a Yjs snapshot reload', async () => {
  const doc = create(formatted, 5);
  const reloaded = load(snapshot(doc));
  assert.deepEqual(json(validate(reloaded, 5)), formatted);
  const blocks = await outline(reloaded, 5);
  assert.deepEqual(blocks.map(b => b.type), ['heading', 'paragraph', 'taskList', 'taskItem', 'paragraph']);
  doc.destroy(); reloaded.destroy();
});

test('schema5 every palette colour is accepted for text and highlight', () => {
  for (const name of PALETTE) create({ type: 'doc', content: [p('x', [t('x', color(name), highlight(name))])] }, 5).destroy();
});

test('schema5 content is refused by schemas 1-4; schema 4 content keeps its exact projection, hashes, bytes and Markdown at 5', async () => {
  for (const version of [1, 2, 3, 4]) assert.throws(() => create(formatted, version), String(version));
  for (const mark of [underline, color('red'), highlight('yellow')]) assert.throws(() => create({ type: 'doc', content: [p('x', [t('x', mark)])] }, 4));
  const v4 = { type: 'doc', content: [
    p('a', [t('prefix '), t('both', link, bold, italic), t(' ~ '), t('struck', strike), t(' and '), t('code', code)]),
    { type: 'codeBlock', attrs: { id: 'cb', language: 'go' }, content: [t('literal')] },
    { type: 'blockquote', attrs: { id: 'q' }, content: [p('qp', [t('quoted', italic)])] },
    { type: 'taskList', attrs: { id: 'l' }, content: [{ type: 'taskItem', attrs: { id: 'i', checked: false }, content: [p('ip', [t('todo')])] }] },
  ] };
  const doc = create(v4, 4), bytes = snapshot(doc);
  assert.deepEqual(validate(doc, 5), validate(doc, 4));
  assert.deepEqual((await outline(doc, 5)).map(b => [b.id, b.hash]), (await outline(doc, 4)).map(b => [b.id, b.hash]));
  assert.equal(snapshot(doc), bytes);
  assert.equal(markdown(validate(doc, 4), 4), markdown(validate(doc, 5), 5));
  doc.destroy();
});

test('schema5 rejects free CSS, unknown colours, extra attributes and inline code combinations', () => {
  for (const value of ['#ff0000', 'Red', 'red;background:url(x)', 'var(--x)', '', 'rgb(1,2,3)', null, 7, ['red'], { name: 'red' }, 'transparent', 'default']) {
    for (const type of ['textColor', 'highlight']) {
      assert.throws(() => create({ type: 'doc', content: [p('x', [t('x', { type, attrs: { color: value } })])] }, 5), `${type} ${JSON.stringify(value)}`);
    }
  }
  for (const marks of [
    [{ type: 'textColor', attrs: {} }], [{ type: 'highlight' }], [{ type: 'textColor', attrs: { color: 'red', style: 'x' } }],
    [{ type: 'underline', attrs: { style: 'wavy' } }], [code, underline], [code, color('red')], [highlight('red'), code],
    [underline, underline], [{ type: 'color', attrs: { color: 'red' } }], [{ type: 'textStyle', attrs: { color: 'red' } }],
  ]) {
    assert.throws(() => create({ type: 'doc', content: [p('x', [t('x', ...marks)])] }, 5), JSON.stringify(marks));
  }
  assert.throws(() => create({ type: 'doc', content: [{ type: 'codeBlock', attrs: { id: 'c' }, content: [t('x', underline)] }] }, 5));
  assert.throws(() => create({ type: 'doc', content: [{ type: 'paragraph', attrs: { id: 'x', style: 'color:red' }, content: [t('x')] }] }, 5));
});

test('schema5 binary validation refuses retained unsafe colours and formats, including removed ones', () => {
  const attempts = [
    doc => find(doc, 'done-p').toArray()[0].format(0, 2, { textColor: { color: '#f00' } }),
    doc => find(doc, 'done-p').toArray()[0].format(0, 2, { highlight: { color: 'red', css: 'x' } }),
    doc => find(doc, 'done-p').toArray()[0].format(0, 2, { highlight: 'yellow' }),
    doc => find(doc, 'done-p').toArray()[0].format(0, 2, { underline: { style: 'wavy' } }),
    doc => find(doc, 'done-p').toArray()[0].format(0, 2, { code: {}, underline: {} }),
    doc => find(doc, 'done-p').toArray()[0].format(0, 2, { textStyle: { color: 'red' } }),
    // A colour that was set and then removed still remains in Yjs history.
    doc => { const text = find(doc, 'done-p').toArray()[0]; text.format(0, 2, { textColor: { color: 'url(x)' } }); text.format(0, 2, { textColor: null }); },
    doc => find(doc, 'h3').setAttribute('style', 'color:red'),
  ];
  for (const attempt of attempts) {
    const doc = create(formatted, 5); attempt(doc);
    assert.throws(() => validate(load(snapshot(doc)), 5));
    doc.destroy();
  }
  // Changing and removing a palette colour keeps valid history.
  const doc = create(formatted, 5), text = find(doc, 'done-p').toArray()[0];
  text.format(0, 4, { highlight: { color: 'pink' } }); text.format(0, 4, { highlight: null }); text.format(0, 2, { textColor: { color: 'purple' } });
  assert.deepEqual(json(validate(load(snapshot(doc)), 5)).content[2].content[0].content[0].content, [t('Do', color('purple')), t('ne')]);
  doc.destroy();
});

test('schema5 projection orders marks deterministically regardless of Yjs insertion order', async () => {
  const one = create({ type: 'doc', content: [p('x', [t('word')])] }, 5), two = create({ type: 'doc', content: [p('x', [t('word')])] }, 5);
  const apply = (doc, order) => { for (const attrs of order) find(doc, 'x').toArray()[0].format(0, 4, attrs); };
  const a = { highlight: { color: 'red' } }, b = { underline: {} }, c = { bold: {} }, d = { textColor: { color: 'blue' } };
  apply(one, [a, b, c, d]); apply(two, [d, c, b, a]);
  assert.deepEqual(validate(one, 5), validate(two, 5));
  assert.equal((await outline(one, 5))[0].hash, (await outline(two, 5))[0].hash);
  assert.deepEqual(project(one).content[0].content[0].marks.map(m => m.type), ['bold', 'underline', 'textColor', 'highlight']);
  one.destroy(); two.destroy();
});

test('schema5 semantic replacement keeps neighbouring formatting and comments stay anchored', () => {
  const doc = create(formatted, 5);
  const thread = anchor(doc, 'marks', 6, 11, 'under-thread', 0, 5);
  patch(doc, { op: 'replaceText', id: 'marks', text: 'Plain under!, red, marked, all and crew ls' }, 5);
  const marks = json(validate(doc, 5)).content[1].content;
  assert.deepEqual(marks.slice(1, 3), [t('under', underline), t('!, ')]);
  assert.equal(resolve(doc, thread, 5).status, 'attached'); assert.equal(resolve(doc, thread, 5).quote, 'under');
  doc.destroy();
});

test('schema5 Markdown writes underline, colour and highlight as closed inline HTML around emphasis', () => {
  assert.equal(markdown(formatted, 5), [
    '### <u>Third level</u>', '',
    'Plain <u>under</u>, <span data-text-color="red">red</span>, <mark data-highlight="yellow">marked</mark>, ' +
      '[<span data-text-color="blue"><mark data-highlight="green"><u>***~~all~~***</u></mark></span>](<https://example.invalid/>) and `crew ls`', '',
    '- [x] <mark data-highlight="gray">Done</mark>', '',
  ].join('\n'));
  assert.equal(markdown({ type: 'doc', content: [p('a', [t(' spaced ', underline)])] }, 5), ' <u>spaced</u>\n');
  // Inside a word the emphasis delimiters still touch text, so they stay flanking.
  assert.equal(markdown({ type: 'doc', content: [p('a', [t('abc'), t('def', bold, underline), t('ghi')])] }, 5), 'abc<u>**def**</u>ghi\n');
  // Literal angle brackets stay escaped text, never markup.
  assert.equal(markdown({ type: 'doc', content: [p('a', [t('<u>x</u>', color('red'))])] }, 5), '<span data-text-color="red">\\<u\\>x\\</u\\></span>\n');
  const table = { type: 'doc', content: [{ type: 'table', attrs: { id: 'tb' }, content: [{ type: 'tableRow', attrs: { id: 'r' }, content: [{ type: 'tableCell', attrs: { id: 'c' }, content: [p('cp', [t('a|b', underline)])] }] }] }] };
  assert.equal(markdown(table, 5), '| <u>a\\|b</u> |\n| --- |\n');
});

test('schema5 hashes are canonical JSON hashes of the projected subtree', async () => {
  const doc = create(formatted, 5), blocks = await outline(doc, 5), value = validate(doc, 5);
  assert.equal(blocks.find(b => b.id === 'marks').hash, await hash(value.content[1]));
  assert.ok(Y.encodeStateAsUpdate(doc).length > 0); doc.destroy();
  validateJSON(formatted, 5);
});
