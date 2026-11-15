package specgen

import (
	"net/http"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/controllers"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/envelope"
)

type fleetNodeParam struct {
	ID string `path:"id"`
}
type fleetAgentQuery struct {
	Agent     string `query:"agent"`
	ProjectID string `query:"projectId"`
}
type fleetModelQuery struct {
	Agent     string `query:"agent" required:"true"`
	ProjectID string `query:"projectId"`
}

func fleetOperations() []operation {
	errors := func(success any) []respUnit {
		return []respUnit{
			{http.StatusOK, success},
			{http.StatusBadRequest, envelope.APIError{}},
			{http.StatusNotFound, envelope.APIError{}},
			{http.StatusServiceUnavailable, envelope.APIError{}},
			{http.StatusNotImplemented, envelope.APIError{}},
			{http.StatusInternalServerError, envelope.APIError{}},
		}
	}
	return []operation{
		{method: http.MethodGet, path: "/api/v1/fleet/nodes", id: "listFleetNodes", tag: "fleet", summary: "List worker presence, load and normalized capabilities without credentials", resps: errors(controllers.FleetNodeListResponse{})},
		{method: http.MethodGet, path: "/api/v1/fleet/nodes/{id}/models", id: "getFleetNodeModels", tag: "fleet", summary: "Read a worker's own model catalog in its project environment", pathParams: []any{fleetNodeParam{}, fleetModelQuery{}}, resps: errors(controllers.AgentModelsResponse{})},
		{method: http.MethodGet, path: "/api/v1/fleet/nodes/{id}/readiness", id: "getFleetNodeReadiness", tag: "fleet", summary: "Read cached readiness on a worker", pathParams: []any{fleetNodeParam{}, fleetAgentQuery{}}, resps: errors(controllers.AgentReadinessResponse{})},
		{method: http.MethodPost, path: "/api/v1/fleet/nodes/{id}/readiness/ensure", id: "ensureFleetNodeReadiness", tag: "fleet", summary: "Refresh normalized readiness on a worker without installing or logging in", pathParams: []any{fleetNodeParam{}, fleetAgentQuery{}}, resps: errors(controllers.AgentReadinessResponse{})},
	}
}
