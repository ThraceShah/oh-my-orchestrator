package sessionmanager

import (
	"fmt"
	"os"
	"strings"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// existingCheckoutWorkspace reports the orchestrator's long-lived directory.
// ok is false when the spawn did not ask for one, so the caller creates a
// worktree as usual. A requested checkout is never replaced by a new worktree.
func existingCheckoutWorkspace(cfg ports.SpawnConfig, id domain.SessionID) (ports.WorkspaceInfo, bool, error) {
	checkout := strings.TrimSpace(cfg.ExistingCheckout)
	if checkout == "" {
		return ports.WorkspaceInfo{}, false, nil
	}
	if cfg.Kind != domain.KindOrchestrator {
		return ports.WorkspaceInfo{}, true, fmt.Errorf("an existing checkout is only valid for an orchestrator session")
	}
	info, err := os.Stat(checkout)
	if err != nil {
		return ports.WorkspaceInfo{}, true, fmt.Errorf("orchestrator checkout: %w", err)
	}
	if !info.IsDir() {
		return ports.WorkspaceInfo{}, true, fmt.Errorf("orchestrator checkout %s is not a directory", checkout)
	}
	branch := strings.TrimSpace(cfg.Branch)
	if branch == "" {
		branch = "HEAD"
	}
	return ports.WorkspaceInfo{
		Path: checkout, RepoPath: checkout, Branch: branch, SessionID: id, ProjectID: cfg.ProjectID,
	}, true, nil
}
