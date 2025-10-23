package mpsd

import (
	"context"
	"github.com/df-mc/go-xsapi"
	"github.com/google/uuid"
)

// JoinConfig implements methods for joining a Session from several handles. It also includes
// a PublishConfig to publish a SessionDescription into the URL referenced in a handle.
type JoinConfig struct {
	PublishConfig
}

// JoinHandleContext joins a Session from the ID of handle and a reference to it.
func (conf JoinConfig) JoinHandleContext(ctx context.Context, src xsapi.TokenSource, handleID uuid.UUID, ref SessionReference) (*Session, error) {
	return conf.publish(ctx, src, handlesURL.JoinPath(handleID.String(), "session"), ref)
}

// JoinActivityContext joins a Session from ActivityHandle.
func (conf JoinConfig) JoinActivityContext(ctx context.Context, src xsapi.TokenSource, handle ActivityHandle) (*Session, error) {
	return conf.JoinHandleContext(ctx, src, handle.ID, handle.SessionReference)
}
