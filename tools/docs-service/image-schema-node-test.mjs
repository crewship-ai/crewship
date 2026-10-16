import test from 'node:test';
import assert from 'node:assert/strict';
import * as Y from 'yjs';
import {create,validate,snapshot,load,find,hash,imageReferences,markdown} from './schema.mjs';
const sha='a'.repeat(64),ref='logical-image-one';
const paragraph={type:'paragraph',attrs:{id:'text'},content:[{type:'text',text:'Original linked text',marks:[{type:'link',attrs:{href:'https://example.invalid/'}}]}]};
const image=(id='image',asset_ref=ref,expected_sha256=sha)=>({type:'image',attrs:{id,asset_ref,expected_sha256,alt:'Private diagram',width:640,height:480}});
const body=(...images)=>({type:'doc',content:[structuredClone(paragraph),...images]});
const json=value=>JSON.parse(JSON.stringify(value));
test('schema4 image model is explicit and schema3/default2 remain closed',()=>{
 const doc=create(body(image()),4);assert.deepEqual(json(validate(doc,4)),body(image()));assert.deepEqual(imageReferences(doc),[{asset_ref:ref,expected_sha256:sha}]);assert.throws(()=>validate(doc,3));assert.throws(()=>create(body(image()),3));assert.throws(()=>create(body(image())));doc.destroy();
});
test('schema4 preserves actual schema3 binary and hash without read normalization',async()=>{
 const doc=create(body(),3),before=snapshot(doc),digest=await hash(validate(doc,3));assert.deepEqual(validate(doc,4),validate(doc,3));assert.equal(snapshot(doc),before);assert.equal(await hash(validate(doc,4)),digest);assert.deepEqual(imageReferences(doc),[]);doc.destroy();
});
test('image attrs are closed scalar values, bounded geometry/alt and root-only blocks',()=>{
 for(const attrs of [{width:0},{width:2049},{height:1.5},{width:'640'},{alt:'a'.repeat(2049)},{alt:'unsafe\nalt'},{asset_ref:['logical']},{expected_sha256:'A'.repeat(64)},{src:'https://outside.invalid/a.png'}]){const node=image();Object.assign(node.attrs,attrs);assert.throws(()=>create(body(node),4),JSON.stringify(attrs))}
 for(const bad of [{...image(),marks:[{type:'code'}]},{...image(),content:[]},{...image(),content:[{type:'text',text:'hidden'}]}])assert.throws(()=>create(body(bad),4));
 const nested={type:'doc',content:[{type:'bulletList',attrs:{id:'list'},content:[{type:'listItem',attrs:{id:'item'},content:[{type:'paragraph',attrs:{id:'first'}},image()]}]}]};assert.throws(()=>create(nested,4));
 const node=image();node.attrs.alt='';node.attrs.width=2048;node.attrs.height=1;const doc=create(body(node),4);doc.destroy();
});
test('deleted image refs remain in complete gc:false manifest across actual Yjs reload',()=>{
 const doc=create(body(image()),4),before=imageReferences(doc);doc.getXmlFragment('default').delete(1,1);assert.equal(validate(doc,4).content.length,1);assert.deepEqual(imageReferences(doc),before);const copy=load(snapshot(doc));assert.deepEqual(imageReferences(copy),before);copy.destroy();doc.destroy();
});
test('retained source pair cannot be rebound or mismatched even after deletion',()=>{
 for(const [key,value]of [['asset_ref','replacement'],['expected_sha256','b'.repeat(64)],['id','renamed-image']]){const doc=create(body(image()),4);find(doc,'image').setAttribute(key,value);assert.throws(()=>validate(doc,4),/immutable/);doc.getXmlFragment('default').delete(1,1);const copy=load(snapshot(doc));assert.throws(()=>imageReferences(copy),/immutable/);copy.destroy();doc.destroy()}
});
test('manifest deduplicates logical placements and refuses conflicting digests across node types',()=>{
 const doc=create(body(image('first'),image('second')),4);assert.deepEqual(imageReferences(doc),[{asset_ref:ref,expected_sha256:sha}]);doc.destroy();assert.throws(()=>create(body(image('first'),image('second',ref,'b'.repeat(64))),4),/conflicting/);
});
test('manifest never cross-products old attrs from different retained image instances',()=>{
 const doc=create(body(image('first',ref,sha),image('second','another-ref','b'.repeat(64))),4);doc.getXmlFragment('default').delete(1,1);assert.deepEqual(imageReferences(doc),[{asset_ref:'another-ref',expected_sha256:'b'.repeat(64)},{asset_ref:ref,expected_sha256:sha}]);doc.destroy();
});
test('bounded alt and geometry edits retain the immutable source without adding phantom references',()=>{
 const doc=create(body(image()),4),node=find(doc,'image');node.setAttribute('alt','Updated description');node.setAttribute('width',320);assert.deepEqual(imageReferences(doc),[{asset_ref:ref,expected_sha256:sha}]);node.setAttribute('alt','bad\u0000alt');assert.throws(()=>validate(doc,4));doc.getXmlFragment('default').delete(1,1);assert.throws(()=>imageReferences(doc));doc.destroy();
});
test('raw retained image children/foreign parent and unknown attrs fail closed',()=>{
 for(const mutate of [node=>{const text=new Y.XmlText();text.insert(0,'hidden');node.insert(0,[text])},node=>node.setAttribute('src','https://outside.invalid/a.png')]){const doc=create(body(image()),4);mutate(find(doc,'image'));assert.throws(()=>validate(doc,4));doc.getXmlFragment('default').delete(1,1);assert.throws(()=>imageReferences(doc));doc.destroy()}
 const doc=create(body(),4),nested=new Y.XmlElement('image');for(const [key,value]of Object.entries(image().attrs))nested.setAttribute(key,value);find(doc,'text').insert(0,[nested]);assert.throws(()=>imageReferences(doc));doc.destroy();
});
test('private images have no implicit Markdown byte or authenticated-URL export',()=>{
 assert.throws(()=>markdown(body(image())),error=>error.code==='unsupported_markdown'&&/private image/.test(error.message));
});
