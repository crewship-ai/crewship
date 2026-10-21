import test from 'node:test';
import assert from 'node:assert/strict';
import { unzipSync, strFromU8 } from 'fflate';
import { validateJSON } from '../../tools/docs-service/schema.mjs';
import { createDocumentDOCXExporter, DocxExportError } from './docx-export.mjs';

const exportDOCX = createDocumentDOCXExporter(validateJSON);
const text = (value, marks) => ({ type: 'text', text: value, ...(marks ? { marks } : {}) });
const p = (id, value = []) => ({ type: 'paragraph', attrs: { id }, content: typeof value === 'string' ? [text(value)] : value });
const doc = (...content) => ({ type: 'doc', content });
const parts = result => Object.fromEntries(Object.entries(unzipSync(result.bytes)).map(([name, bytes]) => [name, strFromU8(bytes)]));
const failure = (content, schema, code) => assert.throws(() => exportDOCX(content, schema), error => error instanceof DocxExportError && error.code === code && !('bytes' in error));

test('all accepted legacy versions and rich schema emit editable OOXML with bounded package relationships', () => {
  for (const schema of [1, 2, 3]) {
    const result = exportDOCX(doc(p('p', 'Příliš žluťoučký 🐎')), schema), files = parts(result);
    assert.equal(result.report.qualification, 'unqualified');
    assert.equal(result.report.schema, schema);
    assert.ok(files['[Content_Types].xml'].includes('wordprocessingml.document.main+xml'));
    assert.ok(files['_rels/.rels'].includes('Target="word/document.xml"'));
    assert.ok(files['word/document.xml'].includes('Příliš žluťoučký 🐎'));
    assert.ok(files['word/document.xml'].includes('<w:p>'));
    assert.ok(files['word/styles.xml']);
    assert.equal(Object.keys(files).some(name => /media|vba|embeddings|externalLink/.test(name)), false);
  }
});

test('LF, CR, tabs, repeated spaces and XML-sensitive text remain literal without raw markup', () => {
  const value = '  a<&"\'  \tline\nnext\r\nend';
  const xml = parts(exportDOCX(doc(p('p', value)), 3))['word/document.xml'];
  assert.ok(xml.includes('xml:space="preserve"'));
  assert.ok(xml.includes('&lt;&amp;'));
  assert.ok(xml.includes('<w:tab/>'));
  assert.equal((xml.match(/<w:br\/>/g) ?? []).length, 2);
  assert.equal(xml.includes('<&'), false);
});

test('native heading/list numbering and table cells keep structure including ordered start', () => {
  const heading = { type: 'heading', attrs: { id: 'h', level: 2 }, content: [text('Heading')] };
  const list = { type: 'orderedList', attrs: { id: 'ol', start: 7, type: null }, content: [
    { type: 'listItem', attrs: { id: 'li' }, content: [p('lp', 'Numbered'), { type: 'bulletList', attrs: { id: 'ul' }, content: [{ type: 'listItem', attrs: { id: 'bli' }, content: [p('bp', 'Bullet')] }] }] },
  ] };
  const cell = (type, id, value) => ({ type, attrs: { id, colspan: 1, rowspan: 1, colwidth: null, align: null }, content: [p(`${id}-p`, value)] });
  const table = { type: 'table', attrs: { id: 't' }, content: [{ type: 'tableRow', attrs: { id: 'tr' }, content: [cell('tableHeader', 'th', 'Header'), cell('tableCell', 'td', 'Cell')] }] };
  const files = parts(exportDOCX(doc(heading, list, table), 3));
  assert.ok(files['word/document.xml'].includes('w:val="Heading2"'));
  assert.ok(files['word/document.xml'].includes('<w:numPr>'));
  assert.ok(files['word/document.xml'].includes('<w:tbl>'));
  assert.equal((files['word/document.xml'].match(/<w:tc>/g) ?? []).length, 2);
  assert.ok(files['word/numbering.xml'].includes('w:start w:val="7"'));
  assert.ok(files['word/numbering.xml'].includes('w:val="bullet"'));
  assert.ok(files['word/numbering.xml'].lastIndexOf('</w:abstractNum>') < files['word/numbering.xml'].indexOf('<w:num w:numId='), 'OOXML abstract definitions precede all numbering instances');
});

