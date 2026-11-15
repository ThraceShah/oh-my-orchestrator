import { after, before, test } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import net from 'node:net';
import { spawn } from 'node:child_process';
import { mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';

let upstream, child, base;
before(async () => {
  upstream = http.createServer((req, res) => {
    if (req.url === '/readyz') return res.end(JSON.stringify({status: 'ready'}));
    if (req.url === '/internal/fleet/nodes') return res.end(JSON.stringify({nodes: [{id: 'worker-a', presence: 'online', wakeCommand: 'private command', token: 'private token'}]}));
    if (req.url === '/api/v1/events') {
      res.writeHead(200, {'content-type': 'text/event-stream'});
      res.write('event: session_updated\ndata: {}\n\n');
      return;
    }
    if (req.url === '/api/v1/sandbox') {
      res.writeHead(200, {'content-security-policy': 'sandbox'});
      return res.end('sandbox');
    }
    res.end('{}');
  });
  upstream.on('upgrade', (req, socket, head) => {
    assert.equal(req.url, '/mux');
    assert.equal(req.headers.origin, undefined);
    socket.write('HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n');
    if (head.length) socket.write(head);
    socket.on('data', data => socket.write(data));
  });
  upstream.listen(0, '127.0.0.1');
  await once(upstream, 'listening');
  const reserve = net.createServer().listen(0, '127.0.0.1');
  await once(reserve, 'listening');
  const port = reserve.address().port;
  await new Promise(resolve => reserve.close(resolve));
  const root = await mkdtemp(join(tmpdir(), 'omo-web-test-'));
  await writeFile(join(root, 'index.html'), '<title>Original renderer</title>');
  child = spawn(process.execPath, [new URL('./server.mjs', import.meta.url).pathname], {env: {...process.env, PORT: String(port), FLEET_API_PORT: String(upstream.address().port), OPERATOR_DIST: root}, stdio: 'pipe'});
  base = `http://127.0.0.1:${port}`;
  for (let attempt = 0; attempt < 50; attempt++) {
    try { const response = await fetch(base + '/readyz'); if (response.ok) return; } catch {}
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  throw new Error('Operator did not start');
});
after(async () => {
  if (child) { child.kill(); await once(child, 'exit'); }
  upstream?.closeAllConnections();
  await new Promise(resolve => upstream?.close(resolve));
});

test('serves the renderer at the portal prefix and reports real readiness', async () => {
  assert.match(await (await fetch(base + '/apps/orchestrator/')).text(), /Original renderer/);
  const readiness = await (await fetch(base + '/apps/orchestrator/readyz')).json();
  assert.equal(readiness.status, 'ready');
  assert.ok(readiness.port > 0);
});
test('keeps fleet credentials and operator plumbing out of browser responses', async () => {
  const nodes = await (await fetch(base + '/fleet/nodes')).text();
  assert.match(nodes, /worker-a/);
  assert.doesNotMatch(nodes, /private|wakeCommand|token/);
  assert.equal((await fetch(base + '/internal/fleet/nodes', {method: 'POST'})).status, 404);
  assert.equal((await fetch(base + '/shutdown', {method: 'POST'})).status, 404);
});
test('forwards SSE before the upstream closes', async () => {
  const abort = new AbortController();
  const timeout = setTimeout(() => abort.abort(), 2000);
  try {
    const response = await fetch(base + '/api/v1/events', {signal: abort.signal});
    const {value} = await response.body.getReader().read();
    assert.match(new TextDecoder().decode(value), /session_updated/);
  } finally { clearTimeout(timeout); abort.abort(); }
});
test('preserves upstream sandbox policy and rejects cross-origin mutation', async () => {
  assert.equal((await fetch(base + '/api/v1/sandbox')).headers.get('content-security-policy'), 'sandbox');
  assert.equal((await fetch(base + '/api/v1/sessions', {method: 'POST', headers: {origin: 'https://elsewhere.example'}})).status, 403);
});
test('requires human work to enter through the coordinator conversation', async () => {
  for (const path of ['/api/v1/sessions', '/api/v1/orchestrators/delegate', '/api/v1/projects/demo/tasks/prepare', '/api/v1/automations']) {
    const response = await fetch(base + '/apps/orchestrator' + path, {method: 'POST', headers: {origin: base}});
    assert.equal(response.status, 409);
    assert.equal((await response.json()).error.code, 'COORDINATOR_REQUIRED');
  }
  const coordinator = await fetch(base + '/api/v1/orchestrators', {method: 'POST', headers: {origin: base}});
  assert.equal(coordinator.status, 200);
});
test('forwards terminal upgrades including data received with the request', async () => {
  const url = new URL(base);
  const socket = net.connect(Number(url.port), url.hostname);
  socket.write(`GET /apps/orchestrator/mux HTTP/1.1\r\nHost: ${url.host}\r\nOrigin: ${base}\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: test\r\nSec-WebSocket-Version: 13\r\n\r\nfirst-frame`);
  const result = await new Promise((resolve, reject) => {
    let output = '';
    socket.setTimeout(2000, () => reject(new Error('Websocket handshake timed out')));
    socket.on('data', chunk => { output += chunk; if (output.includes('first-frame')) resolve(output); });
    socket.on('error', reject);
  }).finally(() => socket.destroy());
  assert.match(result, /101 Switching Protocols/);
  assert.match(result, /first-frame/);
});
