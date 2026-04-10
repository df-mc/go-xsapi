package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
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
// If the client loses MPSD subscription tracking, automatic updates stop for
// that Session until it is reattached by the client; see [Session.TrackingLost].
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
	// backgroundSeq is the client's background-work generation when this
	// session was registered. It is used to avoid tearing down sessions created
	// after a prior MPSD subscription-loss event.
	backgroundSeq uint64
	// trackingLost reports whether the session is no longer maintained by the
	// client's automatic MPSD tracking.
	trackingLost atomic.Bool

	// etag holds the most recently observed E-Tag for the session resource.
	// When reading or accessing etag, cacheMu must be held for concurrent safety.
	etag string
	// cache holds the SessionDescription for the multiplayer session in the last known state.
	// Callers can always refresh this cache to the remote state using [Session.Sync] method.
	cache SessionDescription
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
	// closeMu serializes remote close attempts so that a failed close can be retried
	// without racing a successful one.
	closeMu sync.Mutex
}

// Close closes the multiplayer session using a context with 15 seconds timeout.
//
// If the caller is the host of the multiplayer session, the session itself is closed.
// If the caller is a non-host participant, this call ensures the caller to leave the session.
//
// Once CloseContext succeeds, the multiplayer session will no longer receive notifications
// about changes in the session even though if the session still exist after leaving.
// If the remote leave or close request fails, the Session remains usable and CloseContext may be retried.
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
// Once CloseContext succeeds, the multiplayer session will no longer receive notifications
// about changes in the session even though if the session still exist after leaving.
// If the remote leave or close request fails, the Session remains usable and CloseContext may be retried.
func (s *Session) CloseContext(ctx context.Context) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()

	select {
	case <-s.closed:
		return nil
	default:
	}

	d := SessionDescription{
		Members: map[string]*MemberDescription{
			// Set myself to nil to leave or close the multiplayer session.
			"me": nil,
		},
	}
	deleted, err := s.update(ctx, d, nil)
	if err != nil {
		return err
	}
	if deleted {
		s.markDeletedLocked()
		return nil
	}
	s.closeLocked()
	return nil
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

// update commits partial changes to the session resource identified by the given URL.
// The provided [SessionDescription] is treated as a patch and merged server-side.
// The [context.Context] is used for making a PUT request call.
//
// On 200 OK, the local cache and stored ETag are updated from the returned
// session body and deleted is false.
//
// On 204 No Content, MPSD documents that the session was deleted as a result
// of the PUT. In that case, deleted is true and the caller is responsible for
// transitioning the local Session into a deleted/closed state.
func (s *Session) update(ctx context.Context, changes SessionDescription, opts []internal.RequestOption) (deleted bool, err error) {
	deleted, _, err = s.commit(ctx, changes, preconditionWildcard, opts)
	return deleted, err
}

// commit writes a partial session update using the requested precondition mode.
// It reports whether the write deleted the session and, for synchronized
// ETag-based writes, whether the caller should treat a 412 as a retryable
// conflict instead of a terminal error.
func (s *Session) commit(ctx context.Context, changes SessionDescription, precondition updatePrecondition, opts []internal.RequestOption) (deleted, conflict bool, err error) {
	select {
	case <-s.closed:
		return false, false, net.ErrClosed
	default:
	}

	match, err := s.ifMatchHeader(ctx, precondition)
	if err != nil {
		return false, false, err
	}
	req, err := internal.WithJSONBody(ctx, http.MethodPut, s.ref.URL().String(), changes, append(opts,
		internal.RequestHeader("Content-Type", "application/json"),
		internal.RequestHeader("If-Match", match),
		internal.ContractVersion(contractVersion),
	))
	if err != nil {
		return false, false, fmt.Errorf("make request: %w", err)
	}

	resp, err := s.client.client.Do(req)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return false, false, s.sync(resp)
	case http.StatusNoContent:
		return true, false, nil
	case http.StatusPreconditionFailed:
		// Only synchronized ETag-based writes treat 412 as a retryable conflict.
		if precondition == preconditionCachedETag {
			return false, true, nil
		}
		return false, false, internal.UnexpectedStatusCode(resp)
	default:
		return false, false, internal.UnexpectedStatusCode(resp)
	}
}

// updatePrecondition is a type that represents the precondition for an update operation.
// It is used to determine whether the update should be treated as a conflict or not.
type updatePrecondition uint8

const (
	// preconditionWildcard uses If-Match: * and is appropriate for writes that
	// should succeed regardless of the currently observed session ETag.
	preconditionWildcard updatePrecondition = iota
	// preconditionCachedETag uses the last observed session ETag and is
	// appropriate for synchronized writes to shared session state.
	preconditionCachedETag
)

// errSessionDeleted is returned when the session is deleted.
var errSessionDeleted = errors.New("mpsd: session deleted")

