import test from 'node:test';
import assert from 'node:assert/strict';
import { create, validate, snapshot, load, anchor, resolve } from './schema.mjs';
import { captureStructure, structuralPatch } from './structure.mjs';
const p = (id, text = id) => ({type:'paragraph',attrs:{id},content:[{type:'text',text}]});
const fixture = () => create({type:'doc',content:[p('outside-left'),p('a'),p('b'),p('c'),p('outside-right')]},3);
const scope = doc => captureStructure(doc,{block_ids:['a','b','c'],allow_insert_after:true},3);
test('explicit whole-block scope captures fixed hashes and surrounding identities',async()=>{
 const doc=fixture();try{
  const selected=await scope(doc);assert.equal(selected.version,1);assert.deepEqual(selected.blocks.map(block=>block.id),['a','b','c']);assert.equal(selected.before.id,'outside-left');assert.equal(selected.after.id,'outside-right');
  await assert.rejects(()=>captureStructure(doc,{block_ids:['a','c'],allow_insert_after:true},3),/contiguous/);
  await assert.rejects(()=>captureStructure(doc,{block_ids:['a'],allow_insert_after:false},3),/explicit/);
  await assert.rejects(()=>captureStructure(doc,{block_ids:['a'],allow_insert_after:true},2),/schema3/);
 }finally{doc.destroy()}
});
test('bounded insert delete and reorder preserve untouched character anchors and reload',async()=>{
 const doc=fixture();try{
  const selected=await scope(doc), comment=anchor(doc,'outside-right',0,7,'untouched',0,3);
  const proof=await structuralPatch(doc,[{op:'insert_after',after_id:'c',node:{type:'heading',attrs:{level:2},content:[{type:'text',text:'new plain block'}]}},{op:'delete',id:'b'},{op:'move_before',id:'c',before_id:'a'}],'request',selected,3);
  assert.deepEqual(validate(doc,3).content.map(node=>node.attrs.id),['outside-left','c','a','request-new-0','outside-right']);
  assert.equal(proof.operations[0].node.attrs.id,'request-new-0');assert.match(proof.operations[1].if_hash,/^[a-f0-9]{64}$/);
  assert.deepEqual(resolve(doc,comment,3),{...comment,status:'attached',from:0,to:7});
  const restored=load(snapshot(doc));try{assert.deepEqual(validate(restored,3),validate(doc,3));assert.deepEqual(resolve(restored,comment,3),{...comment,status:'attached',from:0,to:7})}finally{restored.destroy()}
 }finally{doc.destroy()}
});
test('structural scope cannot escape through foreign targets or client IDs/marks',async()=>{
 const cases=[{op:'delete',id:'outside-left'},{op:'insert_after',after_id:'outside-right',node:p('injected')},{op:'move_before',id:'a',before_id:'outside-left'},{op:'insert_after',after_id:'a',node:p('existing')},{op:'insert_after',after_id:'a',node:{type:'paragraph',content:[{type:'text',text:'link',marks:[{type:'link',attrs:{href:'https://example.test/'}}]}]}}];
 for(const op of cases){const doc=fixture();try{const selected=await scope(doc);await assert.rejects(()=>structuralPatch(doc,[op],'request',selected,3))}finally{doc.destroy()}}
});

test('whole document selection supports 200 blocks without truncating and rejects over 256',async()=>{
 for(const count of [200,256,257]){
  const json={type:'doc',content:Array.from({length:count},(_,i)=>p(`block-${i}`))},doc=create(json,3);
  try{const input={block_ids:json.content.map(node=>node.attrs.id),allow_insert_after:true};if(count>256)await assert.rejects(()=>captureStructure(doc,input,3),/explicit/);else assert.equal((await captureStructure(doc,input,3)).blocks.length,count)}finally{doc.destroy()}
 }
});
