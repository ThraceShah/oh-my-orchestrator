//go:build !windows

package sessionmanager

import (
	"context"
	"os/exec"

	aoprocess "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/process"
)

func workspaceStepCommand(ctx context.Context, command string) *exec.Cmd {
	return aoprocess.CommandContext(ctx, "sh", "-c", command)
}
