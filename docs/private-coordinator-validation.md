# Private coordinator validation

The private web build uses the original React renderer with `VITE_AO_WEB=1` and `VITE_NO_ELECTRON=0`. Preview fixture mode is a separate build.

## Automated checks

- Full frontend suite with the repository CI's Node 24 runtime: 749 files passed, 13,346 tests passed, 10 skipped. The final coordinator button labels were subsequently checked in all 85 relevant component tests and by typecheck.
- Full affected backend packages: HTTP/spec/controllers, importer, project, session service, fleet, daemon, CLI, SQLite/store, session manager, reports, domain and Git worktrees.
- Pinned golangci-lint v2.14.0 with the repository configuration.
- All six portal proxy tests, including streamed SSE, same-origin enforcement, secret filtering, blocked manual worker creation and terminal upgrade bytes.
- Real Git lifecycle tests protect clean and dirty coordinator source directories through failure rollback, stop, replacement, cleanup and shutdown/restore. Local repositories record their initial base branch once, so later checkouts cannot change a worker's starting point.
- Real migrated SQLite report tests cover local worker session/report/output foreign keys, transfer into a central durable inbox, exact parent delivery, semantic retry, restart and deduplication without a shadow coordinator on the worker.

The repository-wide backend command was also run. It exposed pre-existing failures in Codex account security tests on this environment, a fake-agent hook-log test, an integration test using that account setup, and a shell history test requiring an unavailable zsh. These were reproduced on the original UI deployment commit `72418f7`. The stale GitHub owner fixture discovered in the project package was corrected to match the URL already present in that fixture; the entire affected project suite passes.

## Real browser and runtime check

An isolated service host and an isolated worker ran the built executable with different data directories and authenticated fleet transport on loopback. This verifies two daemon roles on one machine, rather than claiming separate physical machines.

The browser added a committed local repository without an origin, using the real server directory picker and import validation. It reached coordinator Agent/model/effort selection without a GitHub publication step. The first message created exactly one local coordinator. The real provider read the README and returned the requested response.

The first review exposed an inherited `!window.ao` terminal-preview branch. The live web build now attaches the existing xterm/mux implementation. The browser observed real prefixed WebSocket frames, no renderer exceptions and no example terminal transcript.

A daemon restart restored the coordinator and preserved the clean source directory. The coordinator subsequently selected a worker and Claude Chat model itself. Initial failures revealed missing packaged ACP infrastructure and the absent explicit base branch for a local-only repository. The release now includes the original locked ACP adapter with checksum-verified Node for each target, and new local project registrations persist their actual symbolic base branch. Failed placement reads show the recorded failure rather than pretending to be starting. A real remote stop returned acknowledgment.

The final subordinate task read the worker checkout and submitted `worker report reached coordinator`. The global session retained the owning node and parent coordinator while the worker kept its local engine identity. The report appeared once in central history, its durable inbox receipt was acknowledged after the existing five-minute settlement window, and the coordinator inspected the worker and acknowledged the exact report in its terminal conversation. Source Git status remained clean.

## Explicit limits

- `ao preview` cannot attach from this DSH session because `AO_SESSION_ID` is unset. Verification used real Chromium and the actual built web server; no AO identity was fabricated.
- Agent and provider installation/login remain manual. Packaging AO's Chat adapter does not install a provider Agent or copy credentials between machines.
- An offline enrolled Windows node without a worker daemon is not a verified worker. Wake configuration alone is not proof of successful installation, launch or shutdown.
- Physical fleet rollout and HTTPS verification are separate deployment steps; the isolated check above does not substitute for them.
