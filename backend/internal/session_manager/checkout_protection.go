package sessionmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// protectedCheckout distinguishes a source checkout from an AO-owned worktree.
// Existing checkout launches persist RepoPath == Path, so protection survives
// both a daemon restart and a later change to the project's configured path.
// Registry paths also protect sessions created before that fact was recorded.
func (m *Manager) protectedCheckout(ctx context.Context, ws ports.WorkspaceInfo) (bool, error) {
	if strings.TrimSpace(ws.Path) == "" {
		return false, nil
	}
	if sameCheckoutPath(ws.Path, ws.RepoPath) {
		rec, found, err := m.store.GetSession(ctx, ws.SessionID)
		if err != nil {
			return false, err
		}
		if found && rec.Kind == domain.KindOrchestrator {
			return true, nil
		}
	}
	if ws.ProjectID == "" {
		return false, nil
	}
	project, err := m.loadProject(ctx, ws.ProjectID)
	if err != nil {
		return false, err
	}
	return sameCheckoutPath(ws.Path, project.Path) || sameCheckoutPath(ws.Path, project.Config.OrchestratorCheckout), nil
}

func sameCheckoutPath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	canonical := func(path string) string {
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
		if physical, err := filepath.EvalSymlinks(path); err == nil {
			path = physical
		}
		return normalizeWorkspacePath(path)
	}
	return canonical(a) == canonical(b)
}

// restoreProtectedCheckout only attaches to the recorded directory. It must
// never recreate, reset, provision, or apply a stash to a user's source tree.
func restoreProtectedCheckout(rec domain.SessionRecord) (ports.WorkspaceInfo, error) {
	info, err := os.Stat(rec.Metadata.WorkspacePath)
	if err != nil {
		return ports.WorkspaceInfo{}, fmt.Errorf("source checkout: %w", err)
	}
	if !info.IsDir() {
		return ports.WorkspaceInfo{}, fmt.Errorf("source checkout is not a directory")
	}
	return workspaceInfo(rec), nil
}

// stopProtectedCheckout terminates the controller while retaining all files.
// Shutdown keeps an active source-directory row as its restore marker; it does
// not claim that the directory was removed or captured in a stash.
func (m *Manager) stopProtectedCheckout(ctx context.Context, rec domain.SessionRecord, restore bool) error {
	if domain.NormalizeSessionMode(rec.Mode) == domain.SessionModeChat {
		m.stopChatBestEffort(ctx, rec.ID)
	} else if handle := runtimeHandle(rec.Metadata); handle.ID != "" {
		if err := m.runtime.Destroy(ctx, handle); err != nil {
			return err
		}
	}
	if err := m.teardownReviewerTerminal(ctx, rec.ID); err != nil {
		return err
	}
	if restore {
		if err := m.store.UpsertSessionWorktree(ctx, domain.SessionWorktreeRecord{
			SessionID: rec.ID, RepoName: domain.RootWorkspaceRepoName,
			Branch: rec.Metadata.Branch, WorktreePath: rec.Metadata.WorkspacePath, State: "active",
		}); err != nil {
			return err
		}
		if err := m.lcm.MarkTerminated(ctx, rec.ID); err != nil {
			return err
		}
	} else if err := m.recordTermination(ctx, rec.ID, true); err != nil {
		return err
	}
	return m.recordWorkspaceCleanup(ctx, rec, domain.DispositionNotApplicable, "", false)
}