// ifMatchHeader returns the If-Match header value based on the precondition.
func (s *Session) ifMatchHeader(ctx context.Context, precondition updatePrecondition) (string, error) {
	switch precondition {
	case preconditionWildcard:
		return "*", nil
	case preconditionCachedETag:
		return s.currentETag(ctx)
	default:
		panic("unreachable")
	}
}

// markDeletedLocked finalizes the local Session after MPSD reports that the
// remote session no longer exists.
//
// It clears the cached session data and ETag, unregisters the Session from its
// parent Client so it no longer receives RTA updates, and closes s.closed so
// future operations fail as if the Session had been closed.
func (s *Session) markDeleted() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	s.markDeletedLocked()
}

// markDeletedLocked is markDeleted with s.closeMu already held by the caller.
func (s *Session) markDeletedLocked() {
	s.cacheMu.Lock()
	s.cache = SessionDescription{}
	s.etag = ""
	s.cacheMu.Unlock()

	s.closeLocked()
}

// closeLocked finalizes the local Session after it is no longer usable by the
// caller.
//
// It unregisters the Session from its parent Client so it no longer receives
// RTA updates and closes s.closed so future operations fail as if the Session
// had been closed.
func (s *Session) closeLocked() {
	select {
	case <-s.closed:
	default:
		s.client.handleSessionClose(s)
		close(s.closed)
	}
}

// stopTrackingLocal unregisters the Session from automatic client-side
// tracking without attempting a remote leave or delete request. It is used
// when the client can no longer maintain multiplayer session state after MPSD
// subscription loss, while still allowing callers to explicitly close or leave
// the remote session later.
func (s *Session) stopTrackingLocal(lossSeq uint64) {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.isClosed() {
		return
	}
	if s.backgroundSeq >= lossSeq {
		return
	}
	s.markTrackingLostLocked()
}

// markTrackingLost marks the session as no longer maintained by the client's
// automatic MPSD tracking and removes it from the client's tracked-session map
// if it is still registered there.
func (s *Session) markTrackingLost() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.isClosed() {
		return
	}
	s.markTrackingLostLocked()
}

// markTrackingLostLocked is markTrackingLost with s.closeMu already held.
func (s *Session) markTrackingLostLocked() {
	s.client.handleSessionClose(s)
	s.trackingLost.Store(true)
}

// TrackingLost reports whether the session is no longer kept up to date
// automatically through the client's MPSD tracking.
func (s *Session) TrackingLost() bool {
	return s.trackingLost.Load()
}

// isClosed checks if the session is closed.
func (s *Session) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
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
			return s.sync(resp)
		case http.StatusNotModified:
			return nil
		case http.StatusNoContent:
			s.markDeleted()
			return nil
		default:
			return internal.UnexpectedStatusCode(resp)
		}
	}
}

// sync decodes the response body into a fresh [SessionDescription] and
// updates the internal cache and last observed ETag.
//
// A fresh [SessionDescription] is always allocated before decoding so that
// members absent from the response are not retained from the previous cache.
// If the response does not include an ETag header, the existing ETag is preserved.
//
// The caller is responsible for closing the response body.
// An error is returned if the response body cannot be decoded.
func (s *Session) sync(resp *http.Response) error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	// A fresh SessionDescription is allocated so that members absent from
	// the response are not retained from the previous cache.
	var d SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return fmt.Errorf("decode response body: %w", err)
	}
	s.cache = d
	if e := resp.Header.Get("ETag"); e != "" {
		s.etag = e // Update the last observed ETag.
	}
	return nil
}

