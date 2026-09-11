package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"github.com/google/uuid"
)

func TestJoinRetriesPreconditionFailedUsingETag(t *testing.T) {
	connectionID := uuid.New()
	handleID := uuid.New()
	var (
		requests  int
		ifMatches []string
	)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		ifMatches = append(ifMatches, req.Header.Get("If-Match"))
		return &http.Response{
			StatusCode: http.StatusPreconditionFailed,
			Status:     "412 Precondition Failed",
			Header:     http.Header{"Etag": []string{fmt.Sprintf(`"etag-%d"`, requests)}},
			Body:       io.NopCloser(strings.NewReader(`{"members":{}}`)),
			Request:    req,
		}, nil
	})}
	client := newJoinTestClient(httpClient, connectionID)

	_, err := client.Join(context.Background(), handleID, JoinConfig{})
	if err == nil {
		t.Fatal("Join succeeded, want precondition error after retries")
	}
	wantError := fmt.Sprintf("PUT https://sessiondirectory.xboxlive.com/handles/%s/session: 412 Precondition Failed", handleID)
	if got := err.Error(); got != wantError {
		t.Fatalf("Join error = %q, want %q", got, wantError)
	}
	if requests != joinMaxAttempts {
		t.Fatalf("requests = %d, want %d", requests, joinMaxAttempts)
	}
	wantIfMatches := []string{"*", `"etag-1"`, `"etag-2"`}
	if !slices.Equal(ifMatches, wantIfMatches) {
		t.Fatalf("If-Match values = %v, want %v", ifMatches, wantIfMatches)
	}
}

func TestJoinSucceedsAfterPreconditionFailed(t *testing.T) {
	connectionID := uuid.New()
	handleID := uuid.New()
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	var (
		putRequests int
		ifMatches   []string
	)
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodPut && strings.HasSuffix(req.URL.Path, "/session"):
			putRequests++
			ifMatches = append(ifMatches, req.Header.Get("If-Match"))
			if putRequests == 1 {
				return &http.Response{
					StatusCode: http.StatusPreconditionFailed,
					Status:     "412 Precondition Failed",
					Header:     http.Header{"Etag": []string{`"latest-etag"`}},
					Body:       io.NopCloser(strings.NewReader(`{"members":{}}`)),
					Request:    req,
				}, nil
			}

			description, err := json.Marshal(SessionDescription{
				Members: map[string]*MemberDescription{
					"me": {
						Properties: &MemberProperties{
							System: &MemberPropertiesSystem{
								Active:     true,
								Connection: connectionID,
							},
						},
					},
				},
			})
			if err != nil {
				return nil, err
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Location": []string{ref.URL().String()},
					"Etag":             []string{`"joined-etag"`},
				},
				Body:    io.NopCloser(strings.NewReader(string(description))),
				Request: req,
			}, nil
		case req.Method == http.MethodPost && req.URL.Path == "/handles":
			return &http.Response{
				StatusCode: http.StatusCreated,
				Status:     "201 Created",
				Body:       http.NoBody,
				Request:    req,
			}, nil
		default:
			return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL)
		}
	})}
	client := newJoinTestClient(httpClient, connectionID)

	session, err := client.Join(context.Background(), handleID, JoinConfig{})
	if err != nil {
		t.Fatalf("Join returned error: %v", err)
	}
	if !session.Reference().Equal(ref) {
		t.Fatalf("session reference = %+v, want %+v", session.Reference(), ref)
	}
	if putRequests != 2 {
		t.Fatalf("PUT requests = %d, want 2", putRequests)
	}
	wantIfMatches := []string{"*", `"latest-etag"`}
	if !slices.Equal(ifMatches, wantIfMatches) {
		t.Fatalf("If-Match values = %v, want %v", ifMatches, wantIfMatches)
	}
}

func TestJoinPreconditionRetryHonorsContext(t *testing.T) {
	connectionID := uuid.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests int
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		cancel()
		return &http.Response{
			StatusCode: http.StatusPreconditionFailed,
			Status:     "412 Precondition Failed",
			Header:     http.Header{"Etag": []string{`"latest-etag"`}},
			Body:       io.NopCloser(strings.NewReader(`{"members":{}}`)),
			Request:    req,
		}, nil
	})}
	client := newJoinTestClient(httpClient, connectionID)

	_, err := client.Join(ctx, uuid.New(), JoinConfig{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Join error = %v, want context cancellation", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func newJoinTestClient(httpClient *http.Client, connectionID uuid.UUID) *Client {
	client := New(httpClient, rta.NewProvider(
		subscriberFunc(func(context.Context, *rta.Subscription) error { return nil }),
		nil,
	), xsts.UserInfo{XUID: "1"}, nil)
	client.subscriptionData.Store(&subscriptionData{ConnectionID: connectionID})
	return client
}
