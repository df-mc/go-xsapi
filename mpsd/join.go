package mpsd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type JoinConfig struct {
	CustomMemberConstants  json.RawMessage
	CustomMemberProperties json.RawMessage
}

func (c *Client) Join(ctx context.Context, activity ActivityHandle, config JoinConfig) (*Session, error) {
	_, payload, err := c.subscribe(ctx)
	if err != nil {
		return nil, err
	}

	d := SessionDescription{
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

	s, err := c.createSession(ctx, activity.SessionReference, activity.URL().JoinPath("session"), d)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return s, nil
}
