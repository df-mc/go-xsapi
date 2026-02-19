package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/google/uuid"
)

// Session represents a multiplayer session in MPSD (Multiplayer Session Directory) in Xbox Live.
//
// A Session is a stateful, client-side handle to a remote multiplayer session.
// It acts as a thin wrapper around a [SessionDescription] and maintains a locally cached
// copy of the session state, which is synchronized with MPSD using E-Tags.
//
// In addition to explicit synchronization via [Session.Sync], the cached
// session state is kept up-to-date automatically though an RTA (Real-Time
// Activity) subscription. RTA delivers session change notifications over a
// WebSocket connection, allowing the Session to update its cache as soon
// as the remote multiplayer session changes (e.g. member is joining/leaving).
//
// Session is safe for concurrent use unless otherwise documented.
//
// Once a Session is closed, all operations should be treated as invalid.
type Session struct {
	// client is the API client for Multiplayer Session Directory (MPSD) in Xbox Live
	// used to create this multiplayer session. It is used to synchronize or commit
	// the properties on the multiplayer session.
	client *Client

	// log is the logger used for reporting errors and diagnostic information
	// related to the session.
	//
	// The logger is configured via PublishConfig or JoinConfig, or defaults
	// to the logger configured on the API client.
	log *slog.Logger

	// ref contains a reference to the multiplayer session.
	ref SessionReference

	// etag holds the most recently observed E-Tag for the session resource.
	//
	// An atomic pointer is used to allow lock-free reads while supporting
	// safe concurrent updates.
	etag atomic.Pointer[string]
	// cache holds the SessionDescription for the multiplayer session in the last known state.
	// Callers can always refresh this cache to the remote state using [Session.Sync] method.
	cache *SessionDescription
	// cacheMu guards the cache from concurrent read-write access.
	cacheMu sync.RWMutex

	// closed is a channel that is closed when the Session is no longer usable.
	//
	// Goroutines may select on this channel to be notified when the session has been closed.
	// Once closed, no further network operations should be performed using this Session.
	closed chan struct{}
	// once ensures that the closure of the Session occurs only once.
	once sync.Once
}

// Close closes the multiplayer session using a context with 15 seconds timeout.
//
// If the caller is the host of the multiplayer session, the session itself is closed.
// If the caller is a non-host participant, this call ensures the caller to leave the session.
//
// Once CloseContext is called, the multiplayer session will no longer receive notifications
// about changes in the session even though if the session still exist after leaving.
// CloseContext can be called many times since it internally uses a [sync.Once].
func (s *Session) Close() error {
	ctx, cancel := context.WithTimeout(s.Context(), time.Second*15)
	defer cancel()
	return s.CloseContext(ctx)
}

// CloseContext closes the multiplayer session using the context.
//
// If the caller is the host of the multiplayer session, the session itself is closed.
// If the caller is a non-host participant, this call ensures the caller to leave the session.
//
// Once CloseContext is called, the multiplayer session will no longer receive notifications
// about changes in the session even though if the session still exist after leaving.
// CloseContext can be called many times since it internally uses a [sync.Once].
func (s *Session) CloseContext(ctx context.Context) (err error) {
	s.once.Do(func() {
		d := SessionDescription{
			Members: map[string]*MemberDescription{
				// Set myself to nil to leave or close the multiplayer session.
				"me": nil,
			},
		}
		if err2 := s.write(ctx, s.ref.URL(), d); err2 != nil {
			err = errors.Join(err, err2)
		}
		s.client.handleSessionClose(s)
		close(s.closed)
	})
	return err
}

// write commits partial changes to the session resource identified by the given URL.
// The provided [SessionDescription] is treated as a patch and merged server-side.
// The [context.Context] is used for making a PUT request call.
//
// On success, both the local cache and stored ETag are updated to reflect the server response.
func (s *Session) write(ctx context.Context, u *url.URL, changes SessionDescription) error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	ctx = context.WithValue(ctx, internal.ETag, &s.etag)
	if err := s.client.do(ctx, http.MethodPut, u.String(), changes, &s.cache); err != nil {
		return err
	}
	return nil
}

// Context returns a [context.Context] bound to the lifecycle of the Session.
// Callers can use this context as the parent context for making calls involving the multiplayer
// session so they can no longer reference a multiplayer session that is closed.
func (s *Session) Context() context.Context {
	return sessionContext{closed: s.closed}
}

// sessionContext implements [context.Context] that is valid for the lifecycle
// of a multiplayer session. It is returned by [Session.Context] so callers can
// use the context as the parent context for making calls involving the multiplayer
// session.
type sessionContext struct{ closed <-chan struct{} }

// Deadline implements [context.Context.Deadline]. It always returns zero time with false.
func (sessionContext) Deadline() (deadline time.Time, ok bool) {
	return deadline, ok
}

// Done returns a channel that is closed when the multiplayer session is no longer usable.
func (ctx sessionContext) Done() <-chan struct{} {
	return ctx.closed
}

// Err returns [context.Canceled] if the multiplayer session has been closed, or nil if the
// underlying multiplayer session is still usable.
func (ctx sessionContext) Err() error {
	select {
	case <-ctx.closed:
		return context.Canceled
	default:
		return nil
	}
}

