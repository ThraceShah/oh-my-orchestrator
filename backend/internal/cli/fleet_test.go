package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFleetNodesJSONOmitsOperatorSecrets(t *testing.T) {
	cfg := setConfigEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/fleet/nodes" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"nodes":[{"id":"remote","name":"Worker","capacity":2,"inUse":1,"wakeCommand":"secret wake","shutdownCommand":"secret shutdown","expectedIdentity":"private identity","capabilities":[{"harness":"pi","installation":"installed","authentication":"configured","freshness":"stale","supportedModes":["chat","tui"]}]}]}`)
	}))
	defer server.Close()
	writeRunFileFor(t, cfg, server)
	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "fleet", "nodes", "--json")
	if err != nil {
		t.Fatalf("err=%v stderr=%s", err, stderr)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "configured") || !strings.Contains(out, "inUse") || strings.Contains(out, "secret") || strings.Contains(out, "private") {
		t.Fatalf("nodes=%s", out)
	}
}

func TestFleetModelsQueriesWorkerScope(t *testing.T) {
	cfg := setConfigEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/fleet/nodes/remote/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("agent") != "pi" || r.URL.Query().Get("projectId") != "project-a" {
			t.Errorf("wrong target query=%s", r.URL)
		}
		_, _ = io.WriteString(w, `{"models":[{"id":"worker-model","label":"Worker model","effortLevels":["high"]}]}`)
	}))
	defer server.Close()
	writeRunFileFor(t, cfg, server)
	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "fleet", "models", "--node", "remote", "--agent", "pi", "--project", "project-a", "--json")
	if err != nil || !strings.Contains(out, "worker-model") || !strings.Contains(out, "high") {
		t.Fatalf("err=%v stderr=%s out=%s", err, stderr, out)
	}
}

func TestFleetSpawnDoesNotProbeControlAgent(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		name := "auto"
		if pinned {
			name = "pinned"
		}
		t.Run(name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			localProbed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/projects/demo":
					_, _ = io.WriteString(w, `{"project":{"id":"demo","name":"Demo","path":"/controller/repo","kind":"git"}}`)
				case "/api/v1/fleet/nodes":
					_, _ = io.WriteString(w, `{"nodes":[]}`)
				case "/api/v1/agents/readiness/ensure":
					localProbed = true
					_, _ = io.WriteString(w, readinessAgentsJSON("pi", "not_installed", "unknown"))
				case "/api/v1/sessions":
					var body spawnRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.Harness != "pi" || body.Model != "remote-model" || body.Effort != "high" {
						t.Errorf("spawn lost settings: %+v", body)
					}
					if pinned && body.NodeID != "remote" {
						t.Errorf("spawn lost worker: %+v", body)
					}
					_, _ = io.WriteString(w, `{"session":{"id":"global-job","status":"idle"}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			writeRunFileFor(t, cfg, server)
			args := []string{"spawn", "--project", "demo", "--agent", "pi", "--model", "remote-model", "--effort", "high", "--name", "Worker"}
			if pinned {
				args = append(args, "--node", "remote")
			}
			out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
			if err != nil || localProbed || !strings.Contains(out, "global-job") {
				t.Fatalf("err=%v localProbe=%v stderr=%s out=%s", err, localProbed, stderr, out)
			}
		})
	}
}
