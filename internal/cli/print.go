package cli

import (
	"encoding/json"
	"fmt"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/fault"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// eventLine spells one recorded event on one line: time, type and subtype, run, group and data.
func eventLine(e bus.Event) string {
	kind := string(e.Type)
	if e.Subtype != "" {
		kind += "/" + string(e.Subtype)
	}
	at := "-"
	if !e.Time.IsZero() {
		at = e.Time.UTC().Format("2006-01-02T15:04:05Z")
	}
	data, _ := json.Marshal(e.Data)
	line := fmt.Sprintf("%s %s run=%s", at, kind, e.Run)
	if e.Group != "" {
		line += " group=" + string(e.Group)
	}
	return line + " " + string(data)
}

// faultOfEvent turns an error event of the Error Handler into a fault; ok is false for any other event.
func faultOfEvent(e bus.Event) (fault.Fault, bool) {
	var d fault.FaultData
	switch p := e.Data.(type) {
	case *fault.ErrorFault:
		d = p.FaultData
	case *fault.PanicFault:
		d = p.FaultData
	case *fault.ResourceFault:
		d = p.FaultData
	default:
		return fault.Fault{}, false
	}
	return fault.Fault{
		Source: fault.Source{Component: d.Component, Run: e.Run, Group: e.Group},
		Code:   d.Code, Kind: fault.Kind(d.Kind), Severity: fault.Severity(d.Severity),
		Reason: d.Reason, Fix: d.Fix, Panic: d.Panic, Stack: d.Stack,
	}, true
}

// faultResult maps an error event the command waited on to a result; a warn fault prints a line and fails nothing.
func (c *CLI) faultResult(f fault.Fault) (Result, bool) {
	if f.Severity == fault.SeverityWarn {
		_ = c.PrintError(f)
		return Result{}, false
	}
	code := f.Code
	if !format.IsCode(code) || code == string(format.Other) {
		code = string(format.Internal)
	}
	facts := format.ErrorFacts{Code: code, Reason: f.Reason, Fix: f.Fix, Run: string(f.Source.Run), Group: string(f.Source.Group)}
	return Result{Outcome: Outcome(f.Kind.ExitCode()), Shape: "error." + code, Code: code, Facts: facts, Reason: f.Reason, Fix: f.Fix}, true
}
