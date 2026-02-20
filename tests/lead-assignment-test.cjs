/**
 * Lead Assignment End-to-End Test
 *
 * Tests that a LEAD agent (Tomáš) can assign a task to a crew member (Viktor)
 * via the sidecar assignment API and that WS assignment events are received.
 *
 * Usage:
 *   WS_TOKEN=$(go run ./tools/gen-ws-token) node _lead-assignment-test.cjs
 *
 * Or with a specific lead chat:
 *   WS_TOKEN=<token> LEAD_CHAT_ID=<chat-id> node _lead-assignment-test.cjs
 *
 * The script sends a message to the lead agent instructing it to assign a task
 * to Viktor, then monitors for:
 *   - assignment_created  (sidecar POSTed to crewshipd)
 *   - assignment_running  (sub-agent started)
 *   - assignment_completed (Viktor responded)
 *
 * Environment:
 *   WS_TOKEN       Required. JWT token from `go run ./tools/gen-ws-token`
 *   LEAD_CHAT_ID   Chat ID for Tomáš. Defaults to test-lead-chat-tomas-001
 *   PORT           Go server port. Defaults to 8081
 *   TIMEOUT_MS     How long to wait. Defaults to 180000 (3 min)
 */

'use strict';
const WebSocket = require('ws');

const PORT = process.env.PORT || '8081';
const CHAT_ID = process.env.LEAD_CHAT_ID || 'test-lead-chat-tomas-001';
const TOKEN = process.env.WS_TOKEN;
const TIMEOUT_MS = parseInt(process.env.TIMEOUT_MS || '180000', 10);

if (!TOKEN) {
  console.error('ERROR: WS_TOKEN is required.');
  console.error('Run: WS_TOKEN=$(go run ./tools/gen-ws-token) node _lead-assignment-test.cjs');
  process.exit(1);
}

const ws = new WebSocket(`ws://localhost:${PORT}/ws?token=${TOKEN}`);

// Track state
const state = {
  connected: false,
  subscribed: false,
  messageSent: false,
  leadResponseChunks: [],
  events: [],
};

const expectedEvents = ['assignment_created', 'assignment_running', 'assignment_completed'];
let receivedEvents = new Set();

const timeout = setTimeout(() => {
  console.log('\n\nTIMEOUT after', TIMEOUT_MS / 1000, 'seconds');
  summary();
  ws.close();
  process.exit(receivedEvents.size === expectedEvents.length ? 0 : 1);
}, TIMEOUT_MS);

function summary() {
  console.log('\n========== SUMMARY ==========');
  console.log('Chat ID:', CHAT_ID);
  console.log('\nAssignment events received:');
  for (const ev of expectedEvents) {
    const got = receivedEvents.has(ev);
    console.log(` ${got ? '✓' : '✗'} ${ev}`);
  }
  if (state.events.length > 0) {
    console.log('\nAll assignment events:');
    for (const ev of state.events) {
      console.log(' ', JSON.stringify(ev));
    }
  }
  const success = expectedEvents.every(e => receivedEvents.has(e));
  console.log('\nResult:', success ? '✓ PASSED' : '✗ FAILED');
  console.log('=============================\n');
}

ws.on('open', () => {
  console.log('[connected] to ws://localhost:' + PORT + '/ws');
  console.log('[chat]', CHAT_ID);
  state.connected = true;

  // Subscribe to session channel
  ws.send(JSON.stringify({ type: 'subscribe', channel: `session:${CHAT_ID}` }));

  // Send assignment instruction after a short delay
  setTimeout(() => {
    const message = [
      'Zadej Viktorovi (viktor) tento úkol přes bash tool:',
      '`curl -s -X POST http://localhost:9119/assign -H "Content-Type: application/json" -d \'{"target":"viktor","task":"Napiš jednoduchý Python hello world skript a vrať jeho obsah."}\'`',
      '',
      'Pak počkej na výsledek pomocí:',
      '`curl -s http://localhost:9119/results/<assignment_id>`',
      '',
      'Zavolej mi výsledek co Viktor vrátí.',
    ].join('\n');

    console.log('[sending] assignment instruction to lead agent...');
    ws.send(JSON.stringify({
      type: 'send_message',
      channel: `session:${CHAT_ID}`,
      payload: { session_id: CHAT_ID, content: message },
    }));
    state.messageSent = true;
  }, 800);
});

ws.on('message', (data) => {
  let msg;
  try {
    msg = JSON.parse(data.toString());
  } catch {
    return;
  }

  if (msg.type === 'ping') return;

  // Handle chat events (lead agent streaming output)
  if (msg.type === 'chat_event' && msg.payload) {
    const p = msg.payload;
    if (p.type === 'text') {
      process.stdout.write(p.content || '');
    } else if (p.type === 'thinking') {
      // suppress repeated "Processing..."
    } else if (p.type === 'system') {
      console.log('\n[system]', (p.content || '').substring(0, 200));
    } else if (p.type === 'error') {
      console.log('\n[ERROR]', p.content);
      clearTimeout(timeout);
      summary();
      ws.close();
      process.exit(1);
    } else if (p.type === 'done') {
      console.log('\n[lead done]');
      if (receivedEvents.size === expectedEvents.length) {
        clearTimeout(timeout);
        summary();
        ws.close();
        process.exit(0);
      }
      // Wait a bit more for final assignment_completed event
      setTimeout(() => {
        clearTimeout(timeout);
        summary();
        ws.close();
        process.exit(receivedEvents.size === expectedEvents.length ? 0 : 1);
      }, 5000);
    } else {
      console.log('\n[' + p.type + ']', (p.content || '').substring(0, 80));
    }
    return;
  }

  // Handle assignment WS events
  const assignmentEvents = ['assignment_created', 'assignment_running', 'assignment_completed', 'assignment_failed'];
  if (assignmentEvents.includes(msg.type)) {
    receivedEvents.add(msg.type);
    state.events.push({ type: msg.type, payload: msg.payload });
    console.log('\n[WS EVENT]', msg.type, JSON.stringify(msg.payload || {}).substring(0, 150));
    return;
  }

  // Log other messages at debug level
  if (msg.type !== 'subscribed') {
    console.log('[ws]', msg.type, JSON.stringify(msg).substring(0, 100));
  }
});

ws.on('error', (e) => {
  console.error('WS error:', e.message);
  process.exit(1);
});

ws.on('close', () => {
  clearTimeout(timeout);
  process.exit(0);
});

process.on('SIGINT', () => {
  console.log('\n[interrupted]');
  summary();
  ws.close();
  process.exit(0);
});