// SetCustomProperties commits the custom properties to the multiplayer session.
// The format or semantics of the custom data is specific to the title. It is
// commonly used to expose session metadata such as display names or
// server details.
func (s *Session) SetCustomProperties(ctx context.Context, custom json.RawMessage, opts ...internal.RequestOption) error {
	return s.finishUpdate(s.synchronizedUpdate(ctx, SessionDescription{
		Properties: &SessionProperties{
			Custom: custom,
		},
	}, opts))
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
	return *cloneSessionConstants(constants)
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
	return *cloneSessionProperties(properties)
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
	return *cloneMemberDescription(member), true
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
	members := make(map[string]MemberDescription, len(s.cache.Members))
	for label, member := range s.cache.Members {
		if member == nil {
			continue
		}
		members[label] = *cloneMemberDescription(member)
	}
	return func(yield func(string, MemberDescription) bool) {
		for label, member := range members {
			if !yield(label, member) {
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
func (s *Session) SetMemberCustomProperties(ctx context.Context, label string, custom json.RawMessage, opts ...internal.RequestOption) error {
	return s.finishUpdate(s.update(ctx, SessionDescription{
		Members: map[string]*MemberDescription{
			label: {
				Properties: &MemberProperties{
					Custom: custom,
				},
			},
		},
	}, opts))
}

// finishUpdate converts a deleted-session result into local teardown and
// otherwise returns the original update error/result unchanged.
func (s *Session) finishUpdate(deleted bool, err error) error {
	if err != nil {
		return err
	}
	if deleted {
		s.markDeleted()
	}
	return nil
}

// synchronizedUpdate is for writes to shared session state that must
// participate in MPSD's ETag-based optimistic concurrency flow. It retries on
// 412 responses after refreshing local state, and treats a remotely deleted
// session as a successful delete.
func (s *Session) synchronizedUpdate(ctx context.Context, changes SessionDescription, opts []internal.RequestOption) (deleted bool, err error) {
	return s.synchronizedUpdateWhile(ctx, changes, opts, nil)
}

// synchronizedUpdateWhile is synchronizedUpdate with an additional
// shouldContinue predicate that is checked before each attempt. When
// shouldContinue returns false the update is aborted with [context.Canceled].
func (s *Session) synchronizedUpdateWhile(ctx context.Context, changes SessionDescription, opts []internal.RequestOption, shouldContinue func() bool) (deleted bool, err error) {
	if shouldContinue == nil {
		shouldContinue = func() bool { return true }
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !shouldContinue() {
			return false, context.Canceled
		}

		deleted, conflict, err := s.commit(ctx, changes, preconditionCachedETag, opts)
		if errors.Is(err, errSessionDeleted) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if !conflict {
			return deleted, nil
		}
		if !shouldContinue() {
			return false, context.Canceled
		}
		if err := s.Sync(ctx); err != nil {
			return false, err
		}
		if s.isClosed() {
			return true, nil
		}
	}
}

// currentETag returns the last observed session ETag, refreshing it from MPSD
// if necessary. If the refresh discovers that the session no longer exists,
// errSessionDeleted is returned.
func (s *Session) currentETag(ctx context.Context) (string, error) {
	// If the ETag is already set, return it.
	s.cacheMu.RLock()
	etag := s.etag
	s.cacheMu.RUnlock()
	if etag != "" {
		return etag, nil
	}

	// Sync the session state from MPSD.
	if err := s.Sync(ctx); err != nil {
		return "", err
	}

	// Return the ETag from the synced session state.
	s.cacheMu.RLock()
	etag = s.etag
	s.cacheMu.RUnlock()
	if etag == "" {
		// A sync that closed the session means the remote session was deleted.
		if s.isClosed() {
			return "", errSessionDeleted
		}
		return "", errors.New("mpsd: synchronized update requires ETag")
	}
	return etag, nil
}

// refreshConnection writes the given connection ID to the session's member
// properties, marking the current user as active.
func (s *Session) refreshConnection(ctx context.Context, connectionID uuid.UUID) error {
	return s.refreshConnectionWhile(ctx, connectionID, nil)
}

// refreshConnectionWhile is refreshConnection with an additional
// shouldContinue predicate passed through to [Session.synchronizedUpdateWhile].
func (s *Session) refreshConnectionWhile(ctx context.Context, connectionID uuid.UUID, shouldContinue func() bool) error {
	return s.finishUpdate(s.synchronizedUpdateWhile(ctx, SessionDescription{
		Members: map[string]*MemberDescription{
			"me": {
				Properties: &MemberProperties{
					System: &MemberPropertiesSystem{
						// MPSD reconnect handling expects the title to mark the
						// current user active again when rebinding to a new
						// connection ID after RTA reconnect via the current-user
						// status flow, not just rewrite the connection field.
						Active:     true,
						Connection: connectionID,
					},
				},
			},
		},
	}, nil, shouldContinue))
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

// parseSessionReference parses a [SessionReference] from the value of the
// 'Content-Location' header.
//
// The value may be either a relative path or an absolute URL, as both are
// permitted by the HTTP specification. In either case, the path component
// must follow this structure:
//
//	"/serviceconfigs/<scid>/sessionTemplates/<templateName>/sessions/<name>"
//
// If the path does not match this pattern or does not refer to the
// multiplayer session, an error will be returned.
func parseSessionReference(loc string) (ref SessionReference, err error) {
	// [url.Parse] accepts both relative and absolute URLs, making it suitable
	// for parsing 'Content-Location' values regardless of form.
	u, err := url.Parse(loc)
	if err != nil {
		return SessionReference{}, fmt.Errorf("parse as URL: %w", err)
	}

	segments := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(segments) != 6 {
		return ref, fmt.Errorf("malformed path: %q", u.Path)
	}
	if !strings.EqualFold(segments[0], "serviceconfigs") || !strings.EqualFold(segments[2], "sessionTemplates") || segments[4] != "sessions" {
		return ref, fmt.Errorf("invalid path to session: %q", u.Path)
	}

	ref.ServiceConfigID, err = uuid.Parse(segments[1])
	if err != nil {
		return ref, fmt.Errorf("parse service config ID: %w", err)
	}
	ref.TemplateName, ref.Name = segments[3], segments[5]
	return ref, nil
}
