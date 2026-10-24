// Run in a real browser with the generated web-target wasm-bindgen module.
// The caller supplies exact fixture strings and initializes the SDK first.
// This is numeric-data/ABI qualification, never host authority or Saved proof.
export function qualifyExplicitOrigins(m, native, raw) {
const checks={};const check=(n,v)=>{if(!v)throw Error(n);checks[n]=true;};const stable=v=>JSON.stringify(v,(k,x)=>x&&typeof x==='object'&&!Array.isArray(x)?Object.fromEntries(Object.entries(x).sort()):x);const same=(a,b)=>JSON.stringify([...a])===JSON.stringify([...b]);const throws=(n,fn,expected)=>{let error;try{fn();}catch(e){error=String(e);}check(n,error!==undefined&&(expected===undefined||error===expected));};
const v=JSON.parse(native);const ids=JSON.stringify(v.body.map((_,i)=>`stable-${i}`));
 for(const [i,origin]of ['1','9007199254740991'].entries()){
 const model=m.WordCraftModel.import_with_origin(native,ids,origin);check('canonical_writable_'+i,!model.is_read_only());const cp=model.checkpoint();check('full_native_'+i,stable(JSON.parse(model.native_json()))===stable(v));check('all_ids_'+i,model.stable_ids_json()===ids);
 // Inspect only the generated checkpoint's original Yrs frontier via explicit
 // occupied-origin rejection. No browser-supplied authority is constructed.
 throws('occupied_original_origin_'+i,()=>m.WordCraftModel.restore_with_fresh_origin(cp,origin),'invalid_checkpoint');
 const fresh=m.WordCraftModel.restore_with_fresh_origin(cp,'31');check('byteexact_checkpoint_'+i,same(fresh.checkpoint(),cp));check('fresh_full_native_'+i,fresh.native_json()===model.native_json());check('fresh_all_ids_'+i,fresh.stable_ids_json()===ids);fresh.free();model.free();}
 for(const [i,origin]of ['', '0','00','01','+1','-1','1.0','1e1',' 1','1 ','9007199254740992','18446744073709551616'].entries()){
 throws('invalid_import_preparse_'+i,()=>m.WordCraftModel.import_with_origin('bad','bad',origin),'invalid_origin');throws('invalid_restore_preparse_'+i,()=>m.WordCraftModel.restore_with_fresh_origin(new Uint8Array([255]),origin),'invalid_origin');}
 for(const [i,bad]of [17,0,true,false,null,undefined,{},[],17n,'1'.repeat(1000000)].entries()){throws('JS_invalid_type_or_length_import_'+i,()=>m.WordCraftModel.import_with_origin(native,ids,bad),'invalid_origin');throws('JS_invalid_type_or_length_restore_'+i,()=>m.WordCraftModel.restore_with_fresh_origin(new Uint8Array([255]),bad),'invalid_origin');}
const rawIds=JSON.stringify(JSON.parse(raw).body.map((_,i)=>`raw-${i}`));const ro=m.WordCraftModel.import_with_origin(raw,rawIds,'17');check('raw_native_readonly',ro.is_read_only());const cp=ro.checkpoint();const rr=m.WordCraftModel.restore_with_fresh_origin(cp,'31');check('restored_raw_readonly',rr.is_read_only());check('raw_checkpoint_exact',same(rr.checkpoint(),cp));check('raw_fullnative_exact',rr.native_json()===ro.native_json());check('raw_ids_exact',rr.stable_ids_json()===rawIds);rr.free();ro.free();
 const legacy=new m.WordCraftModel(native,ids);check('legacy_constructor_unchanged',!legacy.is_read_only());const lr=m.WordCraftModel.restore(legacy.checkpoint());check('legacy_restore_unchanged',lr.native_json()===legacy.native_json());lr.free();legacy.free();return {checks,scope:'Real Chromium generated WASM numeric-data constructors only; no issuance, network, human controller or Saved proof'};
}
