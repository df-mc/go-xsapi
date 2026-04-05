package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/google/uuid"
)

// PublishConfig describes a configuration for publishing a multiplayer session
// in the directory. It can be passed to [Client.Publish] to customize the session's
// initial contents.
type PublishConfig struct {
	// CustomProperties holds mutable properties to be associated with the multiplayer session
	// when publishing.
	//
	// The format and semantics of this field are defined by the title. It is
	// commonly used to expose session metadata such as display names or
	// server details.
	CustomProperties json.RawMessage
	// CustomConstants holds immutable constants to be associated with the multiplayer session
	// when publishing. Once published, it cannot be changed during for the lifetime of the session.
	//
	// The format and semantics of this field are defined by the title.
	CustomConstants json.RawMessage

	// CustomMemberProperties holds mutable properties associated with the host.
	// Unlike [JoinConfig.CustomMemberConstants], these can be updated at any time
	// during the session via [Session.SetMemberCustomProperties].
	//
	// The format and semantics of this field are defined by the title.
	CustomMemberProperties json.RawMessage
	// CustomMemberConstants holds immutable constants associated with the host.
	// These are set when publishing and cannot be changed for the lifetime of the ownership.
	//
	// The format and semantics of this field are defined by the title.
	CustomMemberConstants json.RawMessage

	// JoinRestriction and ReadRestriction specify who may join or read an open session.
	// If JoinRestriction or ReadRestriction are empty, it will default to [SessionRestrictionFollowed].
	JoinRestriction, ReadRestriction string
}

// Publish publishes a new multiplayer session in the directory using the
// provided [SessionReference]. The provided [PublishConfig] is applied to the
// session's initial contents.
//
// If [SessionReference.Name] is empty, a randomly-generated GUID will be used.
// Make sure to call [Session.Close] to close the session when it is no longer needed.
func (c *Client) Publish(ctx context.Context, ref SessionReference, config PublishConfig, opts ...internal.RequestOption) (*Session, error) {
	if ref.Name == "" {
		ref.Name = strings.ToUpper(uuid.NewString())
	}
	if config.JoinRestriction == "" {
		config.JoinRestriction = SessionRestrictionFollowed
	}
	if config.ReadRestriction == "" {
		config.ReadRestriction = SessionRestrictionFollowed
	}
	_, payload, err := c.subscribe(ctx)
	if err != nil {
		return nil, err
	}

	d := SessionDescription{
		Properties: &SessionProperties{
			System: &SessionPropertiesSystem{
				JoinRestriction: config.JoinRestriction,
				ReadRestriction: config.ReadRestriction,
			},
			Custom: config.CustomProperties,
		},
		Members: map[string]*MemberDescription{
			"me": {
				Constants: &MemberConstants{
					System: &MemberConstantsSystem{
						Initialize: true,
						XUID:       c.userInfo.XUID,
					},
					Custom: config.CustomMemberConstants,
				},
				Properties: &MemberProperties{
					System: &MemberPropertiesSystem{
						Active:     true,
						Connection: payload.ConnectionID,
						Subscription: &MemberPropertiesSystemSubscription{
							ID:          strings.ToUpper(uuid.NewString()),
							ChangeTypes: []string{ChangeTypeEverything},
						},
					},
					Custom: config.CustomMemberProperties,
				},
			},
		},
	}
	if config.CustomConstants != nil {
		d.Constants = &SessionConstants{
			Custom: config.CustomConstants,
		}
	}

	// Newly create a multiplayer session.
	// This request call will fail if the session already exists.
	req, err := internal.WithJSONBody(ctx, http.MethodPut, ref.URL().String(), d, append(opts,
		internal.RequestHeader("Content-Type", "application/json"),
		internal.RequestHeader("If-None-Match", "*"),
		internal.ContractVersion(contractVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		return c.createSessionAndReconcile(ctx, ref, resp, payload.ConnectionID, "publish")
	default:
		return nil, internal.UnexpectedStatusCode(resp)
	}
}

// createSessionAndReconcile creates a session from the HTTP response and then
// ensures its connection ID matches the current subscription data. If the
// immediate reconciliation fails, a background retry is spawned so the caller
// still receives the session without blocking.
func (c *Client) createSessionAndReconcile(ctx context.Context, ref SessionReference, resp *http.Response, connectionID uuid.UUID, action string) (*Session, error) {
	s, err := c.createSession(ctx, ref, resp)
	if err != nil {
		return nil, err
	}
	if err := c.reconcileSessionConnection(ctx, s, connectionID); err != nil {
		s.log.Error("error reconciling session connection after "+action, slog.Any("error", err))
		go c.retryReconcileSessionConnection(s, connectionID, c.backgroundSeq.Load())
	}
	return s, nil
}

// createSession creates a multiplayer session on the directory using the URL.
// The URL may be a session reference or the handle referencing the session to join.
// When joining an existing multiplayer session, the session reference may be
// nil or unavailable in the context. In that case, the session reference will
// be automatically derived from the Content-Location header in the first request call.
// The initial response will be used to decode the initial contents of the remote session.
// The caller should close the response body after calling this method.
func (c *Client) createSession(ctx context.Context, ref SessionReference, resp *http.Response) (*Session, error) {
	var d SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("decode initial contents: %w", err)
	}
	s := &Session{
		client: c,

		h:      NopHandler{}, // fast-path without locking
		cache:  d,
		etag:   resp.Header.Get("ETag"),
		closed: make(chan struct{}),
		ref:    ref,
	}

	s.log = c.log.With(
		slog.Group("session",
			slog.String("scid", s.ref.ServiceConfigID.String()),
			slog.String("templateName", s.ref.TemplateName),
			slog.String("name", s.ref.Name),
		),
	)

	if err := s.writeActivity(ctx); err != nil {
		err = fmt.Errorf("write activity handle: %w", err)
		if err2 := s.Close(); err2 != nil {
			err = errors.Join(
				err,
				fmt.Errorf("close session: %w", err2),
			)
		}
		return nil, err
	}

	// Bind the session to the client so we can receive updates from RTA subscription.
	c.sessionsMu.Lock()
	c.sessions[s.ref.URL().String()] = s
	c.sessionsMu.Unlock()

	return s, nil
}

// handleSessionClosure handles closure of a multiplayer session.
// It releases the multiplayer session from the client so it can no
// longer receive notifications from the RTA subscription.
func (c *Client) handleSessionClose(s *Session) {
	c.sessionsMu.Lock()
	key := s.ref.URL().String()
	if current := c.sessions[key]; current == s {
		delete(c.sessions, key)
	}
	c.sessionsMu.Unlock()
}