// Value implements [context.Context.Value]. It always returns nil for any key.
func (sessionContext) Value(any) any {
	return nil
}

// Sync reconciles the local session state with the remote session state.
// In most cases, callers do not need to call Sync explicitly, as the cache
// is kept up-to-date automatically though RTA subscription.
// The request uses the current ETag to perform a conditional GET when possible.
func (s *Session) Sync(ctx context.Context) error {
	select {
	case <-s.closed:
		return net.ErrClosed
	default:
		s.cacheMu.Lock()
		defer s.cacheMu.Unlock()

		ctx = context.WithValue(ctx, internal.ETag, &s.etag)
		err := s.client.do(ctx, http.MethodGet, s.ref.URL().String(), nil, &s.cache)
		if err != nil {
			return err
		}
		return nil
	}
}

// SetCustomProperties commits the custom properties to the multiplayer session.
// The format or semantics of the custom data is specific to the title. It is
// commonly used to expose session metadata such as display names or
// server details.
func (s *Session) SetCustomProperties(ctx context.Context, custom json.RawMessage) error {
	return s.write(ctx, s.ref.URL(), SessionDescription{
		Properties: &SessionProperties{
			Custom: custom,
		},
	})
}

// Constants returns the immutable session constants.
// The returned value is a copy of the cached session state and is safe
// to modify by the caller.
func (s *Session) Constants() SessionConstants {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	constants := s.cache.Constants
	if constants == nil {
		return SessionConstants{}
	}
	return *constants
}

// Properties returns the mutable session properties.
// The returned value is a copy of the cached session state and is safe
// to modify by the caller.
func (s *Session) Properties() SessionProperties {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	properties := s.cache.Properties
	if properties == nil {
		return SessionProperties{}
	}
	return *properties
}

// Reference returns a reference to the multiplayer session.
// Callers may use this method for referencing the Session in external services in the game.
func (s *Session) Reference() SessionReference {
	return s.ref
}

// Member returns the cached description of the member identified by label.
//
// The label corresponds to a member identifier in the session.
// In addition to concrete member IDs, the special label "me" may be
// used to refer to the currently authenticated caller participating in the session.
//
// The returned [MemberDescription] is a copy of the cached state. The
// boolean result reports whether a non-nil member with the given label
// exists in the cached session state.
func (s *Session) Member(label string) (MemberDescription, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	member, ok := s.cache.Members[label]
	if !ok || member == nil {
		return MemberDescription{}, false
	}
	return *member, true
}

// MemberByXUID returns the cached description of the member identified by their XUID.
//
// The returned [MemberDescription] is a copy of the cached state. The
// boolean result reports whether a non-nil member with the given XUID
// exists in the member list iterator returned from [Session.Members].
func (s *Session) MemberByXUID(xuid string) (MemberDescription, bool) {
	for _, member := range s.Members() {
		if member.Constants != nil && member.Constants.System != nil && member.Constants.System.XUID == xuid {
			return member, true
		}
	}
	return MemberDescription{}, false
}

// Members returns an iterator that yields non-nil members from the cached session state.
// The returned seq operates over a snapshot of the member map taken at the time
// of the call, so it is safe to iterate without holding internal locks.
func (s *Session) Members() iter.Seq2[string, MemberDescription] {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	if s.cache.Members == nil {
		return func(func(string, MemberDescription) bool) {}
	}
	members := maps.Clone(s.cache.Members)
	return func(yield func(string, MemberDescription) bool) {
		for label, member := range members {
			if member == nil {
				continue
			}
			if !yield(label, *member) {
				break
			}
		}
	}
}

// SetMemberCustomProperties updates the custom properties of the specified member.
// The special label "me" refers to the current authenticated caller. Only the owning
// member may modify their own properties.
// The [context.Context] is used for making a PUT request call. Changes are commited
// immediately and reflected in the local cache.
func (s *Session) SetMemberCustomProperties(ctx context.Context, label string, custom json.RawMessage) error {
	return s.write(ctx, s.ref.URL(), SessionDescription{
		Members: map[string]*MemberDescription{
			label: {
				Properties: &MemberProperties{
					Custom: custom,
				},
			},
		},
	})
}

// SessionReference encapsulates a reference to a multiplayer session.
type SessionReference struct {
	// ServiceConfigID is the Xbox Live service configuration ID (SCID)
	// associated with the title.
	//
	// A single service configuration may be shared by multiple titles
	// and platforms for the same game.
	ServiceConfigID uuid.UUID `json:"scid,omitempty"`

	// TemplateName is the name of the session template used to create
	// the session.
	//
	// This value may be used to retrieve the template definition via
	// API.TemplateByName.
	TemplateName string `json:"templateName,omitempty"`

	// Name is the unique identifier of the session.
	//
	// The value is an uppercase UUID (GUID) that uniquely identifies
	// the session within the service configuration.
	Name string `json:"name,omitempty"`
}

// URL returns the URL locating to the HTTP resource of the session.
func (ref SessionReference) URL() *url.URL {
	return endpoint.JoinPath(
		"/serviceconfigs/", ref.ServiceConfigID.String(),
		"/sessionTemplates", ref.TemplateName,
		"/sessions", ref.Name,
	)
}
