package cache

// Layer is one part of a prompt, in the order the parts are sent.
type Layer struct {
	Name string `json:"name"` // tools, role, repo, standards, brief or conversation
	Hash string `json:"hash"` // SHA-256 of the layer's bytes
}

// MarkerPlan says where the model client places markers for one agent role.
type MarkerPlan struct {
	Layers []Layer  `json:"layers"` // the layers in send order
	Fixed  []string `json:"fixed"`  // names of layers followed by a fixed marker
	Auto   bool     `json:"auto"`   // true when the provider's automatic marker is on
}

// fixedMarkerLayers names the layers that carry a fixed marker.
var fixedMarkerLayers = map[string]bool{"repo": true, "brief": true}

// PlanMarkers returns the plan for a layer list: fixed markers after repo and brief, automatic marker on.
// A layer missing from the list drops its marker; the plan holds its own copy of the layers.
func PlanMarkers(layers []Layer) MarkerPlan {
	plan := MarkerPlan{Layers: make([]Layer, len(layers)), Fixed: []string{}, Auto: true}
	copy(plan.Layers, layers)
	seen := map[string]bool{}
	for _, l := range layers {
		if fixedMarkerLayers[l.Name] && !seen[l.Name] {
			seen[l.Name] = true
			plan.Fixed = append(plan.Fixed, l.Name)
		}
	}
	return plan
}
