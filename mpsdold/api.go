package mpsd2

import "context"

type API interface {
	SessionByReference(ref SessionReference) (*Commit, error)
	Publish(ctx context.Context, ref SessionReference, d *SessionDescription)
}
