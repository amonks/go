package llm

import (
	"context"
	"sync/atomic"
)

// Usage recording: an optional process-wide hook observing every
// completed stream's token counts, so an app can ship its operational
// LLM usage somewhere (the costs app) without threading a recorder
// through every call site. The hook receives token counts, not costs —
// pricing belongs to the reader's price table (pkg/modelprice), not to
// the moment of use.

var usageRecorder atomic.Pointer[func(AssistantMessage)]
var contextUsageRecorder atomic.Pointer[func(context.Context, AssistantMessage)]

// SetUsageRecorder installs f to be called once per completed stream,
// from Wait, with the final assistant message (model, usage,
// timestamp). Pass nil to uninstall. f must not block: it is called on
// the caller's Wait path.
func SetUsageRecorder(f func(AssistantMessage)) {
	contextUsageRecorder.Store(nil)
	if f == nil {
		usageRecorder.Store(nil)
		return
	}
	usageRecorder.Store(&f)
}

// SetUsageRecorderContext installs a recorder that also receives the context
// of the stream whose usage completed. It lets operational sinks retain the
// LLM call's causal trace without changing the older callback API.
func SetUsageRecorderContext(f func(context.Context, AssistantMessage)) {
	usageRecorder.Store(nil)
	if f == nil {
		contextUsageRecorder.Store(nil)
		return
	}
	contextUsageRecorder.Store(&f)
}

// recordUsage invokes the installed recorder for a message that
// carries billable tokens. Streams that died before any usage arrived
// have nothing to record.
func recordUsage(ctx context.Context, msg AssistantMessage) {
	u := msg.Usage
	if u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
		return
	}
	if f := contextUsageRecorder.Load(); f != nil {
		(*f)(ctx, msg)
		return
	}
	if f := usageRecorder.Load(); f != nil {
		(*f)(msg)
	}
}
