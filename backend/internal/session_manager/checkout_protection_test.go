package sessionmanager

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

// This adapter deliberately performs real destructive operations if reached.
// Its roots are exclusively t.TempDir repositories: a regression really loses
// the file (or stashes it), rather than passing against a non-mutating fake.
type checkoutSafetyWorkspace struct {
	*fakeWorkspace
	t                *testing.T
	root             string
	destructiveCalls int
}

func (w *checkoutSafetyWorkspace) Destroy(_ context.Context, info ports.WorkspaceInfo) error {
	w.t.Helper()
	if filepath.Clean(info.Path) != w.root {
		w.t.Fatalf("unexpected teardown path %q", info.Path)
	}
	w.destructiveCalls++
	return os.RemoveAll(w.root)
}
func (w *checkoutSafetyWorkspace) ForceDestroy(ctx context.Context, info ports.WorkspaceInfo) error {
	return w.Destroy(ctx, info)
}
func (w *checkoutSafetyWorkspace) DestroyReclaim(ctx context.Context, info ports.WorkspaceInfo) (ports.WorkspaceReclaim, error) {
	return ports.WorkspaceReclaimRemoved, w.Destroy(ctx, info)
}
func (w *checkoutSafetyWorkspace) StashUncommitted(_ context.Context, info ports.WorkspaceInfo) (string, error) {
	if filepath.Clean(info.Path) != w.root {
		w.t.Fatalf("unexpected stash path %q", info.Path)
	}
	w.destructiveCalls++
	return "", exec.Command("git", "-C", w.root, "stash", "push", "--include-untracked").Run()
}

