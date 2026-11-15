# Portal renderer

The portal serves the existing Orchestrator React renderer. It uses the same shell,
sidebar, board, composer, conversation, and xterm components as the desktop build.
`VITE_AO_WEB=1` enables the private browser interface and a real connection to the service host;
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

Human work enters through each project's long-lived coordinator conversation.
Initial choices are the service host's Agent, model and thinking level; the
coordinator chooses subordinate workers and their Agents. Manual worker spawn,
delegate, speculative workspace preparation and direct automation creation are
blocked at the portal boundary. They remain daemon/CLI capabilities for the
coordinator. The service-host daemon should use `AO_CLOUD_OFFERING=off` as well;
private web independently hides SaaS sign-in and native-only controls.

Folder and workspace selection uses the daemon's filesystem browser and import
validation. Clone destinations refer to the service host. Local-only Git project
import does not create or publish a GitHub repository. Agent installation and
provider authorization remain manual on each machine. Browser preferences persist
with validation, and unsupported Browser previews never display fabricated page
content or passing tests. The renderer uses real data and creates no samples.

Worker daemons must be upgraded alongside the service host. The control plane
requires the advertised launch protocol version before dispatching full launch
settings, so an older worker cannot silently ignore a model or thinking level.
Each intended worker needs the same logical project registered with its own local
checkout; the UI and coordinator report missing prerequisites without installing,
cloning or transferring credentials.

The Go executable alone does not include Claude's Chat adapter. Build the original
runtime with `npm --prefix frontend run build:acp-runtime`, deploy its complete
`resources/acp-runtime` directory with a Node binary for each target platform, and
set `AO_ACP_RUNTIME_DIR` to that directory in the daemon service. The original build
pins and checksums Node, patches the locked adapter, and excludes provider Agent
executables. This installs AO infrastructure, not Claude or provider credentials.
A missing adapter returns an explicit failed placement, never a successful task.
Pi and other native Chat integrations still use their already-installed Agents.

Verification:

```sh
node --test deploy/portal-operator/server.test.mjs
cd frontend
npm run typecheck
npm test -- src/renderer/lib/web-mode.test.ts src/renderer/lib/api-client.test.ts src/renderer/hooks/useDaemonStatus.test.tsx
```

Proxy tests cover readiness, the application prefix, secret filtering, blocked
operator plumbing, streamed SSE, upstream sandbox policies, origin checks, and
terminal upgrade bytes. Live browser checks cover startup, Fleet inventory, server
folder browsing, no-origin local project import, coordinator configuration and
actual xterm WebSocket frames. A separate isolated control/worker test launched a
real coordinator, let it select and launch a Claude Chat worker, transferred the
worker's durable completion report, and observed the coordinator acknowledge it.
That test used separate daemon data directories on one host; deployment checks
must separately verify the installed workers on their actual machines.

Completed reports retain AO's existing five-minute settlement window. Transfer
receipts acknowledge semantic delivery only after that window, and retries reuse
the same receipt. The coordinator is instructed to finish its turn after dispatch
and resume when the report arrives, rather than hold a polling shell open.