test('schema3 links are external relationships and code stays native literal text', () => {
  const link = { type: 'link', attrs: { href: 'https://example.invalid/?x=1&y=2' } };
  const content = doc(p('p', [text('link', [link]), text('code', [{ type: 'code', attrs: {} }])]), { type: 'codeBlock', attrs: { id: 'c' }, content: [text('<script>\ncode')] });
  const files = parts(exportDOCX(content, 3));
  assert.ok(files['word/document.xml'].includes('<w:hyperlink r:id="'));
  assert.ok(files['word/_rels/document.xml.rels'].includes('TargetMode="External"'));
  assert.ok(files['word/_rels/document.xml.rels'].includes('?x=1&amp;y=2'));
  assert.ok(files['word/document.xml'].includes('w:val="CodeBlock"'));
  assert.ok(files['word/document.xml'].includes('w:val="InlineCode"'));
  failure(content, 2, 'invalid-content');
});

test('unknown schemas, attrs, marks, images and invalid structure never produce bytes', () => {
  failure(doc(p('p', 'text')), 99, 'unsupported-schema');
  for (const content of [doc({ type: 'image', attrs: { id: 'i' } }), doc({ ...p('p', 'text'), attrs: { id: 'p', future: true } }), doc(p('p', [text('x', [{ type: 'bold' }])])), doc({ type: 'paragraph', attrs: { id: 'p' }, content: [p('nested', 'bad')] })]) failure(content, 3, 'invalid-content');
});

test('XML-invalid characters and surrogates fail explicitly without replacement or truncation', () => {
  for (const value of ['a\u0000b', '\u0001', '\ud800', '\udfff', '\ufffe']) failure(doc(p('p', value)), 3, 'unmappable-content');
});

test('missing, async or normalizing validators cannot bypass the converter closure', () => {
  assert.throws(() => createDocumentDOCXExporter(), TypeError);
  assert.throws(() => createDocumentDOCXExporter(null), TypeError);
  const permissive = createDocumentDOCXExporter(() => true);
  assert.throws(() => permissive(doc({ ...p('p', 'x'), attrs: { id: 'p', future: 1 } }), 3), DocxExportError);
  const asyncValidator = createDocumentDOCXExporter(async value => value);
  assert.throws(() => asyncValidator(doc(p('p', 'x')), 3), DocxExportError);
  const normalizer = createDocumentDOCXExporter(value => { value.content[0].content[0].text = 'changed'; return value; });
  const original = doc(p('p', 'original'));
  assert.throws(() => normalizer(original, 3), DocxExportError);
  assert.equal(original.content[0].content[0].text, 'original');
});

test('input is untouched and repeat conversions are deterministic', () => {
  const input = doc(p('p', 'stable')), before = JSON.stringify(input);
  assert.deepEqual(exportDOCX(input, 3).bytes, exportDOCX(input, 3).bytes);
  assert.equal(JSON.stringify(input), before);
});

test('unmappable list depth and ragged tables fail with a path and no partial package', () => {
  let list = { type: 'bulletList', attrs: { id: 'list-0' }, content: [{ type: 'listItem', attrs: { id: 'item-0' }, content: [p('p-0', 'deep')] }] };
  for (let i = 1; i <= 9; i++) list = { type: 'bulletList', attrs: { id: `list-${i}` }, content: [{ type: 'listItem', attrs: { id: `item-${i}` }, content: [p(`p-${i}`, 'deep'), list] }] };
  // The engine depth limit can reject before the DOCX-specific nine-level limit.
  assert.throws(() => exportDOCX(doc(list), 3), error => error instanceof DocxExportError && error.code === 'unmappable-content' && error.path.startsWith('$.content'));
  const cell = id => ({ type: 'tableCell', attrs: { id }, content: [p(`${id}-p`, 'x')] });
  const ragged = doc({ type: 'table', attrs: { id: 'table' }, content: [{ type: 'tableRow', attrs: { id: 'row-1' }, content: [cell('a')] }, { type: 'tableRow', attrs: { id: 'row-2' }, content: [cell('b'), cell('c')] }] });
  failure(ragged, 3, 'unmappable-content');
});


test('nested native table cells retain the required final paragraph', () => {
  const cell = (id, content) => ({ type: 'tableCell', attrs: { id }, content });
  const table = (id, content) => ({ type: 'table', attrs: { id }, content: [{ type: 'tableRow', attrs: { id: `${id}-row` }, content }] });
  const nested = table('inner', [cell('inner-cell', [p('inner-text', 'Nested')])]);
  const outer = table('outer', [cell('outer-cell', [p('outer-text', 'Before'), nested])]);
  const xml = parts(exportDOCX(doc(outer), 3))['word/document.xml'];
  assert.equal((xml.match(/<w:tbl>/g) ?? []).length, 2);
  assert.ok(xml.includes('</w:tbl><w:p/></w:tc>'));
});
