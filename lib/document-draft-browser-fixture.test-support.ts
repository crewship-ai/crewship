export const documentDraftBrowserFixture = `
import React from 'react';
import {createRoot} from 'react-dom/client';import {createPortal} from 'react-dom';
import {QueryClient,QueryClientProvider} from '@tanstack/react-query';
import {AuthProvider,useAuth} from '@/hooks/use-auth';
import {AUTH_EVENT} from '@/lib/api-fetch';
import {usePageDocument} from '@/hooks/use-page-document';
import {documentSchema} from '@/lib/documents/schema';
const source=documentSchema.create({type:'doc',content:[{type:'paragraph',attrs:{id:'paragraph'},content:[{type:'text',text:'base'}]}]},3);
const state={page_id:'p',schema:3,rev:1,epoch:'epoch',generation:1,incarnation:'generation',snapshot:documentSchema.snapshot(source),snapshotRev:1,updates:[],blocks:[],content:{type:'doc'},outline:[],can_write:true,can_comment:false,can_resolve:false};
// Keep this single-tab fixture mounted so its post-sign-out IDB rows can be observed.
window.BroadcastChannel=undefined;
const f=window.documentErasureFixture={human:'alice',auth:null,editor:null,pause:false,putStarted:false,abortCount:0,purgeStarted:false,done:false,holdSession:false,sessionStarted:false};
const idbOpen=IDBFactory.prototype.open;
IDBFactory.prototype.open=function(name,...args){if(f.failOpening&&name==='crewship-document-drafts')throw new DOMException('fixture storage absent','InvalidStateError');return idbOpen.call(this,name,...args);};
const networkFetch=window.fetch.bind(window);
window.fetch=async (url,init)=>{
 const path=String(url);f.lastRequest=path;
 if(path.includes('/draft-key?'))return networkFetch(url,init);
 if(path.includes('/api/auth/session')){
  const response=Response.json({user:{id:f.human,name:f.human,email:f.human+'@example.test'},expires:'2099-01-01'});
  if(f.holdSession){f.sessionStarted=true;await new Promise(resolve=>f.releaseSession=resolve);}
  return response;
 }
 if(path.includes('/api/auth/signout'))return Response.json({});
 if(path.includes('/api/auth/token/refresh'))return Response.json({error:'session expired'},{status:401});
 if(path.includes('/document?'))return f.readStatus ? Response.json({error:'access unavailable'},{status:f.readStatus}) : Response.json(state);
 throw new Error('Unexpected fixture request');
};
const abort=IDBTransaction.prototype.abort;
IDBTransaction.prototype.abort=function(){f.abortCount++;return abort.call(this);};
const put=IDBObjectStore.prototype.put;
IDBObjectStore.prototype.put=function(value,...args){
 if(this.name==='drafts' && f.failWrites){f.failedWrites=(f.failedWrites||0)+1;throw new DOMException('fixture storage failed','QuotaExceededError');}
 const result=put.call(this,value,...args);
 if(this.name==='drafts' && f.pause){
  f.putStarted=true;
  const keepAlive=()=>{if(!f.pause)return;this.get(value.key).onsuccess=keepAlive;};
  keepAlive();
 }
 return result;
};
const cursor=IDBObjectStore.prototype.openCursor;
IDBObjectStore.prototype.openCursor=function(...args){if(this.name==='drafts')f.purgeStarted=true;return cursor.apply(this,args);};
function Editor(){const d=usePageDocument('w','slug','p',true);f.editor=d;return createPortal(<div id='draft-private-portal'>PRIVATE_PORTAL_COPY</div>,document.body);}
function App(){const auth=useAuth();f.auth=auth;return auth.session?<Editor key={auth.session.user.id}/>:null;}
createRoot(document.getElementById('root')).render(<QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false,gcTime:0}}})}><AuthProvider><App/></AuthProvider></QueryClientProvider>);
f.edit=()=>documentSchema.textNode(f.editor.doc,'paragraph').insert(0,'private draft ');
f.rows=()=>new Promise((resolve,reject)=>{
 const request=indexedDB.open('crewship-document-drafts',1);
 request.onerror=()=>reject(new Error('fixture read'));
 request.onsuccess=()=>{const db=request.result;const tx=db.transaction('drafts','readonly');const rows=tx.objectStore('drafts').getAll();tx.oncomplete=()=>{db.close();resolve(rows.result.map(row=>JSON.parse(row.key[0])[0]));};};
});
window.draftFailureFixture=f;
f.expire=()=>window.dispatchEvent(new CustomEvent(AUTH_EVENT,{detail:{reason:'session_expired'}}));
`
