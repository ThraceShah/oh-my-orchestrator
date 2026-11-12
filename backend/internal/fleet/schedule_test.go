package fleet

import (
	"errors"
	"testing"
)

func worker(id string, availability Availability, presence Presence, capacity, inUse int, platforms ...string) Candidate {
	return Candidate{InUse: inUse, Node: Node{
		ID: id, Name: id, Role: RoleWorker, Availability: availability,
		Capacity: capacity, Platforms: platforms, Presence: presence,
		Capabilities:    []Capability{{Harness: "claude-code", LoggedIn: true}},
		PowerBrokerNode: "broker",
	}}
}

func TestPlaceSkipsOfflineFullOnDemandAndControl(t *testing.T) {
	nodes := []Candidate{
		{Node: Node{ID: "control", Role: RoleControl}},
		worker("offline", AlwaysOn, PresenceOffline, 4, 0, "linux"),
		worker("full", AlwaysOn, PresenceOnline, 1, 1, "linux"),
		worker("cold", OnDemand, PresenceOffline, 4, 0, "windows"),
		worker("busy", AlwaysOn, PresenceOnline, 4, 2, "linux"),
		worker("quiet", AlwaysOn, PresenceOnline, 4, 0, "linux"),
	}
	got, err := Place(Request{Kind: "worker", Harness: "claude-code"}, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "quiet" || got.State != PlacementAssigned || got.Wake {
		t.Fatalf("decision = %+v", got)
	}
}

func TestPlaceRejectsMissingHarnessAndWrongPlatform(t *testing.T) {
	nodes := []Candidate{worker("a", AlwaysOn, PresenceOnline, 2, 0, "linux")}
	if _, err := Place(Request{Kind: "worker", Harness: "codex"}, nodes); !errors.Is(err, ErrNoNode) {
		t.Fatalf("missing harness err = %v", err)
	}
	if _, err := Place(Request{Kind: "worker", Harness: "claude-code", Platform: "windows"}, nodes); !errors.Is(err, ErrNoNode) {
		t.Fatalf("platform err = %v", err)
	}
}

func TestExplicitOnDemandWakesAndDoesNotMove(t *testing.T) {
	nodes := []Candidate{
		worker("warm", AlwaysOn, PresenceOnline, 4, 0, "linux"),
		worker("cold", OnDemand, PresenceOffline, 4, 0, "windows"),
	}
	got, err := Place(Request{Kind: "worker", Harness: "claude-code", NodeID: "cold"}, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "cold" || !got.Wake || got.State != PlacementWaking || got.BrokerID != "broker" {
		t.Fatalf("decision = %+v", got)
	}
}

func TestStickyBeatsAQuieterNode(t *testing.T) {
	nodes := []Candidate{
		worker("old", AlwaysOn, PresenceOnline, 4, 3, "linux"),
		worker("new", AlwaysOn, PresenceOnline, 4, 0, "linux"),
	}
	got, err := Place(Request{Kind: "worker", StickyNode: "old"}, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "old" {
		t.Fatalf("node = %s", got.NodeID)
	}
}

func TestOrchestratorStaysHome(t *testing.T) {
	nodes := []Candidate{
		worker("home", AlwaysOn, PresenceOnline, 1, 1, "linux"),
		worker("other", AlwaysOn, PresenceOnline, 4, 0, "linux"),
	}
	if _, err := Place(Request{Kind: "orchestrator"}, nodes); !errors.Is(err, ErrHomeRequired) {
		t.Fatalf("missing home err = %v", err)
	}
	got, err := Place(Request{Kind: "orchestrator", HomeNode: "home", HomeCheckout: "/work/proj", Harness: "claude-code"}, nodes)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != "home" || got.Checkout != "/work/proj" {
		t.Fatalf("decision = %+v", got)
	}
	offline := nodes
	offline[0].Node.Presence = PresenceOffline
	got, err = Place(Request{Kind: "orchestrator", HomeNode: "home", HomeCheckout: "/work/proj"}, offline)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != PlacementOffline || got.NodeID != "home" {
		t.Fatalf("offline home = %+v", got)
	}
}

func TestControlNodeNeverRunsSessions(t *testing.T) {
	nodes := []Candidate{{Node: Node{ID: "control", Role: RoleControl, Presence: PresenceOnline}}}
	if _, err := Place(Request{Kind: "worker", NodeID: "control"}, nodes); !errors.Is(err, ErrControlExcluded) {
		t.Fatalf("err = %v", err)
	}
}
