import http from 'node:http';
import net from 'node:net';
import { readFile, stat } from 'node:fs/promises';
import { resolve, extname, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const host = process.env.HOST || '127.0.0.1';
const port = Number(process.env.PORT || 3012);
const apiHost = process.env.FLEET_API_HOST || '127.0.0.1';
const apiPort = Number(process.env.FLEET_API_PORT || 3011);
const staticRoot = resolve(process.env.OPERATOR_DIST || fileURLToPath(new URL('./dist', import.meta.url)));
const basePath = (process.env.BASE_PATH || '/apps/orchestrator').replace(/\/$/, '');
function requestUrl(raw) {
  const url = new URL(raw || '/', 'http://localhost');
  if (url.pathname === basePath || url.pathname.startsWith(basePath + '/')) url.pathname = url.pathname.slice(basePath.length) || '/';
  return url;
}
const types = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2', '.ttf': 'font/ttf', '.wasm': 'application/wasm', '.mp3': 'audio/mpeg' };

function publicNode(node) {
  const { id, name, role, availability, capacity, platforms, presence, lastSeenAt, powerBroker, capabilities } = node;
  return { id, name, role, availability, capacity, platforms, presence, lastSeenAt, powerBroker, capabilities };
}
function send(res, status, type, body) {
  res.writeHead(status, { 'content-type': type, 'cache-control': 'no-store' });
  res.end(body);
}
function sameOrigin(req) {
  if (!req.headers.origin) return !req.headers['sec-fetch-site'] || req.headers['sec-fetch-site'] === 'same-origin';
  try { return new URL(req.headers.origin).host === req.headers.host; } catch { return false; }
}
function proxy(req, res, path) {
  const headers = {};
  for (const key of ['content-type', 'accept', 'last-event-id', 'range', 'if-none-match']) {
    if (req.headers[key]) headers[key] = req.headers[key];
  }
  const upstream = http.request({ host: apiHost, port: apiPort, method: req.method, path, headers }, (incoming) => {
    const out = { 'cache-control': 'no-store' };
    for (const key of ['content-type', 'content-range', 'accept-ranges', 'etag', 'content-security-policy', 'content-disposition', 'x-content-type-options', 'x-frame-options']) {
      if (incoming.headers[key]) out[key] = incoming.headers[key];
    }
    // Forward streams immediately: buffering an SSE response until end prevents
    // the original renderer from receiving conversation and board updates.
    res.writeHead(incoming.statusCode || 502, out);
    res.flushHeaders();
    incoming.pipe(res);
    res.on('close', () => incoming.destroy());
  });
  upstream.on('error', () => {
    if (!res.headersSent) send(res, 502, 'application/json', JSON.stringify({ error: { message: 'Control plane is unavailable' } }));
    else res.destroy();
  });
  req.pipe(upstream);
  res.on('close', () => upstream.destroy());
}

const server = http.createServer(async (req, res) => {
  try {
    const url = requestUrl(req.url);
    if (!['GET', 'HEAD'].includes(req.method) && !sameOrigin(req)) return send(res, 403, 'text/plain', 'Origin is not allowed');
    if (url.pathname === '/fleet/nodes' && req.method === 'GET') {
      const response = await fetch(`http://${apiHost}:${apiPort}/internal/fleet/nodes`, { signal: AbortSignal.timeout(10000) });
      if (!response.ok) throw new Error('Cannot read fleet');
      const body = await response.json();
      return send(res, 200, 'application/json', JSON.stringify({ nodes: (body.nodes || []).map(publicNode) }));
    }
    const power = url.pathname.match(/^\/fleet\/nodes\/([^/]+)\/power$/);
    if (power && req.method === 'POST') return proxy(req, res, `/internal/fleet/nodes/${power[1]}/power`);
    if (url.pathname === '/readyz' && req.method === 'GET') {
      const response = await fetch(`http://${apiHost}:${apiPort}/readyz`, { signal: AbortSignal.timeout(10000) });
      const body = await response.json();
      return send(res, response.status, 'application/json', JSON.stringify({ ...body, port: apiPort }));
    }
    if (url.pathname === '/healthz' || url.pathname.startsWith('/api/v1/')) {
      return proxy(req, res, url.pathname + url.search);
    }
    if (url.pathname.startsWith('/internal') || url.pathname === '/shutdown') return send(res, 404, 'text/plain', 'Not found');
    if (!['GET', 'HEAD'].includes(req.method)) return send(res, 405, 'text/plain', 'Method not allowed');
    const relative = decodeURIComponent(url.pathname).replace(/^\/+/, '') || 'index.html';
    let target = resolve(staticRoot, relative);
    if (!target.startsWith(staticRoot + sep)) return send(res, 404, 'text/plain', 'Not found');
    try {
      const info = await stat(target);
      if (!info.isFile()) throw new Error('Not a file');
    } catch {
      if (extname(relative)) return send(res, 404, 'text/plain', 'Not found');
      target = resolve(staticRoot, 'index.html');
    }
    const content = await readFile(target);
    res.writeHead(200, { 'content-type': types[extname(target)] || 'application/octet-stream', 'cache-control': extname(target) === '.html' ? 'no-store' : 'public, max-age=3600' });
    res.end(req.method === 'HEAD' ? undefined : content);
  } catch {
    if (!res.headersSent) send(res, 502, 'application/json', JSON.stringify({ error: { message: 'Operator could not serve this request' } }));
    else res.destroy();
  }
});

server.on('upgrade', (req, socket, head) => {
  if (requestUrl(req.url).pathname !== '/mux' || !sameOrigin(req)) {
    socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
    return;
  }
  const upstream = net.connect(apiPort, apiHost);
  upstream.once('connect', () => {
    const headers = ['GET /mux HTTP/1.1', `Host: ${apiHost}:${apiPort}`, 'Connection: Upgrade', 'Upgrade: websocket'];
    for (const [key, value] of Object.entries(req.headers)) {
      if (key.startsWith('sec-websocket-')) headers.push(`${key}: ${value}`);
    }
    upstream.write(headers.join('\r\n') + '\r\n\r\n');
    if (head.length) upstream.write(head);
    upstream.pipe(socket);
    socket.pipe(upstream);
  });
  const close = () => { socket.destroy(); upstream.destroy(); };
  socket.on('error', close); upstream.on('error', close);
  socket.on('close', () => upstream.destroy()); upstream.on('close', () => socket.destroy());
});
server.listen(port, host);
