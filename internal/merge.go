package internal

import "context"

// MergeContext merges two contexts into one [context.Context].
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
