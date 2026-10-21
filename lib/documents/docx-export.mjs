// Pure conversion of already authorized engine content; no storage or ACL authority.
import { zipSync, strToU8 } from 'fflate';

const W = 'http://schemas.openxmlformats.org/wordprocessingml/2006/main';
const R = 'http://schemas.openxmlformats.org/officeDocument/2006/relationships';
const REL = 'http://schemas.openxmlformats.org/package/2006/relationships';
const XML = '<?xml version="1.0" encoding="UTF-8" standalone="yes"?>';
const blockTypes = ['paragraph', 'heading', 'bulletList', 'orderedList', 'table', 'codeBlock'];
const nodeTypes = ['doc', 'text', ...blockTypes, 'listItem', 'tableRow', 'tableCell', 'tableHeader'];
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value) && Object.getPrototypeOf(value) === Object.prototype;

export class DocxExportError extends Error {
  constructor(code, path, message) { super(message); this.name = 'DocxExportError'; this.code = code; this.path = path; }
}
const fail = (code, path, message) => { throw new DocxExportError(code, path, message); };
const invalid = (path, message) => fail('invalid-content', path, message);
const unmappable = (path, message) => fail('unmappable-content', path, message);
const keys = (value, allowed, path) => { if (!object(value) || Object.keys(value).some(key => !allowed.includes(key))) invalid(path, 'Unsupported object fields'); };
function literal(value, path) {
  // XML 1.0 cannot represent these values; replacement would silently lose text.
  for (const character of value) {
    const code = character.codePointAt(0);
    if ((code < 32 && ![9, 10, 13].includes(code)) || (code >= 0xd800 && code <= 0xdfff) || [0xfffe, 0xffff].includes(code)) unmappable(path, 'Text is not representable in XML 1.0');
  }
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&apos;');
}
function linkHref(href, path) {
  if (typeof href !== 'string' || new TextEncoder().encode(href).length > 2048 || /[\u0000-\u0020\u007f\p{Cs}]/u.test(href)) invalid(path, 'Unsupported link');
  let url; try { url = new URL(href); } catch { invalid(path, 'Unsupported link'); }
  if (!['http:', 'https:'].includes(url.protocol) || !url.hostname || url.username || url.password || url.href !== href) invalid(path, 'Unsupported link');
  return href;
}
function checkContent(content, schema) {
  const ids = new Set(); let count = 0;
  const visit = (node, parent, path, depth) => {
    if (depth > 40 || ++count > 10000) invalid(path, 'Content budget exceeded');
    keys(node, ['type', 'attrs', 'content', 'text', 'marks'], path);
    if (!nodeTypes.includes(node.type) || (schema !== 3 && node.type === 'codeBlock')) invalid(path, 'Unsupported node');
    if (node.type === 'doc' && parent !== null) invalid(path, 'Nested document');
    if (node.type === 'text') {
      if (typeof node.text !== 'string' || !node.text || node.attrs !== undefined || node.content !== undefined) invalid(path, 'Invalid text node');
      literal(node.text, path);
      if (node.marks !== undefined && (!Array.isArray(node.marks) || node.marks.length > (schema === 3 ? 1 : 0))) invalid(path, 'Unsupported marks');
      for (const mark of node.marks ?? []) {
        keys(mark, ['type', 'attrs'], path);
        if (!['link', 'code'].includes(mark.type)) invalid(path, 'Unsupported mark');
        keys(mark.attrs ?? {}, mark.type === 'link' ? ['href'] : [], path);
        if (mark.attrs === null) invalid(path, 'Invalid mark attributes');
        if (mark.type === 'link') linkHref(mark.attrs?.href, path);
      }
      return;
    }
    if (node.text !== undefined || (node.marks !== undefined && (!Array.isArray(node.marks) || node.marks.length))) invalid(path, 'Invalid block properties');
    const attrs = node.attrs ?? {};
    const allowed = node.type === 'doc' ? [] : ['id', ...(node.type === 'heading' ? ['level'] : node.type === 'orderedList' ? ['start', 'type'] : ['tableCell', 'tableHeader'].includes(node.type) ? ['colspan', 'rowspan', 'colwidth', 'align'] : [])];
    keys(attrs, allowed, `${path}.attrs`);
    if (node.type !== 'doc') {
      if (typeof attrs.id !== 'string' || !/^[a-zA-Z0-9_-]{1,80}$/.test(attrs.id) || ids.has(attrs.id)) invalid(path, 'Invalid or duplicate block identity');
      ids.add(attrs.id);
    }
    if (node.type === 'heading' && ![1, 2, 3].includes(attrs.level)) invalid(path, 'Unsupported heading');
    if (node.type === 'orderedList' && (attrs.type != null || !Number.isInteger(attrs.start ?? 1) || (attrs.start ?? 1) < 1 || (attrs.start ?? 1) > 10000)) invalid(path, 'Unsupported list attributes');
    if (['tableCell', 'tableHeader'].includes(node.type) && ((attrs.colspan ?? 1) !== 1 || (attrs.rowspan ?? 1) !== 1 || attrs.colwidth != null || attrs.align != null)) invalid(path, 'Only simple table cells are supported');
    const children = node.content ?? [];
    if (!Array.isArray(children)) invalid(path, 'Invalid child collection');
    let childTypes;
    switch (node.type) {
      case 'paragraph': case 'heading': case 'codeBlock': childTypes = ['text']; break;
      case 'bulletList': case 'orderedList': childTypes = ['listItem']; break;
      case 'table': childTypes = ['tableRow']; break;
      case 'tableRow': childTypes = ['tableCell', 'tableHeader']; break;
      default: childTypes = blockTypes;
    }
    if (!['paragraph', 'heading', 'codeBlock'].includes(node.type) && !children.length) invalid(path, 'Empty structural node');
    if (node.type === 'listItem' && children[0]?.type !== 'paragraph') invalid(path, 'List item must begin with a paragraph');
    for (const [index, child] of children.entries()) {
      if (!childTypes.includes(child?.type)) invalid(`${path}.content[${index}]`, 'Invalid child type');
      if (node.type === 'codeBlock' && child.marks?.length) invalid(path, 'Code block cannot contain marks');
      visit(child, node.type, `${path}.content[${index}]`, depth + 1);
    }
    if (node.type === 'table' && children.some(row => row.content.length !== children[0].content.length)) unmappable(path, 'Ragged tables have no lossless simple-table mapping');
  };
  if (content?.type !== 'doc') invalid('$', 'Document root required');
  visit(content, null, '$', 0);
}

