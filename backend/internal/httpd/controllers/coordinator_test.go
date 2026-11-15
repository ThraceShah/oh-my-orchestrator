package controllers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/controllers"
	sessionsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/session"
)

type coordinatorTestService struct {
	*fakeSessionService
	started []sessionsvc.CoordinatorStartInput
}

func (f *coordinatorTestService) StartCoordinator(_ context.Context, in sessionsvc.CoordinatorStartInput) (domain.Session, error) {
	f.started = append(f.started, in)
	return domain.Session{SessionRecord: domain.SessionRecord{ID: "project-coordinator", ProjectID: in.ProjectID, Kind: domain.KindOrchestrator, Harness: in.Harness}}, nil
}
func TestCoordinatorHTTPPreservesChoicesAndComputesRetryHash(t *testing.T) {
	svc := &coordinatorTestService{fakeSessionService: newFakeSessionService()}
	router := chi.NewRouter()
	controller := &controllers.SessionsController{Svc: svc}
	router.Route("/api/v1", controller.Register)
	server := httptest.NewServer(router)
	defer server.Close()
	payload := `{"projectId":"p","agent":"pi","model":"chosen-model","effort":"high","brief":"read the project","clientRequestId":"request-1","attachments":[{"mimeType":"text/plain","data":"YWJj"}]}`
	for i := 0; i < 2; i++ {
		body, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/orchestrators", payload)
		if status != http.StatusCreated {
			t.Fatalf("%d %s", status, body)
		}
	}
	if len(svc.started) != 2 {
		t.Fatal("request was not routed to coordinator start service")
	}
	in := svc.started[0]
	if in.Harness != domain.HarnessPi || in.Model != "chosen-model" || in.Effort == nil || *in.Effort != "high" || len(in.Attachments) != 1 || string(in.Attachments[0].Data) != "abc" {
		t.Fatalf("lost opening choices: %#v", in)
	}
	if in.ClientRequestHash == "" || in.ClientRequestHash != svc.started[1].ClientRequestHash {
		t.Fatal("payload fingerprint is not stable")
	}
	changed := `{"projectId":"p","agent":"pi","model":"different-model","clientRequestId":"request-1"}`
	_, status, _ := doRequest(t, server, http.MethodPost, "/api/v1/orchestrators", changed)
	if status != http.StatusCreated || svc.started[2].ClientRequestHash == in.ClientRequestHash {
		t.Fatal("payload fingerprint did not bind selected model")
	}
}
