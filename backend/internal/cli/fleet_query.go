package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func newFleetQueryCommand(ctx *commandContext, operation string) *cobra.Command {
	var node, agent, project string
	var refresh, asJSON bool
	cmd := &cobra.Command{Use: operation, Short: "Query an enrolled worker's " + operation, Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(node) == "" {
			return usageError{fmt.Errorf("--node is required")}
		}
		if operation == "models" && strings.TrimSpace(agent) == "" {
			return usageError{fmt.Errorf("--agent is required")}
		}
		query := url.Values{}
		if agent != "" {
			query.Set("agent", agent)
		}
		if project != "" {
			query.Set("projectId", project)
		}
		path := "fleet/nodes/" + url.PathEscape(node) + "/" + operation
		var result json.RawMessage
		var err error
		if operation == "readiness" && refresh {
			err = ctx.postJSON(cmd.Context(), path+"/ensure?"+query.Encode(), nil, &result)
		} else {
			err = ctx.getJSON(cmd.Context(), path+"?"+query.Encode(), &result)
		}
		if err != nil {
			return err
		}
		// The contracts are machine-readable even without --json; no lossy table
		// projection should hide supported model tuning or unavailable reasons.
		_ = asJSON
		return writeJSON(cmd.OutOrStdout(), result)
	}}
	cmd.Flags().StringVar(&node, "node", "", "worker node id")
	cmd.Flags().StringVar(&agent, "agent", "", "agent harness id")
	cmd.Flags().StringVar(&project, "project", "", "worker-local logical project id")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the complete machine-readable response")
	if operation == "readiness" {
		cmd.Flags().BoolVar(&refresh, "refresh", false, "refresh readiness on the worker before returning")
	}
	return cmd
}

// A worker may offer an agent that is absent on the control host. Discover
// fleet mode through the daemon; credentials/binaries are validated on owner.
func (c *commandContext) fleetWorkerSpawn(ctx context.Context, kind, node, platform string) (bool, error) {
	if kind == "orchestrator" {
		return false, nil
	}
	if node != "" || platform != "" {
		return true, nil
	}
	var response struct {
		Nodes []json.RawMessage `json:"nodes"`
	}
	err := c.getJSON(ctx, "fleet/nodes", &response)
	if err == nil {
		return true, nil
	}
	var apiErr apiResponseError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusNotFound || apiErr.ErrorBody.Code == "FLEET_NOT_CONFIGURED") {
		return false, nil
	}
	return false, err
}