// The callback must be the trusted compiled engine's synchronous validateJSON.
// It is a required dependency, not a caller-controlled content authority.
export function createDocumentDOCXExporter(validateJSON) {
  if (typeof validateJSON !== 'function') throw new TypeError('Trusted compiled validateJSON callback required');
  return function exportDocumentDOCX(content, schema) {
    if (![1, 2, 3].includes(schema)) fail('unsupported-schema', '$', 'Unsupported Document schema');
    let copy, serialized;
    try {
      serialized = JSON.stringify(content);
      if (!serialized || serialized.length > 500000) invalid('$', 'Document size budget exceeded');
      copy = structuredClone(content);
      checkContent(copy, schema);
      const validated = validateJSON(copy, schema);
      if (validated !== copy || JSON.stringify(copy) !== serialized) invalid('$', 'Validator must return unchanged synchronous content');
    } catch (error) {
      if (error instanceof DocxExportError) throw error;
      invalid('$', 'Engine rejected Document content');
    }
    const relationships = [], numbering = [];
    function runs(node, path) {
      return (node.content ?? []).map((child, index) => {
        const childPath = `${path}.content[${index}]`, mark = child.marks?.[0];
        const value = child.text.replace(/\r\n?/g, '\n');
        const fragments = value.split(/([\t\n])/).map(part => part === '\t' ? '<w:tab/>' : part === '\n' ? '<w:br/>' : part ? `<w:t xml:space="preserve">${literal(part, childPath)}</w:t>` : '').join('');
        const run = `<w:r>${mark?.type === 'code' ? '<w:rPr><w:rStyle w:val="InlineCode"/></w:rPr>' : ''}${fragments}</w:r>`;
        if (mark?.type !== 'link') return run;
        const id = `link${relationships.length + 1}`;
        relationships.push(`<Relationship Id="${id}" Type="${R}/hyperlink" Target="${literal(mark.attrs.href, childPath)}" TargetMode="External"/>`);
        return `<w:hyperlink r:id="${id}">${run}</w:hyperlink>`;
      }).join('');
    }
    function paragraph(node, path, list) {
      const style = node.type === 'heading' ? `Heading${node.attrs.level}` : node.type === 'codeBlock' ? 'CodeBlock' : null;
      const properties = (style ? `<w:pStyle w:val="${style}"/>` : '') + (list ? `<w:numPr><w:ilvl w:val="${list.level}"/><w:numId w:val="${list.id}"/></w:numPr>` : '');
      return `<w:p>${properties ? `<w:pPr>${properties}</w:pPr>` : ''}${runs(node, path)}</w:p>`;
    }
    function block(node, path, depth = 0) {
      if (['paragraph', 'heading', 'codeBlock'].includes(node.type)) return paragraph(node, path);
      if (['bulletList', 'orderedList'].includes(node.type)) {
        if (depth >= 9) unmappable(path, 'Word list nesting exceeds nine levels');
        const id = numbering.length + 1;
        numbering.push({ id, ordered: node.type === 'orderedList', start: node.attrs.start ?? 1 });
        return node.content.map((item, index) => item.content.map((child, childIndex) => childIndex === 0 ? paragraph(child, `${path}.content[${index}].content[0]`, { id, level: depth }) : block(child, `${path}.content[${index}].content[${childIndex}]`, depth + 1)).join('')).join('');
      }
      if (node.type === 'table') {
        return `<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/></w:tblPr><w:tblGrid>${node.content[0].content.map(() => '<w:gridCol/>').join('')}</w:tblGrid>${node.content.map((row, rowIndex) => `<w:tr>${row.content.every(cell => cell.type === 'tableHeader') ? '<w:trPr><w:tblHeader/></w:trPr>' : ''}${row.content.map((cell, colIndex) => {
          const contents = cell.content.map((child, index) => block(child, `${path}.content[${rowIndex}].content[${colIndex}].content[${index}]`, depth)).join('');
          // Word requires every table cell to end in a paragraph, including after a nested table.
          const end = cell.content.at(-1).type === 'table' ? '<w:p/>' : '';
          return `<w:tc><w:tcPr/>${contents}${end}</w:tc>`;
        }).join('')}</w:tr>`).join('')}</w:tbl>`;
      }
      unmappable(path, 'Node has no DOCX mapping');
    }
    const body = copy.content.map((node, index) => block(node, `$.content[${index}]`)).join('');
    const styles = `<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>${[1, 2, 3].map(level => `<w:style w:type="paragraph" w:styleId="Heading${level}"><w:name w:val="heading ${level}"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="${level - 1}"/></w:pPr></w:style>`).join('')}<w:style w:type="paragraph" w:styleId="CodeBlock"><w:name w:val="Code Block"/><w:basedOn w:val="Normal"/></w:style><w:style w:type="character" w:styleId="InlineCode"><w:name w:val="Inline Code"/></w:style>`;
    const abstracts = numbering.map(({ id, ordered, start }) => `<w:abstractNum w:abstractNumId="${id}"><w:multiLevelType w:val="multilevel"/>${Array.from({ length: 9 }, (_, level) => `<w:lvl w:ilvl="${level}"><w:start w:val="${ordered ? start : 1}"/><w:numFmt w:val="${ordered ? 'decimal' : 'bullet'}"/><w:lvlText w:val="${ordered ? `%${level + 1}.` : '•'}"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="${720 * (level + 1)}" w:hanging="360"/></w:pPr></w:lvl>`).join('')}</w:abstractNum>`).join('');
    const numbers = abstracts + numbering.map(({ id }) => `<w:num w:numId="${id}"><w:abstractNumId w:val="${id}"/></w:num>`).join('');
    const overrides = [['document', 'document.main'], ['styles', 'styles'], ['numbering', 'numbering']].map(([name, type]) => `<Override PartName="/word/${name}.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.${type}+xml"/>`).join('');
    const files = {
      '[Content_Types].xml': `${XML}<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>${overrides}</Types>`,
      '_rels/.rels': `${XML}<Relationships xmlns="${REL}"><Relationship Id="document" Type="${R}/officeDocument" Target="word/document.xml"/></Relationships>`,
      'word/document.xml': `${XML}<w:document xmlns:w="${W}" xmlns:r="${R}"><w:body>${body}</w:body></w:document>`,
      'word/styles.xml': `${XML}<w:styles xmlns:w="${W}">${styles}</w:styles>`,
      'word/numbering.xml': `${XML}<w:numbering xmlns:w="${W}">${numbers}</w:numbering>`,
      'word/_rels/document.xml.rels': `${XML}<Relationships xmlns="${REL}"><Relationship Id="styles" Type="${R}/styles" Target="styles.xml"/><Relationship Id="numbering" Type="${R}/numbering" Target="numbering.xml"/>${relationships.join('')}</Relationships>`,
    };
    // Stable ZIP timestamps make identical inputs byte-identical, without clock metadata.
    const zipped = Object.fromEntries(Object.entries(files).map(([path, value]) => [path, [strToU8(value), { mtime: new Date(1980, 0, 1) }]]));
    const bytes = zipSync(zipped, { level: 6 });
    if (bytes.length > 8 << 20) unmappable('$', 'DOCX output budget exceeded');
    return { bytes, report: { format: 'docx', schema, qualification: 'unqualified' } };
  };
}
