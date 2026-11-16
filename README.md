# Oh My Orchestrator

**A private, cross-device coordinator for coding agents.** Keep one long-lived coordinator with a project's checkout on your service host, then let it delegate substantial work to agents running on your own machines.

[简体中文](translations/README.zh-CN.md) · [Fleet setup](docs/fleet.md) · [Development guide](docs/development.md) · [Issues](https://github.com/ThraceShah/oh-my-orchestrator/issues)

This is an independently maintained community fork of [Orchestrator.inc](https://github.com/OrchestratorInc/agent-orchestrator). It reuses the original React interface and adds a private web workflow for service-host coordinators and remote workers. It is not affiliated with or endorsed by OrchestratorInc. The upstream desktop downloads do not contain this fork's changes.

## How it works

```text
You ── chat with ──> Project coordinator (service host)
                             │
                       chooses worker,
                       Agent and model
                             │
            ┌────────────────┴────────────────┐
            ▼                                 ▼
      Worker machine                    Worker machine
      isolated worktree                 isolated worktree
            └────────────────┬────────────────┘
                             │ durable report
                             └────────> Coordinator
```

- **One coordinator per project.** It stays tied to the registered project checkout on the service host. Stopping or replacing the coordinator preserves that source directory and its uncommitted files.
- **Work starts as a conversation.** Describe what you want in the coordinator chat. The coordinator decides whether to continue current work or start a subordinate task; the web interface does not let people create worker tasks directly.
- **You choose the coordinator setup.** Select its service-host Agent, model and thinking level. The coordinator chooses worker machines and their Agent, model and effort based on reported capabilities and project readiness.
- **Workers keep their own runtime and credentials.** Each worker creates its worktree and runs its Agent locally. Completion reports return to the parent coordinator with stable session identity and deduplication.
- **No provider is installed or signed in for you.** Install and authorize each provider Agent yourself on the machine where it will run. Credentials are not copied between hosts.

## Private web mode

The web interface uses the original Orchestrator renderer, board, chat and terminal. It adds project coordinators, worker discovery and server-side project browsing. It does not require an AO SaaS account and hides account, billing, team and hosted-execution entry points. In a portal deployment, access is protected by the portal's existing authentication.

The daemon's primary API stays on loopback. Workers connect to the separately configured fleet endpoint; keep that endpoint on a trusted private network. Do not expose the daemon API or the portal operator directly to the public internet. See [Fleet setup and security notes](docs/fleet.md) and the [portal deployment guide](deploy/portal-operator/README.md).

## Build from source

This repository currently publishes source code, not installable desktop or server releases. The binaries linked by the upstream project are not builds of this fork.

### Requirements

- Go 1.27.4 or newer
- Node.js 24 and npm 10
- Git
- A provider Agent CLI installed and authorized separately on each host that will run it

### Build the daemon

```bash
git clone https://github.com/ThraceShah/oh-my-orchestrator.git
cd oh-my-orchestrator
cd backend
go build -o ../out/ao ./cmd/ao
```

### Build and run the private web interface

The web build is the original renderer configured for the real private-web daemon. `dev:web` is a fixture preview and is not a production server.

```bash
cd frontend
npm ci --ignore-scripts
npm run build:web
cd ../deploy/portal-operator
HOST=127.0.0.1 PORT=3012 FLEET_API_PORT=3011 node server.mjs
```

Run the daemon separately under a persistent process manager. For a private deployment, set `AO_CLOUD_OFFERING=off`, configure the control role and fleet listener, and place the portal operator behind an authenticated reverse proxy. The service must keep the daemon API on loopback and must not expose the operator's loopback port directly. See the [portal deployment guide](deploy/portal-operator/README.md) for the complete configuration.

## Add worker machines

1. Install and authorize the Agent CLIs you intend to use on each worker.
2. Enroll the worker from the service host with `ao fleet enroll`. The enrollment token is shown once; store it in a protected environment file.
3. Start the worker daemon with `AO_FLEET_ROLE=worker`, its node ID, the service host's private fleet URL and the token.
4. Register the same logical project ID on each worker, pointing to that worker's own checkout. The project IDs must match across hosts; paths remain local to each machine.
5. Check fresh, project-scoped Agent readiness before assigning work.

For command examples and configuration options, see [docs/fleet.md](docs/fleet.md). The coordinator reports offline workers, stale authorization and missing project registrations as unmet prerequisites; it does not install providers or transfer credentials.

## Development checks

```bash
# Backend
(cd backend && go test ./...)

# Frontend
npm run frontend:typecheck
npm --prefix frontend test

# Portal reverse proxy
node --test deploy/portal-operator/server.test.mjs
```

See [docs/development.md](docs/development.md) for the full development setup. The fleet implementation and private web deployment have additional focused validation notes in [docs/private-coordinator-validation.md](docs/private-coordinator-validation.md).

## Scope and status

- The service-host coordinator and fleet protocol are intended for deployment by the project owner on machines they control.
- The private web flow is source-only at present; no ready-made public binary release is provided for this fork.
- Windows worker deployment has not been validated for this fork. Treat a registered but offline node as unavailable.
- Existing desktop and cloud-related upstream code remains in the source tree. The private web interface described here does not provide AO SaaS login, billing or hosted execution.

## License

Apache-2.0. See [LICENSE](LICENSE) for the license text and inherited notices.
