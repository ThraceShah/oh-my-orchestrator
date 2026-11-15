package cli

import (
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
)

func newFleetCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Manage fleet nodes that run sessions for the control plane",
		Long: "Fleet nodes are enrolled through the running control-plane daemon.\n\n" +
			"No host names are built in. Start the daemon with AO_FLEET_ROLE=control and AO_FLEET_LISTEN, then enroll workers here. " +
			"The token prints once and becomes that worker's AO_FLEET_TOKEN.",
		Args: noArgs,
	}
	cmd.AddCommand(newFleetEnrollCommand(ctx), newFleetNodesCommand(ctx), newFleetPowerCommand(ctx), newFleetQueryCommand(ctx, "models"), newFleetQueryCommand(ctx, "readiness"))
	return cmd
}

func newFleetEnrollCommand(ctx *commandContext) *cobra.Command {
	var role, availability, brokerNode, wake, shutdown, identity string
	var capacity int
	var platforms []string
	var powerBroker bool
	cmd := &cobra.Command{
		Use:   "enroll <id> <name>",
		Short: "Enroll a node and print its one-time token",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := fleetEnrollRequest{
				ID: args[0], Name: args[1], Role: role, Availability: availability, Capacity: capacity,
				Platforms: platforms, PowerBroker: powerBroker, PowerBrokerNode: brokerNode,
				WakeCommand: wake, ShutdownCommand: shutdown, Identity: identity,
			}
			var res fleetEnrollResponse
			if err := ctx.postLoopbackJSONDecoded(cmd.Context(), "/internal/fleet/nodes", req, &res); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "enrolled %s\ntoken: %s\n", res.Node.ID, res.Token); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.ErrOrStderr(), "The token is shown once. Give it to the node as AO_FLEET_TOKEN.")
			return err
		},
	}
	cmd.Flags().StringVar(&role, "role", string(fleet.RoleWorker), "control or worker")
	cmd.Flags().StringVar(&availability, "availability", string(fleet.AlwaysOn), "always-on or on-demand")
	cmd.Flags().IntVar(&capacity, "capacity", 1, "maximum concurrent sessions")
	cmd.Flags().StringSliceVar(&platforms, "platform", nil, "OS family this node offers; repeat for several")
	cmd.Flags().BoolVar(&powerBroker, "power-broker", false, "this worker can run another node's power commands")
	cmd.Flags().StringVar(&brokerNode, "broker", "", "node id that powers this on-demand node")
	cmd.Flags().StringVar(&wake, "wake-command", "", "command the broker runs to power this node on")
	cmd.Flags().StringVar(&shutdown, "shutdown-command", "", "command the broker runs to power this node off")
	cmd.Flags().StringVar(&identity, "identity", "", "optional expected network identity")
	return cmd
}

func newFleetNodesCommand(ctx *commandContext) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "nodes",
		Short: "List enrolled fleet nodes",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res fleetNodesResponse
			if err := ctx.getLoopbackJSON(cmd.Context(), "/internal/fleet/nodes", &res); err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"nodes": fleet.PublicNodes(res.Nodes)})
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "ID\tNAME\tROLE\tAVAILABILITY\tPRESENCE\tCAPACITY\tPLATFORMS"); err != nil {
				return err
			}
			for _, n := range res.Nodes {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", n.ID, n.Name, n.Role, n.Availability, n.Presence, n.Capacity, strings.Join(n.Platforms, ",")); err != nil {
					return err
				}
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print sanitized node capabilities as JSON")
	return cmd
}

// fleetEnrollRequest mirrors the daemon's enrollment body. The CLI keeps its
// own copy so it does not import the HTTP controller package.
func newFleetPowerCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:   "power <id> <wake|shutdown>",
		Short: "Ask a node's broker to run its configured power command",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := strings.TrimSpace(args[1])
			if action != "wake" && action != "shutdown" {
				return usageError{fmt.Errorf("power action must be wake or shutdown")}
			}
			return ctx.postLoopbackJSON(cmd.Context(), "/internal/fleet/nodes/"+url.PathEscape(args[0])+"/power", map[string]string{"action": action})
		},
	}
}

type fleetEnrollRequest struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Role            string   `json:"role"`
	Availability    string   `json:"availability"`
	Capacity        int      `json:"capacity"`
	Platforms       []string `json:"platforms,omitempty"`
	PowerBroker     bool     `json:"powerBroker"`
	PowerBrokerNode string   `json:"powerBrokerNodeId,omitempty"`
	WakeCommand     string   `json:"wakeCommand,omitempty"`
	ShutdownCommand string   `json:"shutdownCommand,omitempty"`
	Identity        string   `json:"expectedIdentity,omitempty"`
}

type fleetEnrollResponse struct {
	Node  fleet.Node `json:"node"`
	Token string     `json:"token"`
}

type fleetNodesResponse struct {
	Nodes []fleet.Node `json:"nodes"`
}
