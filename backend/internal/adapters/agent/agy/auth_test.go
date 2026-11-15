package agy

import (
	"context"
	"errors"
	"testing"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/adapters/agent/binaryutil"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/ports"
)

func TestAuthStatusUnknownWhenInstalledWithoutAuthenticationEvidence(t *testing.T) {
	plugin := &Plugin{resolvedBinary: "agy"}

	status, err := plugin.AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestAuthStatusUnknownWhenBinaryIsMissing(t *testing.T) {
	// Isolate discovery from the developer's actual Agent installation.
	original := agyBinarySpec
	agyBinarySpec = binaryutil.BinarySpec{Label: "agy", Names: []string{"ao-test-missing-agy-auth-binary"}, WinNames: []string{"ao-test-missing-agy-auth-binary.exe"}}
	t.Cleanup(func() { agyBinarySpec = original })
	t.Setenv("PATH", t.TempDir())

	status, err := (&Plugin{}).AuthStatus(context.Background())
	if status != ports.AgentAuthStatusUnknown || !errors.Is(err, ports.ErrAgentBinaryNotFound) {
		t.Fatalf("status = %q, err = %v; want unknown with missing-binary cause", status, err)
	}
}
