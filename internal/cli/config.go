package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/config"
	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/format"
)

// ConfigGetData is the data of reply.config.get.
type ConfigGetData struct {
	Items []ConfigItemData `json:"items"`
}

// ConfigItemData is one setting as config get returns it.
type ConfigItemData struct {
	Key     string   `json:"key"`
	Value   any      `json:"value"`
	Source  string   `json:"source"`
	Default any      `json:"default,omitempty"`
	Limit   any      `json:"limit,omitempty"`
	Allowed []string `json:"allowed,omitempty"` // the valid values of an enum key
}

// ConfigSetData is the data of reply.config.set.
type ConfigSetData struct {
	Batch   string             `json:"batch,omitempty"` // the audit batch id; empty when nothing changed
	Changes []ConfigChangeData `json:"changes"`
}

// ConfigChangeData is one key that changed.
type ConfigChangeData struct {
	Key string `json:"key"`
	Old any    `json:"old"`
	New any    `json:"new"`
}

// handleConfigGet prints the named keys, every key with --list, or the full listing with --all.
func handleConfigGet(c *Call) Result {
	cfg := c.cli.deps.Config

	var items []config.Item
	switch {
	case c.Global.List || (c.Global.All && len(c.Args) == 0):
		items = cfg.ListConfigItems("")
	case len(c.Args) == 0:
		return usageErr("config get needs a key, --list or --all", "komodo config get --list")
	default:
		for _, k := range c.Args {
			it, err := cfg.GetConfigItem(k)
			if err != nil {
				return configErr(err)
			}
			items = append(items, it)
		}
	}

	data := ConfigGetData{Items: make([]ConfigItemData, 0, len(items))}
	rows := format.Table{Head: []string{"Key", "Value", "Source"}, What: "settings"}

	for _, it := range items {
		d := ConfigItemData{Key: it.Key, Value: it.Value, Source: layerName(it.Source)}
		if c.Global.All {
			d.Default, d.Limit, d.Allowed = it.Default, it.Limit, it.Allowed
		}
		data.Items = append(data.Items, d)
		rows.Rows = append(rows.Rows, []string{it.Key, text(it.Value), d.Source})
	}

	if c.Global.All && !c.cli.json {
		b, _ := json.MarshalIndent(data.Items, "", "  ")
		return Result{Raw: string(b)}
	}
	return Result{Shape: "reply.config.get", Facts: format.ConfigGetFacts{Rows: rows}, Data: data}
}

// layerName spells the layer a value came from.
func layerName(l config.Layer) string {
	if l == config.LayerMachine {
		return "machine"
	}
	return "built-in"
}

// text spells a setting value for a table cell.
func text(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// handleConfigSet sets pairs, resets keys with --default, or accepts a hand edit with --accept.
func handleConfigSet(c *Call) Result {
	cfg := c.cli.deps.Config

	switch {
	case flagBool(c, "accept") && c.Global.DryRun:
		changes, err := acceptPreview(cfg)
		if err != nil {
			return configErr(err)
		}
		return setResult(config.Batch{Changes: changes})
	case flagBool(c, "accept"):
		b, err := cfg.Accept()
		if err != nil {
			return configErr(err)
		}
		return setResult(b)
	case flagBool(c, "default"):
		return resetKeys(c)
	case c.Global.DryRun:
		return notBuilt("--dry-run for config set key=value, which the Configuration Manager has no check-only call for,")
	}

	changes := make([]config.Change, 0, len(c.Args))
	for _, a := range c.Args {
		k, v, _ := strings.Cut(a, "=")
		changes = append(changes, config.Change{Key: strings.TrimSpace(k), New: v})
	}

	b, err := cfg.SetConfigItem(changes...)
	if err != nil {
		return configErr(err)
	}
	return setResult(b)
}

// resetKeys puts each named key, role or file back to its built-in value; several changes wait for a typed y.
func resetKeys(c *Call) Result {
	cfg := c.cli.deps.Config

	var plan []config.Change
	for _, k := range c.Args {
		p, err := cfg.PlanReset(k)
		if err != nil {
			return configErr(err)
		}
		plan = append(plan, p...)
	}

	if c.Global.DryRun {
		return setResult(config.Batch{Changes: plan})
	}
	if len(plan) > 1 && !c.cli.confirm(plan) {
		return setResult(config.Batch{})
	}

	reset := make([]config.Change, len(plan))
	for i, p := range plan {
		reset[i] = config.Change{Key: p.Key}
	}

	b, err := cfg.SetConfigItem(reset...)
	if err != nil {
		return configErr(err)
	}
	return setResult(b)
}

// confirm lists each change on standard error and reads one line from standard input; only y accepts.
func (c *CLI) confirm(plan []config.Change) bool {
	_, _ = fmt.Fprintln(c.errOut, "These settings go back to their built-in values:")
	for _, p := range plan {
		_, _ = fmt.Fprintf(c.errOut, "  %s: %s -> %s\n", p.Key, text(p.Old), c.resetTarget(p))
	}

	_, _ = fmt.Fprint(c.errOut, "Type y to continue: ")
	line, _ := bufio.NewReader(c.in).ReadString('\n')

	if strings.EqualFold(strings.TrimSpace(line), "y") {
		return true
	}

	_, _ = fmt.Fprintln(c.errOut, "Nothing was changed.")
	return false
}

// resetTarget spells the value a reset puts back; a model reset has no value to write, so it shows the default.
func (c *CLI) resetTarget(p config.Change) string {
	if p.New != nil {
		return text(p.New)
	}
	if it, err := c.deps.Config.GetConfigItem(p.Key); err == nil {
		return text(it.Default)
	}
	return ""
}

// acceptPreview lists the files config set --accept would record, with their old and new checksums, writing none.
func acceptPreview(cfg *config.Manager) ([]config.Change, error) { return cfg.AcceptPlan() }

// setResult builds the reply.config.set result from a written batch.
func setResult(b config.Batch) Result {
	data := ConfigSetData{Batch: b.ID, Changes: make([]ConfigChangeData, 0, len(b.Changes))}
	rows := format.Table{Head: []string{"Key", "Old", "New"}, What: "settings"}

	for _, ch := range b.Changes {
		data.Changes = append(data.Changes, ConfigChangeData{Key: ch.Key, Old: ch.Old, New: ch.New})
		rows.Rows = append(rows.Rows, []string{ch.Key, text(ch.Old), text(ch.New)})
	}

	return Result{Shape: "reply.config.set", Facts: format.ConfigSetFacts{
		Batch:   b.ID,
		Changed: rows},
		Data: data,
	}
}

// configErr maps a Configuration Manager error to the code and exit the CLI table gives it.
func configErr(err error) Result {
	var (
		uk  config.UnknownKeyError
		bv  config.BadValueError
		lim config.LimitError
	)

	switch {
	case errors.As(err, &uk):
		return errResult(OutcomeUsage, format.UnknownKey, err.Error(), "komodo config get "+uk.Closest)
	case errors.As(err, &bv):
		return errResult(OutcomeUsage, format.BadValue, err.Error(), "komodo config get "+bv.Key+" --all")
	case errors.As(err, &lim):
		return refuse(format.OverLimit, err.Error(), "komodo config get "+lim.Key+" --all")
	case errors.Is(err, config.ErrTampered):
		return refuse(format.ConfigTampered, err.Error(), "komodo config set --accept")
	case errors.Is(err, config.ErrLocked):
		return refuse(format.Locked, err.Error(), "wait for the other komodo process to finish, then retry")
	}
	return internalErr(err.Error())
}
