package cache

import (
	"encoding/json"
	"reflect"
	"testing"
)

func layersOf(names ...string) []Layer {
	out := make([]Layer, len(names))
	for i, n := range names {
		out[i] = Layer{Name: n, Hash: "h-" + n}
	}
	return out
}

func TestPlanMarkersFixesRepoAndBriefAndTurnsAutoOn(t *testing.T) {
	plan := PlanMarkers(layersOf("tools", "role", "repo", "standards", "brief", "conversation"))
	if !reflect.DeepEqual(plan.Fixed, []string{"repo", "brief"}) || !plan.Auto {
		t.Fatalf("plan = %+v; want fixed repo and brief, auto on", plan)
	}
	if len(plan.Layers) != 6 || plan.Layers[2].Name != "repo" {
		t.Fatalf("layers = %+v; want all 6 in send order", plan.Layers)
	}
}

func TestPlanMarkersDropsAMissingLayerMarker(t *testing.T) {
	plan := PlanMarkers(layersOf("tools", "role", "repo"))
	if !reflect.DeepEqual(plan.Fixed, []string{"repo"}) {
		t.Fatalf("fixed = %v; want only repo", plan.Fixed)
	}
	empty := PlanMarkers(nil)
	if len(empty.Fixed) != 0 || len(empty.Layers) != 0 {
		t.Fatalf("plan = %+v; want no layers and no fixed markers", empty)
	}
	raw, err := json.Marshal(empty)
	if err != nil || string(raw) != `{"layers":[],"fixed":[],"auto":true}` {
		t.Fatalf("json = %s, %v; want empty arrays, not null", raw, err)
	}
}

func TestPlanMarkersDoesNotAliasItsInput(t *testing.T) {
	in := layersOf("repo")
	plan := PlanMarkers(in)
	in[0].Hash = "changed"
	if plan.Layers[0].Hash != "h-repo" {
		t.Fatal("the plan must hold its own copy of the layers")
	}
}

func TestMarkerPlanRoundTripsThroughThePromptNamespace(t *testing.T) {
	c := newTestCache(t, t.TempDir(), "v1", 0)
	plan := PlanMarkers(layersOf("tools", "role", "repo", "brief"))
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	key := c.Key("builder", "confighash")
	if err := c.Put(NSPrompt, key, raw); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get(NSPrompt, key)
	var back MarkerPlan
	if !ok || json.Unmarshal(got, &back) != nil || !reflect.DeepEqual(back, plan) {
		t.Fatalf("plan = %+v, %v; want %+v", back, ok, plan)
	}
}
