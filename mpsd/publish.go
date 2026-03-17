package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/df-mc/go-xsapi/internal"
	"github.com/google/uuid"
)

type PublishConfig struct {
	CustomProperties json.RawMessage
	CustomConstants  json.RawMessage

	CustomMemberProperties json.RawMessage
	CustomMemberConstants  json.RawMessage

	JoinRestriction, ReadRestriction string
}

func (c *Client) Publish(ctx context.Context, ref SessionReference, config PublishConfig) (*Session, error) {
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

	s, err := c.createSession(ctx, &ref, ref.URL(), d)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return s, nil
}

// createSession creates a multiplayer session on the directory using the URL.
// The URL may be a session reference or the handle referencing the session to join.
// When joining an existing multiplayer session, the session reference may be
// nil or unavailable in the context. In that case, the session reference will
// be automatically derived from the Content-Location header in the first request call.
func (c *Client) createSession(ctx context.Context, knownRef *SessionReference, u *url.URL, d SessionDescription) (*Session, error) {
	s := &Session{
		client: c,

		h:      NopHandler{}, // fast-path without locking
		cache:  &d,
		closed: make(chan struct{}),
	}

	if knownRef == nil {
		// Join the multiplayer session by updating the members field to add the caller as participant.
		// This request call will fail if the multiplayer session does not exist.
		resp, err := s.write(ctx, u, d, internal.RequestHeader("If-Match", "*"))
		if err != nil {
			return nil, fmt.Errorf("write session using handle: %w", err)
		}
		if resp == nil {
			// For 304/204 responses, resp may be nil.
			return nil, errors.New("mpsd: session was unmodified")
		}
		contentLocation := resp.Header.Get("Content-Location")
		if contentLocation == "" {
			return nil, fmt.Errorf("Content-Location response header is absent from response")
		}
		s.ref, err = parseSessionReference(contentLocation)
		if err != nil {
			return nil, fmt.Errorf("parse session reference from Content-Location response header: %w", err)
		}
	} else {
		// Newly create a multiplayer session.
		// This request call will fail if the session already exists.
		resp, err := s.write(ctx, u, d, internal.RequestHeader("If-None-Match", "*"))
		if err != nil {
			return nil, fmt.Errorf("create session: %w", err)
		}
		if resp == nil {
			// For 304/204 responses, resp may be nil.
			return nil, errors.New("mpsd: session was not created")
		}
		s.ref = *knownRef
	}
	s.log = c.log.With(
		slog.Group("session",
			slog.String("scid", s.ref.ServiceConfigID.String()),
			slog.String("templateName", s.ref.TemplateName),
			slog.String("name", s.ref.Name),
		),
	)

	if err := s.writeActivity(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("write session activity: %w", err)
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
	delete(c.sessions, s.ref.URL().String())
	c.sessionsMu.Unlock()
}
