// Synthetic cross-language fixture. Regenerate: node tools/docs-service/rich-semantic-fixture.mjs > testdata/docs-rich-semantic-v3.json
import {create,validate,outline,patch} from './schema.mjs';
const out=[];
for(const [name,parts,text,range] of [
 ['marks',[{text:'prefix ',marks:[{type:'link',attrs:{href:'https://example.invalid/'}}]},{text:'middle'},{text:' suffix',marks:[{type:'code'}]}],'prefix CHANGED suffix',undefined],
 ['delete',[{text:'all marked',marks:[{type:'code'}]}],'',undefined],
 ['repeated',[{text:'aaaa',marks:[{type:'link',attrs:{href:'https://example.invalid/'}}]}],'aaaaa',{from:0,to:1}],
 ['unicode',[{text:'α😀<>&\u2028\u2029 middle suffix',marks:[{type:'code'}]}],'α😀<>&\u2028\u2029 X suffix',undefined],
 ['empty',[],'new',undefined],
]) {
const para={type:'paragraph',attrs:{id:'target'},...(parts.length?{content:parts.map(part=>({type:'text',...part}))}:{})};
const doc=create({type:'doc',content:[{type:'orderedList',attrs:{id:'ancestor',start:2},content:[{type:'listItem',attrs:{id:'item'},content:[para]}]},{type:'heading',attrs:{id:'other',level:2},content:[{type:'text',text:'Preserve ancestor, root order and marks'}]},{type:'codeBlock',attrs:{id:'code'},content:[{type:'text',text:'literal <script> & 😀'}]}]},3);
const before=validate(doc,3),beforeProjection=(await outline(doc,3)).map(({id,hash})=>({id,hash}));
const edit=patch(doc,{op:'replaceText',id:'target',text,...(range?{range}:{})},3);
const after=validate(doc,3),afterProjection=(await outline(doc,3)).map(({id,hash})=>({id,hash}));
out.push({name,before,after,beforeProjection,afterProjection,semantic:{target:'target',text,edit}});doc.destroy();
}
console.log(JSON.stringify(out,null,2));
