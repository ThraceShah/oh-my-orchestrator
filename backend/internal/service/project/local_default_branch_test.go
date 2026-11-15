package project_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/workspace/gitworktree"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/project"
)

func TestAddLocalProjectRecordsBaseBranchAtRegistration(t *testing.T) {
	configureCommitter(t)
	for _, explicit := range []string{"", domain.DefaultBranchAuto, "stable-base"} {
		t.Run("configured_"+explicit, func(t *testing.T) {
			ctx := context.Background()
			repo := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "private-project"))
			run := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v (%s)", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			run("branch", "-m", "private/starting-point")
			run("branch", "stable-base")
			initialCommit := run("rev-parse", "HEAD")
			m := newManager(t)
			input := project.AddInput{Path: repo}
			if explicit != "" {
				input.Config = &domain.ProjectConfig{DefaultBranch: explicit}
			}
			registered, err := m.Add(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			want := "private/starting-point"
			if explicit == "stable-base" {
				want = explicit
			}
			if registered.Config == nil || registered.Config.DefaultBranch != want {
				t.Fatalf("persisted config = %#v, want base branch %q", registered.Config, want)
			}
			run("checkout", "-b", "later-feature")
			run("commit", "--allow-empty", "-m", "later work")
			loaded, err := m.Get(ctx, registered.ID)
			if err != nil || loaded.Project == nil || loaded.Project.Config == nil || loaded.Project.Config.DefaultBranch != want {
				t.Fatalf("base branch changed after checkout: %#v, %v", loaded, err)
			}
			ws, err := gitworktree.New(gitworktree.Options{ManagedRoot: t.TempDir(), RepoResolver: gitworktree.StaticRepoResolver{registered.ID: repo}})
			if err != nil {
				t.Fatal(err)
			}
			info, err := ws.Create(ctx, ports.WorkspaceConfig{ProjectID: registered.ID, SessionID: "first", Branch: "ao/first", BaseBranch: loaded.Project.Config.DefaultBranch})
			if err != nil {
				t.Fatalf("create worker worktree from recorded local base: %v", err)
			}
			out, err := exec.Command("git", "-C", info.Path, "rev-parse", "HEAD").Output()
			if err != nil || strings.TrimSpace(string(out)) != initialCommit {
				t.Fatalf("worktree HEAD = %q, %v; want original base %s", out, err, initialCommit)
			}
		})
	}
}

func TestAddLocalProjectDoesNotGuessDetachedHeadBase(t *testing.T) {
	configureCommitter(t)
	repo := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "detached"))
	if out, err := exec.Command("git", "-C", repo, "checkout", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("detach HEAD: %v (%s)", err, out)
	}
	_, err := newManager(t).Add(context.Background(), project.AddInput{Path: repo})
	wantCode(t, err, "DEFAULT_BRANCH_UNRESOLVED")
}

func TestAddRemoteProjectKeepsAutomaticBaseBranchPolicy(t *testing.T) {
	configureCommitter(t)
	for _, remote := range []string{"origin", "upstream"} {
		t.Run(remote, func(t *testing.T) {
			repo := gitRepoWithCommitNoOrigin(t, filepath.Join(t.TempDir(), "remote-project"))
			if out, err := exec.Command("git", "-C", repo, "remote", "add", remote, "https://example.invalid/repository.git").CombinedOutput(); err != nil {
				t.Fatalf("add remote: %v (%s)", err, out)
			}
			registered, err := newManager(t).Add(context.Background(), project.AddInput{Path: repo})
			if err != nil {
				t.Fatal(err)
			}
			if registered.DefaultBranch != domain.DefaultBranchAuto || (registered.Config != nil && registered.Config.DefaultBranch != "" && registered.Config.DefaultBranch != domain.DefaultBranchAuto) {
				t.Fatalf("remote project acquired checkout-derived base: %#v", registered)
			}
		})
	}
}
