import test from 'node:test';
import assert from 'node:assert/strict';
import { creationDocument, CREATION_MAX_TEXT_BYTES } from './create-document.mjs';

test('readable authoring produces unique server-owned IDs without HTML interpretation', () => {
  let id=0;
  const doc=creationDocument([{type:'heading',level:1,text:'Report'},{type:'paragraph',text:'Literal <script> & text'},{type:'bulletList',items:['First','Second']},{type:'orderedList',items:['Step']},{type:'codeBlock',text:'const x = "<script>";\n'}],()=>`server-${++id}`);
  const ids=[];
  const walk=node=>{if(node.attrs)ids.push(node.attrs.id);for(const child of node.content??[])walk(child)};
  walk(doc);
  assert.equal(new Set(ids).size,ids.length);
  assert.equal(doc.content[1].content[0].text,'Literal <script> & text');
  assert.equal(doc.content[2].content[0].content[0].type,'paragraph');
  assert.equal(doc.content[4].type,'codeBlock');
});
test('closed authoring rejects images, supplied IDs, marks, URLs, malformed text and empty content', () => {
  for(const blocks of [[],[{type:'image',src:'https://example.invalid'}],[{type:'paragraph',id:'forged',text:'x'}],[{type:'paragraph',text:'x',marks:[]}],[{type:'heading',level:4,text:'x'}],[{type:'paragraph',text:'\ud800'}],[{type:'paragraph',text:'\u0000'}],[{type:'paragraph',text:'  '}]])assert.throws(()=>creationDocument(blocks));
});
test('text and generated-block budgets apply before CRDT allocation', () => {
  const text='é'.repeat(32768);
  assert.equal(Buffer.byteLength(text),65536);
  const blocks=Array.from({length:4},()=>({type:'paragraph',text}));
  assert.equal(Buffer.byteLength(blocks.map(b=>b.text).join('')),CREATION_MAX_TEXT_BYTES);
  assert.equal(creationDocument(blocks).content.length,4);
  assert.throws(()=>creationDocument([...blocks,{type:'paragraph',text:'x'}]));
  assert.throws(()=>creationDocument(Array.from({length:3},()=>({type:'bulletList',items:Array.from({length:128},()=> 'item')}))));
});

test('Unicode blank classification matches Go rather than JS trim', () => {
 for(const text of ['\ufeff','\u0085',' \t\r\n\u00a0\u1680\u2000\u200a\u2028\u2029\u202f\u205f\u3000'])assert.throws(()=>creationDocument([{type:'paragraph',text}]));
 for(const text of ['x\ufeff','\u0085x','český text'])assert.equal(creationDocument([{type:'paragraph',text}]).content[0].content[0].text,text);
});

test('all authoring blocks roundtrip through the actual schema3 CRDT engine', async () => {
 const schema=await import('./schema.mjs');
 const json=creationDocument([{type:'heading',level:1,text:'Complete report'},{type:'paragraph',text:'A useful paragraph.'},{type:'bulletList',items:['One','Two']},{type:'orderedList',items:['First']},{type:'codeBlock',text:'const literal = "<script>";\n'}]);
 const doc=schema.create(json,3);
 const restored=schema.load(schema.snapshot(doc));
 try {
  const content=schema.validate(restored,3),outline=await schema.outline(restored,3);
  assert.deepEqual(content.content.map(node=>node.type),['heading','paragraph','bulletList','orderedList','codeBlock']);
  assert.ok(outline.some(block=>block.type==='codeBlock' && block.text==='const literal = "<script>";\n'));
  assert.equal(new Set(outline.map(block=>block.id)).size,outline.length);
  assert.ok(outline.every(block=>/^[a-f0-9]{64}$/.test(block.hash)));
 } finally {restored.destroy();doc.destroy()}
});
