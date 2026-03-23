package mpsd

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
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
	// When reading or accessing etag, cacheMu must be held for concurrent safety.
	etag string
	// cache holds the SessionDescription for the multiplayer session in the last known state.
	// Callers can always refresh this cache to the remote state using [Session.Sync] method.
	cache *SessionDescription
	// cacheMu guards the cache from concurrent read-write access.
	cacheMu sync.RWMutex

	// h is the Handler registered to this Session to receive updates from RTA.
	h Handler
	// hMu guards h from concurrent read/write access.
	hMu sync.RWMutex

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
		if _, err2 := s.write(ctx, s.ref.URL(), d, internal.RequestHeader("If-Match", "*")); err2 != nil {
			err = err2
		}
		s.client.handleSessionClose(s)
		close(s.closed)
	})
	return err
}

// Handle registers h as the [Handler] for this session. The registered handler
// is called when an event is received over the RTA (Real-Time Activity)
// subscription, such as when a member joins or leaves the session. Passing nil
// falls back to [NopHandler].
func (s *Session) Handle(h Handler) {
	if h == nil {
		h = NopHandler{}
	}
	s.hMu.Lock()
	s.h = h
	s.hMu.Unlock()
}

// handler returns the [Handler] currently registered for this session.
func (s *Session) handler() Handler {
	s.hMu.RLock()
	defer s.hMu.RUnlock()
	return s.h
}

// write commits partial changes to the session resource identified by the given URL.
// The provided [SessionDescription] is treated as a patch and merged server-side.
// The [context.Context] is used for making a PUT request call.
//
// On success, both the local cache and stored ETag are updated to reflect the server response.
func (s *Session) write(ctx context.Context, u *url.URL, changes SessionDescription, opts ...internal.RequestOption) (*http.Response, error) {
	select {
	case <-s.closed:
		return nil, net.ErrClosed
	default:
	}

	req, err := internal.WithJSONBody(ctx, http.MethodPut, u.String(), changes, append(opts,
		internal.RequestHeader("Content-Type", "application/json"),
		internal.ContractVersion(contractVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := s.client.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		s.cacheMu.Lock()
		defer s.cacheMu.Unlock()

		if err := json.NewDecoder(resp.Body).Decode(&s.cache); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		if e := resp.Header.Get("ETag"); e != "" {
			s.etag = e
		}
		return resp, nil
	case http.StatusNotModified, http.StatusNoContent:
		return nil, nil
	default:
		return nil, internal.UnexpectedStatusCode(resp)
	}
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
	case <-ctx.Done():
		return ctx.Err()
	default:
		s.cacheMu.RLock()
		etag := s.etag
		s.cacheMu.RUnlock()

		req, err := internal.NewRequest(ctx, http.MethodGet, s.ref.URL().String(), nil, []internal.RequestOption{
			internal.RequestHeader("Accept", "application/json"),
			internal.RequestHeader("If-None-Match", etag),
			internal.ContractVersion(contractVersion),
		})
		if err != nil {
			return fmt.Errorf("make request: %w", err)
		}

		resp, err := s.client.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusOK:
			s.cacheMu.Lock()
			defer s.cacheMu.Unlock()

			// I think re-using s.cache may reduce allocations?
			if err := json.NewDecoder(resp.Body).Decode(&s.cache); err != nil {
				return fmt.Errorf("decode response body: %w", err)
			}
			if e := resp.Header.Get("ETag"); e != "" {
				s.etag = e // Update the last observed ETag.
			}
			return nil
		case http.StatusNotModified:
			return nil
		default:
			return internal.UnexpectedStatusCode(resp)
		}
	}
}

// SetCustomProperties commits the custom properties to the multiplayer session.
// The format or semantics of the custom data is specific to the title. It is
// commonly used to expose session metadata such as display names or
// server details.
func (s *Session) SetCustomProperties(ctx context.Context, custom json.RawMessage) error {
	_, err := s.write(ctx, s.ref.URL(), SessionDescription{
		Properties: &SessionProperties{
			Custom: custom,
		},
	}, internal.RequestHeader("If-Match", "*"))

	// TODO: Should we still use a shared method or split depending on usage? i.e. update()?
	// TODO: Can we use the current etag for 'If-Match' header?

	return err
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
	_, err := s.write(ctx, s.ref.URL(), SessionDescription{
		Members: map[string]*MemberDescription{
			label: {
				Properties: &MemberProperties{
					Custom: custom,
				},
			},
		},
	})
	return err
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

// parseSessionReference parses a SessionReference from the path contained in the
// 'Content-Location' header.
//
// The path must refer to a session reference in the form
//
//	"/serviceconfigs/<scid>/sessionTemplates/<templateName>/sessions/<name>"
//
// If the path does not match this pattern or does not refer to the
// multiplayer session, an error will be returned.
func parseSessionReference(path string) (ref SessionReference, err error) {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) != 6 {
		return ref, fmt.Errorf("malformed path: %q", path)
	}
	if !strings.EqualFold(segments[0], "serviceconfigs") || !strings.EqualFold(segments[2], "sessionTemplates") || segments[4] != "sessions" {
		return ref, fmt.Errorf("invalid path to session: %q", path)
	}

	ref.ServiceConfigID, err = uuid.Parse(segments[1])
	if err != nil {
		return ref, fmt.Errorf("parse service config ID: %w", err)
	}
	ref.TemplateName, ref.Name = segments[3], segments[5]
	return ref, nil
}
