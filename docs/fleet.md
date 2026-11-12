# Fleet

A fleet has one control plane and any number of workers. The control plane keeps the board, the schedule, and pull-request facts. A worker runs the existing session engine on its own disk and dials in. The product ships with no host names, addresses, or power commands.

## Roles

- `control` binds `AO_FLEET_LISTEN` and accepts worker dial-ins. It does not run coding agents. The loopback API stays on `127.0.0.1` and is not the fleet bus.
- `worker` dials `AO_FLEET_CONTROL_URL` with its enrollment token. It builds worktrees and starts agents locally.
- A worker may also be a power broker. It only runs the wake and shutdown commands stored for another node. Those commands are deployment configuration.

## Enrollment

On the control plane:

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

`ao fleet nodes` shows enrolled nodes and whether their last heartbeat is inside the offline window.

## Placement

- Omitting `--node` on `ao spawn` keeps the session on the daemon you called.
- `--node <id>` pins it to that worker. An on-demand node is woken through its configured broker and is not eligible for automatic choice.
- `--platform <os>` limits automatic choice to a worker that offers that family.
- A session's directory stays on the node that created it. A lost heartbeat marks the placement `offline`. It does not terminate the session and does not move the directory.
- An orchestrator session ignores those choices. Its project config must set both `orchestratorHomeNode` and `orchestratorCheckout`. The checkout is a long-lived directory on that one node.

## Power

An on-demand node names its broker and the commands that broker should run. The control plane sends that configured command when a session names the node, or when an operator runs `ao fleet power <id> wake`. `ao fleet power <id> shutdown` is refused while the node still owns a session. The broker executes the command on its own machine; the product has no built-in power protocol. A worker only accepts power commands when `AO_FLEET_POWER_BROKER=on`.

## What is not included

Terminal bytes and chat events are not yet proxied through the control plane. A pinned spawn is delivered to the worker, the worker starts it in its own engine, and the control plane records that engine's session id on the placement. Opening the terminal from the control plane is the next slice.
