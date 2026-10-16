package cli

// handleEngine is the hidden verb that starts the engine; it fails fast until the engine is built.
func handleEngine(*Call) Result { return notBuilt("the engine") }
