package llm

// OpenAIModel is one OpenAI model a link runs. The catalog lists no OpenAI model, so the caller names each one.
type OpenAIModel struct {
	ID        string  // exact model identifier, such as the one the Models interface lists
	Window    int     // context window in tokens; 0 means DefaultWindow
	MaxOut    int     // output cap per request; 0 leaves the server's own default
	PriceIn   float64 // USD per million input tokens; the paid link refuses a model with no price
	PriceOut  float64 // USD per million output tokens
	Reasoning bool    // the model takes a reasoning effort
}

// openAIModelFor returns the model a weight uses: its own entry, else the nearest weight's that has one.
func openAIModelFor(models map[Weight]OpenAIModel, w Weight) (OpenAIModel, bool) {
	if m, ok := models[w]; ok && m.ID != "" {
		return m, true
	}

	at := indexOfWeight(w)
	for d := 1; d < len(weightOrder); d++ {
		for _, i := range []int{at - d, at + d} {
			if i >= 0 && i < len(weightOrder) {
				if m, ok := models[weightOrder[i]]; ok && m.ID != "" {
					return m, true
				}
			}
		}
	}

	return OpenAIModel{}, false
}

// openAIWindow returns the context window of a model, else the default window.
func openAIWindow(m OpenAIModel) int {
	if m.Window > 0 {
		return m.Window
	}
	return DefaultWindow
}
