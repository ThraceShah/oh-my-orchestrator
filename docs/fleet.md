# Fleet

A fleet has one service host and any number of workers. The service host keeps the project registry, coordinator conversations, board and placement facts. Each worker runs the existing session engine on its own disk and dials in. The product contains no deployment hostnames, addresses or power commands.

## Roles

- `control` binds `AO_FLEET_LISTEN`, accepts worker dial-ins and runs the human-facing project coordinators. A coordinator can read its local project checkout, perform lightweight work and run focused checks. Substantial implementation, builds and tests belong on workers. The control node is never an eligible worker.
- `worker` dials `AO_FLEET_CONTROL_URL` with its enrollment token. It builds worktrees and starts subordinate agents locally.
- A worker can also be a power broker. It executes the configured wake or shutdown command for another node. Those commands are deployment configuration.

The primary daemon API remains unauthenticated on `127.0.0.1`. The separately configured fleet connection is not that API listener.

## Enrollment

On the service host:

```bash
AO_FLEET_ROLE=control AO_FLEET_LISTEN=127.0.0.1:7443 ao daemon
ao fleet enroll worker-a "Worker A" --role worker --availability always-on --capacity 2 --platform linux
```

The token prints once. On the worker:

```bash
AO_FLEET_ROLE=worker \
AO_FLEET_NODE_ID=worker-a \
AO_FLEET_CONTROL_URL=ws://127.0.0.1:7443/fleet/v1/connect \
AO_FLEET_TOKEN=<token> \
ao daemon
```

Use a protected environment file in persistent deployments. Do not put enrollment tokens in project configuration, the browser bundle or logs. Agent installation and provider authorization are performed on each machine; credentials remain there.

## Human conversation and coordinator locality

Each project has one active, long-lived coordinator. In the private web interface, adding or opening a project leads to that conversation. The human chooses its service-host Agent, model and thinking level when it starts. Work is proposed through messages; the coordinator decides whether to continue an existing task or create a subordinate task.

The portal does not expose direct worker creation through session creation, delegate or speculative task preparation APIs. Existing creation routes and shortcuts in the private renderer open the project's coordinator instead. Coordinator creation uses the project singleton lock and a durable client request identity for its opening message. A concurrent creation that finds another coordinator preserves the draft and reports the existing conversation instead of silently dropping or duplicating it.

The coordinator uses the registered project directory, or an explicitly configured local `orchestratorCheckout`. The directory is not an AO-owned worktree: stopping, replacing, rolling back or cleaning up the coordinator preserves it and its uncommitted files. Legacy `orchestratorHomeNode` configuration requires explicit migration rather than silently interpreting a remote path as a local one.

## Discovering worker capabilities

The coordinator uses the existing CLI and daemon boundaries:

```bash
ao fleet nodes --json
ao fleet readiness --node worker-a --agent pi --project my-project --refresh --json
ao fleet models --node worker-a --agent pi --project my-project --json
```

The application API exposes sanitized node summaries and worker-scoped readiness/model queries under `/api/v1/fleet/nodes`. Summaries distinguish reported installation, authorization, configured credentials, supported interfaces, freshness, capacity and occupied slots. A recent heartbeat does not make an old native authentication check fresh. Missing or unknown observations are not proof of readiness.

An installed AGY executable does not prove that native browser/keyring sign-in is complete. Its authentication remains unknown when no reliable non-interactive status check exists; the UI does not label installation as authorization.

Each worker must have the corresponding logical project registered with its own local path. Model discovery and launch run in that worker's environment; a missing project, executable or authorization is reported rather than substituting the service host's path or credentials. These APIs do not install agents or initiate login.

## Placement and identity

- Only worker sessions use fleet placement. A coordinator executes on the service host and rejects remote node/platform options.
- The coordinator can select `ao spawn --node <id> --agent <agent> --model <model> --effort <level>`. Omitting a node chooses an eligible online always-on worker. On-demand nodes require explicit selection.
- `--platform <os>` constrains candidate platforms, including when a node is explicit.
- Project defaults and parent coordinator permissions are resolved centrally; the worker receives the full launch request, including model, effort, permission, attachment and idempotency facts. It validates the actual runtime locally. A worker-only Agent need not be installed on the service host.
- The global session ID remains stable. The owning node and its local engine ID are internal placement facts. Parent relationships survive dispatch and reconnect.
- A lost heartbeat marks placement offline. It does not prove that a task stopped, release its occupied slot or move its directory. The local service-host reaper never owns remote runtimes.
- Durable dispatch payloads and worker request receipts prevent ordinary retries from creating duplicate sessions. A pending on-demand launch resumes after its worker connects.

## Supervision and reports

List and detail reads refer to the same global identity. Connected worker session reads provide actual activity and delivery facts; unavailable reads retain placement metadata with an explicit unavailable state, rather than inventing idle, success or termination.

Messages and supported lifecycle actions route to the owning worker. Worker-to-coordinator messages use the authenticated fleet connection and the session's project/ownership relation. Report transfer reads the worker's durable outbox, writes a deduplicated service-host inbox before acknowledging the worker, and uses existing semantic report delivery to notify the parent coordinator.

## Power

An on-demand node names its broker and configured commands. An explicit placement can request wake through that broker. Operators can also run `ao fleet power <id> wake`. Shutdown is refused while the node owns unfinished sessions, including disconnected sessions. The product has no built-in power protocol. A broker only accepts power commands when `AO_FLEET_POWER_BROKER=on`.

## Chat and terminals

The ordinary `/mux` recognizes `fleet-term:` handles and attaches to the existing worker runtime. It never starts a second agent. Session-addressed chat operations use the same worker's chat service; conversation updates publish a live hint using the global session identity. Such hints are not durable CDC cursors.

## Private web interface

The portal builds the original Orchestrator React renderer and preserves its shell, project board, conversation, terminal and settings primitives. It removes AO SaaS account, billing, team and hosted execution entry points. Existing portal authentication protects access; the app adds no separate user account system.

The Fleet dialog separates service-host coordinator Agents from worker inventories and explains missing or stale prerequisites. Native-only actions are hidden. Server folder/project selection uses daemon validation; clone destinations refer to the service host. Browser preferences persist locally with validation. The Browser panel never fabricates a loaded page or passing tests when no real browser runtime exists.

Enrollment, internal control routes and daemon shutdown are not forwarded. See [the portal deployment guide](../deploy/portal-operator/README.md) for build, proxy and verification details.
