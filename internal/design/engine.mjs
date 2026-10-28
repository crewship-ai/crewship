// Standard Yjs engine. The host supplies state and enforces authority; this
// short-lived subprocess has no database, network, credentials or persistence.
import * as Y from 'yjs'
import * as sync from 'y-protocols/sync'
import * as encoding from 'lib0/encoding'
import * as decoding from 'lib0/decoding'

// Validate raw field values before conversion to JSON as well as REST patches.
// A nested Y.Map or plain object must never reach React as a node label or paint.
const nodeStringLimits = new Map([
  ['id', 128], ['type', 16], ['name', 2000], ['parentId', 128],
  ['fill', 256], ['text', 100000], ['src', 2000000],
])
const nodeGeometryFields = new Set(['x', 'y', 'width', 'height'])
function validateNodeFields(entries) {
  for (const [key, value] of entries) {
    if (nodeStringLimits.has(key)) {
      if (typeof value !== 'string' || value.length > nodeStringLimits.get(key)) throw new Error(`invalid node ${key}`)
    } else if (nodeGeometryFields.has(key)) {
      if (typeof value !== 'number' || !Number.isFinite(value) || Math.abs(value) > 1000000) throw new Error('invalid geometry')
    } else throw new Error('unknown node field')
  }
}

async function main() {
let input = ''
for await (const chunk of process.stdin) input += chunk
const request = JSON.parse(input)
const doc = new Y.Doc()
try {
  if (request.state) Y.applyUpdate(doc, Buffer.from(request.state, 'base64'))
  const nodes = doc.getMap('nodes')
  const order = doc.getArray('order')
  const before = Y.encodeStateVector(doc)
  let response = null
  if (request.frame) {
    const decoder = decoding.createDecoder(Buffer.from(request.frame, 'base64'))
    const type = decoding.readVarUint(decoder)
    if (type !== 0) throw new Error('expected standard sync frame')
    const encoder = encoding.createEncoder()
    encoding.writeVarUint(encoder, 0)
    sync.readSyncMessage(decoder, encoder, doc, 'remote', error => { throw error })
    if (encoding.length(encoder) > 1) response = Buffer.from(encoding.toUint8Array(encoder)).toString('base64')
    if (decoding.hasContent(decoder)) throw new Error('unexpected trailing frame bytes')
  }
  if (request.replace) {
    // Restore updates existing CRDT structures, preserving connected clients.
    const restored = new Y.Doc()
    Y.applyUpdate(restored, Buffer.from(request.replace, 'base64'))
    const target = restored.getMap('nodes').toJSON()
    doc.transact(() => {
      for (const id of nodes.keys()) if (!(id in target)) nodes.delete(id)
      for (const [id, values] of Object.entries(target)) {
        let node = nodes.get(id)
        if (!(node instanceof Y.Map)) { node = new Y.Map(); nodes.set(id, node) }
        for (const key of node.keys()) if (!(key in values)) node.delete(key)
        for (const [key, value] of Object.entries(values)) node.set(key, value)
      }
      order.delete(0, order.length)
      order.push(restored.getArray('order').toArray())
    })
    restored.destroy()
  }
  if (request.operations) {
    if (!Array.isArray(request.operations) || request.operations.length > 500) throw new Error('maximum 500 operations')
    doc.transact(() => {
      for (const op of request.operations) {
        if (op.op === 'upsert') {
          const value = op.node
          if (!value || typeof value.id !== 'string' || !value.id || value.id.length > 128) throw new Error('node id required')
          validateNodeFields(Object.entries(value))
          let node = nodes.get(value.id)
          if (!(node instanceof Y.Map)) {
            node = new Y.Map(); nodes.set(value.id, node)
            if (!order.toArray().includes(value.id)) order.push([value.id])
          }
          for (const [key, v] of Object.entries(value)) node.set(key, v)
        } else if (op.op === 'delete') {
          nodes.delete(op.id)
          for (let i = order.length - 1; i >= 0; i--) if (order.get(i) === op.id) order.delete(i, 1)
        } else if (op.op === 'reorder') {
          if (!Array.isArray(op.order)) throw new Error('order required')
          order.delete(0, order.length); order.push(op.order)
        } else throw new Error('unknown operation')
      }
    })
  }
  const result = []
  if (nodes.size > 2000 || order.length > 4000) throw new Error('design capacity exceeded')
  for (const [id, value] of nodes) {
    if (!(value instanceof Y.Map)) throw new Error('node must be a Y.Map')
    validateNodeFields(value.entries())
    const node = value.toJSON()
    if (!id || id.length > 128 || node.id !== id || !['frame', 'rect', 'text', 'image'].includes(node.type)) throw new Error('invalid node')
    for (const key of ['x', 'y', 'width', 'height']) {
      if (typeof node[key] !== 'number' || !Number.isFinite(node[key]) || Math.abs(node[key]) > 1000000) throw new Error('invalid geometry')
    }
    if (node.width < 0 || node.height < 0) throw new Error('invalid size')
    result.push(node)
  }
  for (const id of order) if (typeof id !== 'string' || !nodes.has(id)) throw new Error('invalid order')
  const update = Y.encodeStateAsUpdate(doc, before)
  const full = Y.encodeStateAsUpdate(doc)
  if (full.length > 4 * 1024 * 1024) throw new Error('design exceeds 4 MiB')
  const encoder = encoding.createEncoder()
  encoding.writeVarUint(encoder, 0)
  sync.writeSyncStep2(encoder, doc)
  const helloEncoder = encoding.createEncoder()
  encoding.writeVarUint(helloEncoder, 0)
  sync.writeSyncStep1(helloEncoder, doc)
  process.stdout.write(JSON.stringify({hello: Buffer.from(encoding.toUint8Array(helloEncoder)).toString('base64'), state: Buffer.from(full).toString('base64'), update: Buffer.from(update).toString('base64'), frame: Buffer.from(encoding.toUint8Array(encoder)).toString('base64'), response, nodes: result, order: order.toArray()}))
} catch (e) {
  process.stderr.write(String(e.message))
  process.exitCode = 1
} finally { doc.destroy() }

}
main()