func TestCoordinatorCheckoutSurvivesLifecycle(t *testing.T) {
	for _, source := range []string{"registered", "explicit", "changed-config"} {
		for _, dirty := range []bool{false, true} {
			for _, operation := range []string{"kill", "replacement", "shutdown-restart", "rollback", "cleanup", "pending-cleanup"} {
				name := source + "/" + operation + "/clean"
				if dirty {
					name = source + "/" + operation + "/dirty"
				}
				t.Run(name, func(t *testing.T) {
					root := newManagerGitRepo(t)
					want := "hello\n"
					if dirty {
						want = "uncommitted source work\n"
						if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(want), 0600); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("keep me"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					before := runManagerGit(t, root, "status", "--porcelain")
					m, st, rt, ws := newLifecycleManager()
					m.dataDir = t.TempDir()
					guard := &checkoutSafetyWorkspace{fakeWorkspace: ws, t: t, root: root}
					m.workspace = guard
					project := st.projects["mer"]
					project.Path = root
					project.Config.PreRemove = []string{"touch cleanup-ran"}
					if source != "registered" {
						project.Path = newManagerGitRepo(t)
						project.Config.OrchestratorCheckout = root
					}
					if source == "changed-config" {
						project.Config.OrchestratorCheckout = t.TempDir()
					}
					st.projects["mer"] = project
					rec := domain.SessionRecord{ID: "mer-orch", ProjectID: "mer", Kind: domain.KindOrchestrator, Harness: domain.HarnessClaudeCode, Mode: domain.SessionModeTUI,
						Metadata: domain.SessionMetadata{WorkspacePath: root, WorkspaceRepoPath: root, Branch: "main", RuntimeHandleID: "h1", Prompt: "continue coordinating"},
						Activity: domain.Activity{State: domain.ActivityActive},
					}
					// Legacy rows still receive registry/config path protection.
					if source != "changed-config" {
						rec.Metadata.WorkspaceRepoPath = ""
					}
					st.sessions[rec.ID] = rec
					switch operation {
					case "kill":
						freed, err := m.Kill(context.Background(), rec.ID)
						if err != nil || freed {
							t.Fatalf("kill freed=%v err=%v", freed, err)
						}
					case "replacement":
						if err := m.RetireForReplacement(context.Background(), rec.ID); err != nil {
							t.Fatal(err)
						}
					case "shutdown-restart":
						if err := m.saveAndTeardownOne(context.Background(), rec); err != nil {
							t.Fatal(err)
						}
						rows := st.worktrees[rec.ID]
						if len(rows) != 1 || rows[0].State != "active" {
							t.Fatalf("shutdown marker=%+v", rows)
						}
						// Simulate a new manager process: protection cannot rely on in-memory
						// spawn configuration or any ownership cache.
						next, _, _, _ := newLifecycleManager()
						next.store, next.workspace, next.runtime = st, guard, rt
						next.lcm = &fakeLCM{store: st}
						next.dataDir = m.dataDir
						if err := next.RestoreAll(context.Background()); err != nil {
							t.Fatal(err)
						}
						if st.sessions[rec.ID].IsTerminated {
							t.Fatal("protected checkout coordinator was not restored")
						}
						if len(ws.restoreConfigs) != 0 {
							t.Fatalf("source checkout passed to worktree Restore: %+v", ws.restoreConfigs)
						}
					case "rollback":
						m.rollbackSeedSpawnWorkspace(context.Background(), rec, workspaceInfo(rec), nil, false, false)
					case "cleanup", "pending-cleanup":
						rec.IsTerminated = true
						st.sessions[rec.ID] = rec
						if operation == "pending-cleanup" {
							if err := m.recordWorkspaceCleanup(context.Background(), rec, domain.DispositionPending, "", false); err != nil {
								t.Fatal(err)
							}
							if err := m.reconcileWorkspaceCleanup(context.Background(), rec.ID); err != nil {
								t.Fatal(err)
							}
						} else {
							result, err := m.Cleanup(context.Background(), "mer")
							if err != nil {
								t.Fatal(err)
							}
							if len(result.Cleaned) != 0 || len(result.AlreadyGone) != 0 {
								t.Fatalf("source was reported removed: %+v", result)
							}
						}
					}
					if guard.destructiveCalls != 0 {
						t.Fatalf("source reached destructive adapter %d times", guard.destructiveCalls)
					}
					got, err := os.ReadFile(filepath.Join(root, "README.md"))
					if err != nil || string(got) != want {
						t.Fatalf("checkout changed: %q err=%v", got, err)
					}
					if after := runManagerGit(t, root, "status", "--porcelain"); after != before {
						t.Fatalf("git state changed: before=%q after=%q", before, after)
					}
					if _, err := os.Stat(filepath.Join(root, "cleanup-ran")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("cleanup script ran on source: %v", err)
					}
					if operation != "rollback" {
						facts, ok, err := st.GetSessionCleanupFacts(context.Background(), rec.ID)
						// Relaunch starts a fresh cleanup generation; other terminal operations
						// must durably say not_applicable, never 'removed' or 'preserved_dirty'.
						if operation != "shutdown-restart" && (err != nil || !ok || facts.WorkspaceDisposition != domain.DispositionNotApplicable) {
							t.Fatalf("cleanup facts=%+v found=%v err=%v", facts, ok, err)
						}
					}
				})
			}
		}
	}
}

func TestExistingCheckoutFailedSpawnDoesNotDestroySource(t *testing.T) {
	root := newManagerGitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("dirty source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, st, rt, ws := newLifecycleManager()
	m.dataDir = t.TempDir()
	project := st.projects["mer"]
	// An explicitly supplied source may differ from today's project config;
	// rollback must use the persisted source-path fact, not only the registry.
	project.Path = newManagerGitRepo(t)
	st.projects["mer"] = project
	guard := &checkoutSafetyWorkspace{fakeWorkspace: ws, t: t, root: root}
	m.workspace = guard
	rt.createErr = errors.New("runtime launch failed")
	_, _, _, err := m.Spawn(context.Background(), ports.SpawnConfig{ProjectID: "mer", Kind: domain.KindOrchestrator, Harness: domain.HarnessClaudeCode, ExistingCheckout: root, RequestedMode: domain.SessionModeTUI})
	if err == nil || !strings.Contains(err.Error(), "runtime launch failed") {
		t.Fatalf("want runtime failure, got %v", err)
	}
	if guard.destructiveCalls != 0 {
		t.Fatalf("source reached destructive adapter %d times", guard.destructiveCalls)
	}
	got, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(got) != "dirty source\n" {
		t.Fatalf("failed spawn changed source: %q %v", got, err)
	}
}

func TestProtectedCheckoutPathAliasesAndMissingRestore(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(root, alias); err != nil {
		t.Skip(err)
	}
	if !sameCheckoutPath(root, alias) {
		t.Fatal("symlink alias did not match source checkout")
	}
	missing := filepath.Join(root, "missing")
	rec := domain.SessionRecord{Metadata: domain.SessionMetadata{WorkspacePath: missing, WorkspaceRepoPath: missing}}
	if _, err := restoreProtectedCheckout(rec); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source should be refused, err=%v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore recreated a source checkout")
	}
}
