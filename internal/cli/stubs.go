package cli

// EngineMain runs the engine in the foreground; the binary's main sets it, because the engine's
// parts sit outside this package's imports.
var EngineMain func(*Call) error

// handleEngine is the hidden verb that runs the engine in the foreground.
func handleEngine(c *Call) Result {
	if EngineMain == nil {
		return notBuilt("the engine")
	}

	if err := EngineMain(c); err != nil {
		return internalErr(err.Error())
	}

	return Result{Outcome: OutcomeOK, Raw: "engine stopped\n"}
}

// handleLogin is the terminal-only login command; what a provider's login does is still open, so it returns the
// not-built-yet reply.
func handleLogin(*Call) Result { return notBuilt("login") }
