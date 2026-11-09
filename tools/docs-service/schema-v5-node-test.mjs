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

// D06: structured inline dates and mentions (schema 5).
const date = value => ({ type: 'date', attrs: { value } });
const mention = userId => ({ type: 'mention', attrs: { userId } });
const atoms = { type: 'doc', content: [
  p('meet', [t('Review on '), date('2026-11-20'), t(' with '), mention('cmw8i35310007a069c4b7'), t(' and '), mention('user_2-B')]),
  { type: 'heading', attrs: { id: 'hd', level: 2 }, content: [date('2027-02-28')] },
  { type: 'table', attrs: { id: 'tb' }, content: [{ type: 'tableRow', attrs: { id: 'r' }, content: [{ type: 'tableCell', attrs: { id: 'c', colspan: 1, rowspan: 1 }, content: [p('cp', [t('a|b '), mention('u1')])] }] }] },
] };

test('schema5 dates and mentions round-trip through a Yjs snapshot reload as closed atoms', async () => {
  const doc = create(atoms, 5), reloaded = load(snapshot(doc));
  assert.deepEqual(json(validate(reloaded, 5)), atoms);
  const blocks = await outline(reloaded, 5);
  assert.equal(blocks.find(b => b.id === 'meet').text, 'Review on  with  and ', 'atoms carry no text, so agent text offsets stay text-only');
  assert.equal(blocks.find(b => b.id === 'meet').hash, await hash(atoms.content[0]));
  doc.destroy(); reloaded.destroy();
});

test('schema5 atoms are refused by schemas 1-4 and carry exactly one valid value', () => {
  for (const version of [1, 2, 3, 4]) assert.throws(() => create(atoms, version), String(version));
  for (const bad of ['2026-02-30', '2026-2-03', '2026-11-20T10:00', '20261120', '0000-01-01', ' 2026-11-20', '', null, 20261120, ['2026-11-20']]) {
    assert.throws(() => create({ type: 'doc', content: [p('x', [date(bad)])] }, 5), `date ${JSON.stringify(bad)}`);
  }
  // Every calendar day from year 1 is a date, including years 1-99 and leap days.
  for (const good of ['0001-01-01', '0099-12-31', '2024-02-29', '9999-12-31']) assert.equal(validateJSON({ type: 'doc', content: [p('x', [date(good)])] }, 5).content[0].content[0].attrs.value, good)
  assert.throws(() => create({ type: 'doc', content: [p('x', [date('2025-02-29')])] }, 5))
  for (const bad of ['', 'a b', 'x'.repeat(65), 'ada@example.invalid', '../u', null, 7, { id: 'u' }]) {
    assert.throws(() => create({ type: 'doc', content: [p('x', [mention(bad)])] }, 5), `mention ${JSON.stringify(bad)}`);
  }
  for (const node of [
    { type: 'date', attrs: { value: '2026-11-20', label: 'Friday' } }, { type: 'mention', attrs: { userId: 'u1', name: 'Ada' } },
    { type: 'mention', attrs: { userId: 'u1', id: 'm1' } }, { type: 'date' }, { type: 'date', attrs: {} },
    { type: 'date', attrs: { value: '2026-11-20' }, content: [t('x')] }, { type: 'mention', attrs: { userId: 'u1' }, text: 'Ada' },
    { type: 'mention', attrs: { userId: 'u1' }, marks: [bold] },
  ]) assert.throws(() => create({ type: 'doc', content: [p('x', [node])] }, 5), JSON.stringify(node));
  // Atoms are inline only: never a block, never inside a code block.
  assert.throws(() => create({ type: 'doc', content: [date('2026-11-20')] }, 5));
  assert.throws(() => create({ type: 'doc', content: [{ type: 'codeBlock', attrs: { id: 'cb' }, content: [mention('u1')] }] }, 5));
  // Leap day exists in 2028, not in 2027.
  create({ type: 'doc', content: [p('x', [date('2028-02-29')])] }, 5).destroy();
  assert.throws(() => create({ type: 'doc', content: [p('x', [date('2027-02-29')])] }, 5));
});

