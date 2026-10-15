package resource

import (
	"time"

	"github.com/rdevitto86/komodo-agentic-harness-coding/internal/bus"
)

// The subtypes of the resource event the Resource Manager reads.
const (
	subtypeUsage    bus.Subtype = "usage"
	subtypeHeadroom bus.Subtype = "headroom"
	subtypeOverage  bus.Subtype = "overage"
	subtypeLimit    bus.Subtype = "limit"
)

// Usage is the data of resource/usage: one finished session's totals.
type Usage struct {
	Link       string  `json:"link"`
	Agent      string  `json:"agent"`
	Session    string  `json:"session"`
	Model      string  `json:"model"`
	TokensIn   int64   `json:"tokens_in"`
	TokensOut  int64   `json:"tokens_out"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	USD        float64 `json:"usd"`       // metered cost; 0 on a subscription link
	USDEquiv   float64 `json:"usd_equiv"` // notional cost of a subscription session; for display
}

// EventKey binds Usage to resource/usage.
func (Usage) EventKey() bus.Key { return bus.Key{Type: bus.TypeResource, Subtype: subtypeUsage} }

// Headroom is the data of resource/headroom: one window after one reply.
type Headroom struct {
	Link      string    `json:"link"`
	Window    string    `json:"window"` // the provider's window name
	Limit     int64     `json:"limit"`  // the window total
	Remaining int64     `json:"remaining"`
	ResetAt   time.Time `json:"reset_at"`
}

// EventKey binds Headroom to resource/headroom.
func (Headroom) EventKey() bus.Key { return bus.Key{Type: bus.TypeResource, Subtype: subtypeHeadroom} }

// Overage is the data of resource/overage: a subscription link is drawing paid credits.
type Overage struct {
	Link    string    `json:"link"`
	ResetAt time.Time `json:"reset_at"`
}

// EventKey binds Overage to resource/overage.
func (Overage) EventKey() bus.Key { return bus.Key{Type: bus.TypeResource, Subtype: subtypeOverage} }

// Limit is the data of resource/limit: a provider refused a request because a cap was hit.
type Limit struct {
	Link    string    `json:"link"`
	Kind    string    `json:"kind"`     // window, weekly, model, spend or quota
	ResetAt time.Time `json:"reset_at"` // zero when the provider gave none
}

// EventKey binds Limit to resource/limit.
func (Limit) EventKey() bus.Key { return bus.Key{Type: bus.TypeResource, Subtype: subtypeLimit} }

// The four data structs are bound here, as the bus expects of the package that owns them.
func init() {
	for _, p := range []func() bus.Payload{
		func() bus.Payload { return &Usage{} },
		func() bus.Payload { return &Headroom{} },
		func() bus.Payload { return &Overage{} },
		func() bus.Payload { return &Limit{} },
	} {
		bus.Register(p().EventKey(), p)
	}
}
