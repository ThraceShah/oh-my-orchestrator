package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/envelope"
)

// FleetNodeListResponse exposes worker readiness and capacity without credentials.
type FleetNodeListResponse struct {
	Nodes []fleet.NodeSummary `json:"nodes"`
}

// RegisterApp exposes only sanitized fleet discovery on the application API.
// Enrollment and shell power commands remain in Register's loopback routes.
func (c *FleetController) RegisterApp(r chi.Router) {
	r.Get("/fleet/nodes", c.publicList)
	r.Get("/fleet/nodes/{id}/models", c.nodeModels)
	r.Get("/fleet/nodes/{id}/readiness", c.nodeReadiness)
	r.Post("/fleet/nodes/{id}/readiness/ensure", c.ensureNodeReadiness)
}
func (c *FleetController) publicList(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.Svc == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "FLEET_NOT_CONFIGURED", "This daemon is not a fleet control plane", nil)
		return
	}
	nodes, err := c.Svc.Nodes(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, FleetNodeListResponse{Nodes: fleet.PublicNodes(nodes)})
}
func (c *FleetController) nodeModels(w http.ResponseWriter, r *http.Request) {
	agent := strings.TrimSpace(r.URL.Query().Get("agent"))
	if agent == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "AGENT_REQUIRED", "agent is required", nil)
		return
	}
	c.queryNode(w, r, fleet.NodeQuery{Operation: "models", Agent: agent, ProjectID: strings.TrimSpace(r.URL.Query().Get("projectId"))})
}
func (c *FleetController) nodeReadiness(w http.ResponseWriter, r *http.Request) {
	c.queryNode(w, r, fleet.NodeQuery{Operation: "readiness", Agent: strings.TrimSpace(r.URL.Query().Get("agent")), ProjectID: strings.TrimSpace(r.URL.Query().Get("projectId"))})
}
func (c *FleetController) ensureNodeReadiness(w http.ResponseWriter, r *http.Request) {
	c.queryNode(w, r, fleet.NodeQuery{Operation: "readiness", Agent: strings.TrimSpace(r.URL.Query().Get("agent")), ProjectID: strings.TrimSpace(r.URL.Query().Get("projectId")), Refresh: true})
}
func (c *FleetController) queryNode(w http.ResponseWriter, r *http.Request, query fleet.NodeQuery) {
	if c == nil || c.Svc == nil {
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "FLEET_NOT_CONFIGURED", "This daemon is not a fleet control plane", nil)
		return
	}
	svc, ok := c.Svc.(interface {
		QueryNode(context.Context, string, fleet.NodeQuery) (json.RawMessage, error)
	})
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusNotImplemented, "not_implemented", "FLEET_DISCOVERY_UNAVAILABLE", "Worker discovery is unavailable", nil)
		return
	}
	body, err := svc.QueryNode(r.Context(), chi.URLParam(r, "id"), query)
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "FLEET_QUERY_FAILED", err.Error(), nil)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, body)
}
