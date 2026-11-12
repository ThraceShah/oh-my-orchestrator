// Package registry is the single source of truth for the agent adapters the
// daemon ships. The daemon wires sessions through it, so adding a harness is a
// single edit to Constructors rather than a list maintained in several places.
package registry

import (
	"fmt"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/agy"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/aider"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/amp"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/auggie"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/autohand"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/cline"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/codewhale"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/commandcode"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/continueagent"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/copilot"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/crush"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/deepseekharness"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/devin"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/droid"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/fx"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/gemini"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/goose"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/grok"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/hermes"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/kilocode"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/kimchi"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/kimi"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/kiro"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/memcode"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/mimocode"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/muse"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/omp"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/opencodev2"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/openhands"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/pi"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/primeagent"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/qwen"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/unrealagent"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/vibe"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// Constructors returns a fresh instance of every agent adapter the daemon
// ships, in a stable registration order. Adding a new harness means adding its
// constructor here (and a domain.AgentHarness constant) — the one edit the
// daemon picks up.
func Constructors() []adapters.Adapter {
	return []adapters.Adapter{
		claudecode.New(),
		codex.New(),
		opencode.New(),
		opencodev2.New(),
		grok.New(),
		cursor.New(),
		qwen.New(),
		gemini.New(),
		copilot.New(),
		kimi.New(),
		muse.New(),
		droid.New(),
		amp.New(),
		agy.New(),
		crush.New(),
		aider.New(),
		goose.New(),
		auggie.New(),
		continueagent.New(),
		devin.New(),
		omp.New(),
		codewhale.New(),
		openhands.New(),
		cline.New(),
		kiro.New(),
		kilocode.New(),
		vibe.New(),
		pi.New(),
		kimchi.New(),
		primeagent.New(),
		autohand.New(),
		fx.New(),
		commandcode.New(),
		hermes.New(),
		unrealagent.New(),
		mimocode.New(),
		memcode.New(),
		deepseekharness.New(),
	}
}

// Build returns a registry populated with the shipped agent adapters, keyed by
// manifest id. Registration only fails on an empty/duplicate id — a programmer
// error, not a runtime condition.
func Build() (*adapters.Registry, error) {
	reg := adapters.NewRegistry()
	for _, a := range Constructors() {
		if err := reg.Register(a); err != nil {
			return nil, fmt.Errorf("register agent adapter %q: %w", a.Manifest().ID, err)
		}
	}
	return reg, nil
}

// HarnessAgent pairs a session harness with the adapter that drives it. The
// harness is the adapter's manifest id, which is also the domain.AgentHarness
// value a session carries and the `--harness` flag users pass.
type HarnessAgent struct {
	Harness  domain.AgentHarness
	Manifest adapters.Manifest
	Agent    ports.Agent
}

// Harnessed returns every shipped adapter that drives an agent, paired with its
// harness, in Constructors() order. An adapter that does not implement
// ports.Agent is skipped.
func Harnessed() []HarnessAgent {
	cons := Constructors()
	out := make([]HarnessAgent, 0, len(cons))
	for _, a := range cons {
		agent, ok := a.(ports.Agent)
		if !ok {
			continue
		}
		out = append(out, HarnessAgent{
			Harness:  domain.AgentHarness(a.Manifest().ID),
			Manifest: a.Manifest(),
			Agent:    agent,
		})
	}
	return out
}
