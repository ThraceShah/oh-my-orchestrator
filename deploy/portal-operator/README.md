# Portal renderer

The portal serves the existing Orchestrator React renderer. It uses the same shell,
sidebar, board, composer, conversation, and xterm components as the desktop build.
`VITE_AO_WEB=1` enables a real browser connection to the control plane;
`VITE_NO_ELECTRON=1` is preview/mock mode and must not be used for deployment.

Build from the repository:

```sh
cd frontend
npm ci --ignore-scripts
npm run build:web
```

The output is `deploy/portal-operator/dist` (ignored by Git). The default resource
prefix is `/apps/orchestrator/`; set `AO_WEB_BASE` before building for another
prefix. Hash routes preserve the resource/API prefix on reload. No hostname or
credentials are embedded in the bundle.

Run with a process manager on the control-plane host:

```sh
HOST=127.0.0.1 PORT=3012 FLEET_API_PORT=3011 node server.mjs
```

Keep `dist` next to `server.mjs`, or set `OPERATOR_DIST` to the absolute release
directory. The primary daemon remains on loopback. Register the operator's
loopback port with the portal using `stripPrefix=true`. The portal's existing
login boundary protects the application; this server is not a standalone public
listener and must not be exposed directly.

The proxy streams API/SSE responses, keeps upstream content sandbox policies,
and tunnels `/mux` to the daemon. `/readyz` checks the actual daemon rather than
reporting only that the web process is alive. Browser mutation and terminal
requests must have the same origin. `/internal/*`, fleet enrollment, and daemon
shutdown are not forwarded. Fleet node summaries omit credentials and power
commands. The Fleet dialog uses the original sidebar and dialog primitives.

Native file pickers and opening local editors require the desktop application.
The web home disables those picker actions and hides host import/update prompts.
Cloning supports entering a destination on the server. The browser renders the
control plane's real data; it does not create sample projects or sessions.

Verification:

```sh
node --test deploy/portal-operator/server.test.mjs
cd frontend
npm run typecheck
npm test -- src/renderer/lib/web-mode.test.ts src/renderer/lib/api-client.test.ts src/renderer/hooks/useDaemonStatus.test.tsx
```

Proxy tests cover readiness, the application prefix, secret filtering, blocked
operator plumbing, streamed SSE, upstream sandbox policies, origin checks, and
terminal upgrade bytes. Live browser checks cover startup, Fleet node listing,
and navigation into the original new-task composer. These checks do not launch
an agent or prove a complete remote Chat/terminal task lifecycle.
