package mpsd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

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

	d := &SessionDescription{
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
						XUID:       c.api.UserInfo().XUID,
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

	s, err := c.createSession(ctx, ref, ref.URL(), d)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return s, nil
}

func (c *Client) createSession(ctx context.Context, ref SessionReference, u *url.URL, d *SessionDescription) (*Session, error) {
	s := &Session{
		client: c,
		log: c.api.Log().With(
			slog.Group("session",
				slog.String("scid", ref.ServiceConfigID.String()),
				slog.String("templateName", ref.TemplateName),
				slog.String("name", ref.Name),
			),
		),

		ref:   ref,
		cache: d,
	}
	s.ctx, s.cancel = context.WithCancelCause(context.Background())

	if err := s.write(ctx, u, d); err != nil {
		return nil, fmt.Errorf("write session description: %w", err)
	}
	if err := s.writeActivity(ctx); err != nil {
		return nil, fmt.Errorf("write activity for session: %w", err)
	}

	return s, nil
}
