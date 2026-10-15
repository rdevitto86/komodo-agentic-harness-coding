package cli

// handleInstall is a stub until the installer is built.
func handleInstall(*Call) Result { return notBuilt("komodo install") }

// handleUpdate is a stub until the updater is built.
func handleUpdate(*Call) Result { return notBuilt("komodo update") }

// handleDoctor is a stub until the health checks are built.
func handleDoctor(*Call) Result { return notBuilt("komodo doctor") }

// handleEngine is the hidden verb that starts the engine; it fails fast until the engine is built.
func handleEngine(*Call) Result { return notBuilt("the engine") }

// handleUI is a stub until the dashboard server is built.
func handleUI(*Call) Result { return notBuilt("komodo ui") }
