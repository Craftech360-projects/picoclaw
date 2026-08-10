package protocoltypes

import (
	"context"
	"time"
)

type dispatchProbeKey struct{}

// WithDispatchProbe arms a callback the provider fires at the moment it hands
// the request to the HTTP client.
//
// A caller that starts its time-to-first-token clock before building the
// request measures request assembly and provider time as one number. The probe
// splits them, so a slow turn can be attributed to one side or the other.
func WithDispatchProbe(ctx context.Context, onDispatch func(time.Time)) context.Context {
	if ctx == nil || onDispatch == nil {
		return ctx
	}
	return context.WithValue(ctx, dispatchProbeKey{}, onDispatch)
}

// ReportDispatch fires the probe armed by WithDispatchProbe, if any.
func ReportDispatch(ctx context.Context) {
	if ctx == nil {
		return
	}
	if onDispatch, ok := ctx.Value(dispatchProbeKey{}).(func(time.Time)); ok {
		onDispatch(time.Now())
	}
}
