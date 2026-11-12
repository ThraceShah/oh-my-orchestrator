package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/envelope"
)

// FleetService is the control-plane surface the loopback API exposes. The CLI
// uses it instead of opening the database beside the running daemon.
type FleetService interface {
	Enroll(context.Context, fleet.Node) (fleet.Enrollment, error)
	Nodes(context.Context) ([]fleet.Node, error)
	RequestPower(context.Context, string, string) error
}

// FleetController serves node enrollment and listing. A nil service means this
// daemon is not a control plane.
type FleetController struct {
	Svc FleetService
}

func (c *FleetController) Register(r chi.Router) {
	// Loopback only. The fleet enrollment token is a credential, and these
	// routes are operator plumbing rather than part of the mobile API, so they
	// stay beside the other internal routes and out of the OpenAPI app surface.
	r.Get("/internal/fleet/nodes", c.list)
	r.Post("/internal/fleet/nodes", c.enroll)
	r.Post("/internal/fleet/nodes/{id}/power", c.power)
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

func (c *FleetController) list(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Svc == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "FLEET_NOT_CONFIGURED", "This daemon is not a fleet control plane", nil)
		return
	}
	nodes, err := c.Svc.Nodes(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	if nodes == nil {
		nodes = []fleet.Node{}
	}
	envelope.WriteJSON(w, http.StatusOK, fleetNodesResponse{Nodes: nodes})
}

func (c *FleetController) enroll(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Svc == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "FLEET_NOT_CONFIGURED", "This daemon is not a fleet control plane", nil)
		return
	}
	var in fleetEnrollRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	node := fleet.Node{
		ID: strings.TrimSpace(in.ID), Name: strings.TrimSpace(in.Name), Role: fleet.Role(in.Role),
		Availability: fleet.Availability(in.Availability), Capacity: in.Capacity, Platforms: in.Platforms,
		PowerBroker: in.PowerBroker, PowerBrokerNode: in.PowerBrokerNode,
		WakeCommand: in.WakeCommand, ShutdownCommand: in.ShutdownCommand, ExpectedIdentity: in.Identity,
	}
	enrolled, err := c.Svc.Enroll(r.Context(), node)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "FLEET_ENROLL_INVALID", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusCreated, fleetEnrollResponse{Node: enrolled.Node, Token: enrolled.Token})
}

type fleetPowerRequest struct {
	Action string `json:"action"`
}

func (c *FleetController) power(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Svc == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "FLEET_NOT_CONFIGURED", "This daemon is not a fleet control plane", nil)
		return
	}
	var in fleetPowerRequest
	if err := decodeJSON(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if err := c.Svc.RequestPower(r.Context(), chi.URLParam(r, "id"), strings.TrimSpace(in.Action)); err != nil {
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "FLEET_POWER_REJECTED", err.Error(), nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
