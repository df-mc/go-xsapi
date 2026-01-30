package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/yomoggies/xsapi-go/internal"
)

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

	etag    atomic.Pointer[string]
	cache   *SessionDescription
	cacheMu sync.Mutex

	ctx    context.Context
	cancel context.CancelCauseFunc
	once   sync.Once
}

func (s *Session) Close() error {
	ctx, cancel := context.WithTimeout(s.Context(), time.Second*15)
	defer cancel()
	return s.CloseContext(ctx)
}

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
	})
	return err
}

func (s *Session) write(ctx context.Context, u *url.URL, changes SessionDescription) error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	ctx = context.WithValue(ctx, internal.ETag, &s.etag)
	if err := s.client.do(ctx, http.MethodPut, u.String(), changes, &s.cache); err != nil {
		return err
	}
	return nil
}

func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) Sync(ctx context.Context) error {
	select {
	case <-s.ctx.Done():
		return context.Cause(s.ctx)
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

func (s *Session) Constants() SessionConstants {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	constants := s.cache.Constants
	if constants == nil {
		return SessionConstants{}
	}
	return *constants
}

func (s *Session) Properties() SessionProperties {
	s.cacheMu.Unlock()
	defer s.cacheMu.Unlock()

	properties := s.cache.Properties
	if properties == nil {
		return SessionProperties{}
	}
	return *properties
}

func (s *Session) Reference() SessionReference {
	return s.ref
}

func (s *Session) Member(label string) (MemberDescription, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	member, ok := s.cache.Members[label]
	if !ok || member == nil {
		return MemberDescription{}, false
	}
	return *member, true
}

func (s *Session) Members() iter.Seq2[string, MemberDescription] {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	if s.cache.Members == nil {
		return nil
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

const MemberSelf = "me"

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
