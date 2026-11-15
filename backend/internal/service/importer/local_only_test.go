package importer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidateLocalOnlyProjectDoesNotRequireRemote(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "private-project")
	gitRepoWithCommitNoOrigin(t, repo)
	svc := New(Deps{Store: newFakeStore()})
	result, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsValid || result.NextStep != ImportNextStepContinue || len(result.Root.RequiredActions) != 0 || result.Root.HasOrigin {
		t.Fatalf("local-only validation = %#v, want committed local repository ready without remote", result)
	}
	// The default desktop import policy remains unchanged.
	ordinary, err := svc.Validate(ctx, ImportValidationInput{ImportKind: ImportKindProject, Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	wantActions(t, ordinary.Root.RequiredActions, []string{GitPreparationActionCreateRemoteRepository})
	if out, err := exec.Command("git", "-C", repo, "remote").Output(); err != nil || len(out) != 0 {
		t.Fatalf("validation changed remotes: %q, %v", out, err)
	}
}

func TestPrepareLocalOnlyProjectStillRequiresApprovalAndPreservesPolicyAcrossSteps(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	svc := New(Deps{Store: newFakeStore()})
	input := GitPreparationInput{ImportKind: ImportKindProject, Path: repo, LocalOnly: true, Stepwise: true}
	if _, err := svc.PrepareGit(ctx, input); err == nil {
		t.Fatal("local-only Git preparation without approval succeeded")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unapproved preparation mutated repository: %v", err)
	}
	input.ApprovedActions = []string{GitPreparationActionInit, GitPreparationActionCommit}
	first, err := svc.PrepareGit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Validation.NextStep != ImportNextStepPrepareGit {
		t.Fatalf("first validation = %#v, want initial commit still pending", first.Validation)
	}
	wantActions(t, first.Validation.Root.RequiredActions, []string{GitPreparationActionCommit})
	second, err := svc.PrepareGit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if second.Validation.NextStep != ImportNextStepContinue || !second.Validation.Root.HasCommit || second.Validation.Root.HasOrigin {
		t.Fatalf("second validation = %#v, want committed local repository", second.Validation)
	}
	for _, result := range []GitPreparationResult{first, second} {
		for _, event := range result.Events {
			if event.Action != GitPreparationActionInit && event.Action != GitPreparationActionCommit {
				t.Fatalf("local import performed external setup: %#v", event)
			}
		}
	}
}

func TestLocalOnlyProjectRejectsRemoteSetupAndRetainsPathValidation(t *testing.T) {
	svc := New(Deps{Store: newFakeStore()})
	repo := t.TempDir()
	_, err := svc.PrepareGit(context.Background(), GitPreparationInput{ImportKind: ImportKindProject, Path: repo, LocalOnly: true, RemoteURL: "https://example.invalid/private.git"})
	if err == nil {
		t.Fatal("local-only preparation accepted remote setup")
	}
	missing := filepath.Join(repo, "missing")
	result, err := svc.Validate(context.Background(), ImportValidationInput{ImportKind: ImportKindProject, Path: missing, LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsValid || result.NextStep != ImportNextStepError {
		t.Fatalf("local-only import bypassed missing-path validation: %#v", result)
	}
	if _, err := svc.Validate(context.Background(), ImportValidationInput{ImportKind: ImportKindWorkspace, Path: repo, LocalOnly: true}); err == nil {
		t.Fatal("project-only policy accepted workspace import")
	}
}