test('schema5 binary validation refuses atom children, extra or invalid attributes, including retained ones', () => {
  const atomIn = (doc, index) => find(doc, 'meet').toArray().filter(n => n instanceof Y.XmlElement)[index];
  const attempts = [
    doc => atomIn(doc, 0).setAttribute('label', 'Friday'),
    doc => atomIn(doc, 1).setAttribute('name', 'Ada Owner'),
    doc => atomIn(doc, 1).setAttribute('userId', 'ada@example.invalid'),
    doc => atomIn(doc, 0).setAttribute('value', '2026-13-01'),
    doc => atomIn(doc, 0).insert(0, [new Y.XmlText('leak')]),
    doc => { const el = atomIn(doc, 1); el.setAttribute('name', 'Ada'); el.removeAttribute('name'); },
    doc => { const el = atomIn(doc, 0); el.insert(0, [new Y.XmlText('x')]); el.delete(0, 1); },
    doc => { const el = atomIn(doc, 1); el.setAttribute('userId', 'bad id'); el.setAttribute('userId', 'u9'); },
    doc => find(doc, 'meet').insert(0, [new Y.XmlElement('person')]),
  ];
  for (const attempt of attempts) {
    const doc = create(atoms, 5); attempt(doc);
    assert.throws(() => validate(load(snapshot(doc)), 5), attempt.toString());
    doc.destroy();
  }
  // Changing a date or a mention target to another valid value keeps valid history.
  const doc = create(atoms, 5);
  atomIn(doc, 0).setAttribute('value', '2026-12-01'); atomIn(doc, 1).setAttribute('userId', 'u9');
  const value = json(validate(load(snapshot(doc)), 5)).content[0].content;
  assert.deepEqual([value[1], value[3]], [date('2026-12-01'), mention('u9')]);
  doc.destroy();
});

test('schema5 semantic replacement and comments keep dates and mentions in place', () => {
  const doc = create(atoms, 5);
  const thread = anchor(doc, 'meet', 0, 6, 'review-thread', 0, 5);
  patch(doc, { op: 'replaceText', id: 'meet', text: 'Review on  with  and ' }, 5);
  patch(doc, { op: 'replaceText', id: 'meet', text: 'Final review on  with  and ', range: { from: 0, to: 6 } }, 5);
  const content = json(validate(doc, 5)).content[0].content;
  assert.deepEqual(content.filter(n => n.type !== 'text'), [date('2026-11-20'), mention('cmw8i35310007a069c4b7'), mention('user_2-B')]);
  assert.equal(resolve(doc, thread, 5).status, 'attached');
  doc.destroy();
});

test('schema5 semantic replacement refuses edits whose side of a date or mention would be a guess', () => {
  const doc = create(atoms, 5);
  const before = json(validate(doc, 5));
  const refused = text => assert.throws(() => patch(doc, { op: 'replaceText', id: 'meet', text }, 5), /date or mention/);
  refused('Review on  with  and  later'); // appended after the trailing mention
  refused('Review on X with  and ');      // inserted exactly before the date
  refused('Review onwith  and ');         // a range across the date (the spaces on both sides)
  assert.deepEqual(json(validate(doc, 5)), before);
  // A change that could slide across an atom is refused even when one side was picked by the diff.
  const sliding = create({ type: 'doc', content: [p('s', [t('aa'), mention('m1'), t('aa')]), p('d', [t('a'), mention('m2'), t('a')])] }, 5);
  assert.throws(() => patch(sliding, { op: 'replaceText', id: 's', text: 'aaaaa' }, 5), /date or mention/);
  assert.throws(() => patch(sliding, { op: 'replaceText', id: 'd', text: 'a' }, 5), /date or mention/);
  patch(sliding, { op: 'replaceText', id: 's', text: 'aaab' }, 5);
  assert.deepEqual(json(validate(sliding, 5)).content[0].content.map(n => n.text ?? n.type), ['aa', 'mention', 'ab']);
  sliding.destroy();
  // Edits inside text, or deleting up to an atom without inserting there, still apply.
  patch(doc, { op: 'replaceText', id: 'meet', text: 'Review on  wit  and ' }, 5);
  patch(doc, { op: 'replaceText', id: 'meet', text: 'Revie on  wit  and ' }, 5);
  const content = json(validate(doc, 5)).content[0].content;
  assert.deepEqual(content.map(n => n.type === 'text' ? n.text : n.type), ['Revie on ', 'date', ' wit ', 'mention', ' and ', 'mention']);
  doc.destroy();
});

test('schema5 Markdown writes dates as ISO text and mentions by the name the reader may see, otherwise neutrally', () => {
  assert.equal(markdown(atoms, 5), ['Review on 2026-11-20 with @Unknown member and @Unknown member', '', '## 2027-02-28', '', '| a\\|b @Unknown member |', '| --- |', ''].join('\n'));
  const people = { cmw8i35310007a069c4b7: 'Ada *Owner*', u1: 'Cell | Person' };
  assert.equal(markdown(atoms, 5, { people }), ['Review on 2026-11-20 with @Ada \\*Owner\\* and @Unknown member', '', '## 2027-02-28', '', '| a\\|b @Cell \\| Person |', '| --- |', ''].join('\n'));
  // Only own keys count; a prototype key never names anyone.
  assert.equal(markdown({ type: 'doc', content: [p('x', [mention('toString')])] }, 5, { people: {} }), '@Unknown member\n');
});
