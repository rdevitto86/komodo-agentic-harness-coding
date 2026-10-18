package fault

// Printer shows one fault to the caller; the CLI implements it in the CLI process and engine.ConnPrinter in the engine.
type Printer interface {
	PrintError(f Fault) error
}
