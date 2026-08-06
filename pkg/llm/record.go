package llm

import "sync/atomic"

// Usage recording: an optional process-wide hook observing every
// completed stream's token counts, so an app can ship its operational
// LLM usage somewhere (the costs app) without threading a recorder
// through every call site. The hook receives token counts, not costs —
// pricing belongs to the reader's price table (pkg/modelprice), not to
// the moment of use.

var usageRecorder atomic.Pointer[func(AssistantMessage)]

// SetUsageRecorder installs f to be called once per completed stream,
// from Wait, with the final assistant message (model, usage,
// timestamp). Pass nil to uninstall. f must not block: it is called on
// the caller's Wait path.
func SetUsageRecorder(f func(AssistantMessage)) {
	if f == nil {
		usageRecorder.Store(nil)
		return
	}
	usageRecorder.Store(&f)
}

// recordUsage invokes the installed recorder for a message that
// carries billable tokens. Streams that died before any usage arrived
// have nothing to record.
func recordUsage(msg AssistantMessage) {
	f := usageRecorder.Load()
	if f == nil {
		return
	}
	u := msg.Usage
	if u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
		return
	}
	(*f)(msg)
}
