package internal

import "context"

// MergeContext returns a context that is canceled when either parentCtx or
// cancelCtx is canceled. The returned CancelFunc stops observing cancelCtx and
// cancels the merged context with [context.Canceled].
func MergeContext(parentCtx, cancelCtx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parentCtx)
	stop := context.AfterFunc(cancelCtx, func() {
		cancel(context.Cause(cancelCtx))
	})
	return ctx, func() {
		stop()
		cancel(context.Canceled)
	}
}
