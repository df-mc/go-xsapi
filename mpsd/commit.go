package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"
)

// Commit pushes a [SessionDescription] into the session, updating properties and other fields
// on the service.
func (s *Session) Commit(ctx context.Context, d *SessionDescription) (*Commit, error) {
	return s.conf.commit(ctx, s.ref.URL(), d)
}

// commit puts a [SessionDescription] on the URL. It is used for creating and updating the description
// of the Session.
func (conf PublishConfig) commit(ctx context.Context, u *url.URL, d *SessionDescription) (*Commit, error) {
	buf := &bytes.Buffer{}
	if err := json.NewEncoder(buf).Encode(d); err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), buf)
	if err != nil {
		return nil, fmt.Errorf("make request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Xbl-Contract-Version", strconv.Itoa(contractVersion))

	resp, err := conf.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		var commitment *Commit
		if err := json.NewDecoder(resp.Body).Decode(&commitment); err != nil {
			return nil, fmt.Errorf("decode response body: %w", err)
		}
		return commitment, nil
	case http.StatusNoContent:
		return nil, nil
	default:
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL, resp.Status)
	}
}

// A SessionReference contains a reference to a Session.
type SessionReference struct {
	ServiceConfigID uuid.UUID `json:"scid,omitempty"`
	TemplateName    string    `json:"templateName,omitempty"`
	Name            string    `json:"name,omitempty"`
}

// URL returns the [url.URL] of the session referenced in SessionReference.
func (ref SessionReference) URL() *url.URL {
	return &url.URL{
		Scheme: "https",
		Host:   "sessiondirectory.xboxlive.com",
		Path: path.Join(
			"/serviceconfigs/", ref.ServiceConfigID.String(),
			"/sessionTemplates/", ref.TemplateName,
			"/sessions/", ref.Name,
		),
	}
}

// Commit includes a [SessionDescription] returned as a response body from the service.
// It can be retrieved on [Session.Query], [Query], and [Session.Commit].
type Commit struct {
	ContractVersion uint32    `json:"contractVersion,omitempty"`
	CorrelationID   uuid.UUID `json:"correlationId,omitempty"`
	SearchHandle    uuid.UUID `json:"searchHandle,omitempty"`
	Branch          uuid.UUID `json:"branch,omitempty"`
	ChangeNumber    uint64    `json:"changeNumber,omitempty"`
	StartTime       time.Time `json:"startTime,omitempty"`
	NextTimer       time.Time `json:"nextTimer,omitempty"`

	*SessionDescription
}

const contractVersion = 107
