package fleet

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ExecPower runs the command the control plane stored for this action. The
// command is operator configuration, not a built-in device protocol. An empty
// command fails instead of guessing how the machine powers on.
func ExecPower(ctx context.Context, req PowerRequest) error {
	command := strings.TrimSpace(req.Command)
	if command == "" {
		return fmt.Errorf("no %s command is configured for %s", req.Action, req.NodeID)
	}
	if req.Action != "wake" && req.Action != "shutdown" {
		return fmt.Errorf("unsupported power action %q", req.Action)
	}
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	shell, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		shell, flag = "cmd", "/c"
	}
	cmd := exec.CommandContext(runCtx, shell, flag, command)
	out, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		if len(text) > 400 {
			text = text[:400]
		}
		if text == "" {
			return fmt.Errorf("power %s for %s: %w", req.Action, req.NodeID, err)
		}
		return fmt.Errorf("power %s for %s: %w: %s", req.Action, req.NodeID, err, text)
	}
	return nil
}
