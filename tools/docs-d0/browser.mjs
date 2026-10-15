import { Editor } from '@tiptap/core';
import Collaboration from '@tiptap/extension-collaboration';
import * as Y from 'yjs';
import { extensions, VERSION, load, decode, encode, textNode, resolve } from './schema.mjs';

const token = localStorage.getItem('d0-token');
const clientSchema = Number(localStorage.getItem('d0-schema') || VERSION);
let editor, doc, socket, state, timer, changes = [], pending = new Set(), generation = 0;
let rejected = false;
const status = text => { document.querySelector('#status').textContent = text; };
const savedStatus = () => status(rejected ? 'Změna nebyla uložena — obnoven potvrzený stav' : 'Uloženo');
function build(snapshot) {
  if (changes.length || pending.size) rejected = true;
  clearTimeout(timer); changes = []; pending.clear();
  editor?.destroy(); doc?.destroy(); doc = load(snapshot);
  editor = new Editor({ element: document.querySelector('#editor'), extensions: [...extensions, Collaboration.configure({ document: doc })] });
  doc.on('update', (update, origin) => {
    if (origin === 'remote') return;
    rejected = false;
    changes.push(update); status('Ukládám');
    clearTimeout(timer); timer = setTimeout(flush, 35);
  });
}
function flush() {
  clearTimeout(timer);
  if (!changes.length || socket?.readyState !== WebSocket.OPEN || !state || state.unsupported) return;
  const id = crypto.randomUUID(); const update = encode(Y.mergeUpdates(changes)); changes = [];
  pending.add(id);
  socket.send(JSON.stringify({ kind: 'update', id, update, epoch: state.epoch, schema: VERSION }));
}
function connect() {
  const ownGeneration = ++generation;
  socket?.close(); clearTimeout(timer); changes = []; pending.clear();
  rejected = false;
  status('Připojuji');
  socket = new WebSocket(location.origin.replace('http', 'ws') + '/ws');
  socket.onopen = () => socket.send(JSON.stringify({ kind: 'auth', token, schema: clientSchema }));
  socket.onmessage = event => {
    if (ownGeneration !== generation) return;
    const message = JSON.parse(event.data);
    if (message.kind === 'state' || message.kind === 'reset') {
      if (message.unsupported || message.schema !== VERSION) { state = message; editor?.setEditable(false); status('Nepodporovaná verze schématu'); return; }
      const fresh = !doc || message.kind === 'reset' || message.epoch !== state?.epoch;
      state = message;
      if (fresh) build(message.snapshot); else Y.applyUpdate(doc, decode(message.snapshot), 'remote');
      document.querySelector('#threads').textContent = JSON.stringify(message.threads.map(t => resolve(doc, t)), null, 2);
      if (!pending.size && !changes.length) savedStatus();
    } else if (message.kind === 'ack') {
      pending.delete(message.id);
      if (state && message.epoch === state.epoch) state.rev = Math.max(state.rev, message.rev);
      if (!pending.size && !changes.length) savedStatus();
    } else if (message.kind === 'forbidden') { editor?.setEditable(false); status('Přístup odvolán'); }
    else if (message.kind === 'rejected') { rejected = true; status('Změna odmítnuta — obnovuji potvrzený stav'); }
  };
  socket.onclose = () => { if (ownGeneration === generation && !state?.unsupported && document.querySelector('#status').textContent !== 'Přístup odvolán') status('Odpojeno — změny nejsou potvrzené'); };
}
window.d0 = {
  get editor() { return editor; }, get doc() { return doc; }, get state() { return state; },
  get pending() { return pending.size + changes.length; },
  edit(id, text) { const node = textNode(doc, id); node.insert(node.length, text); },
  replace(id, text) { const node = textNode(doc, id); doc.transact(() => { node.delete(0, node.length); node.insert(0, text); }); },
  flush, reload: connect,
};
connect();
