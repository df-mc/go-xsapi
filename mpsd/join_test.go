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
	"testing/synctest"
	"time"

	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"github.com/google/uuid"
)

func TestJoinRetriesPreconditionFailedUsingETag(t *testing.T) {
	for _, withETag := range []bool{true, false} {
		t.Run(fmt.Sprintf("ETag=%t", withETag), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				connectionID := uuid.New()
				handleID := uuid.New()
				var (
					requests     int
					ifMatches    []string
					responseBody *joinResponseBody
					requestBody  string
				)
				httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					if responseBody != nil && !responseBody.closed {
						t.Fatal("retried before closing the previous response body")
					}
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatal(err)
					}
					if requests == 0 {
						requestBody = string(body)
					} else if string(body) != requestBody {
						t.Fatal("retry changed the session write body")
					}
					requests++
					ifMatches = append(ifMatches, req.Header.Get("If-Match"))
					header := http.Header{}
					if withETag {
						header.Set("ETag", fmt.Sprintf(`"etag-%d"`, requests))
					}
					responseBody = &joinResponseBody{Reader: strings.NewReader(`{"members":{}}`)}
					return &http.Response{
						StatusCode: http.StatusPreconditionFailed,
						Status:     "412 Precondition Failed",
						Header:     header,
						Body:       responseBody,
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
				if !withETag {
					wantIfMatches = []string{"*", "*", "*"}
				}
				if !slices.Equal(ifMatches, wantIfMatches) {
					t.Fatalf("If-Match values = %v, want %v", ifMatches, wantIfMatches)
				}
				if !responseBody.closed {
					t.Fatal("last response body was not closed")
				}
			})
		})
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
	synctest.Test(t, func(t *testing.T) {
		connectionID := uuid.New()
		ctx, cancel := context.WithCancelCause(context.Background())
		defer cancel(nil)
		var requests int
		httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			time.AfterFunc(time.Millisecond, func() { cancel(errors.New("caller stopped waiting")) })
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
		if err != context.Canceled {
			t.Fatalf("Join error = %v, want context cancellation", err)
		}
		if requests != 1 {
			t.Fatalf("requests = %d, want 1", requests)
		}
	})
}

// TestJoinDoesNotRetryOtherFailures protects writes whose outcome may already
// have reached MPSD, including successful writes with an unreadable response.
func TestJoinDoesNotRetryOtherFailures(t *testing.T) {
	transportErr := errors.New("connection lost after sending the request")
	for _, tt := range []struct {
		name     string
		status   int
		location string
		err      error
	}{
		{name: "transport", err: transportErr},
		{name: "throttled", status: http.StatusTooManyRequests},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "missing location", status: http.StatusOK},
		{name: "invalid location", status: http.StatusOK, location: "not-a-session"},
		{name: "invalid response body", status: http.StatusOK, location: SessionReference{
			ServiceConfigID: uuid.New(), TemplateName: "template", Name: "session",
		}.URL().String()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests int
			body := &joinResponseBody{Reader: strings.NewReader("invalid JSON")}
			client := newJoinTestClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if tt.err != nil {
					return nil, tt.err
				}
				return &http.Response{
					StatusCode: tt.status,
					Status:     fmt.Sprintf("%d %s", tt.status, http.StatusText(tt.status)),
					Header:     http.Header{"Content-Location": []string{tt.location}},
					Body:       body,
					Request:    req,
				}, nil
			})}, uuid.New())
			_, err := client.Join(context.Background(), uuid.New(), JoinConfig{})
			if err == nil {
				t.Fatal("Join succeeded, want error")
			}
			if tt.err != nil && !errors.Is(err, tt.err) {
				t.Fatalf("Join error = %v, want %v", err, tt.err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
			if tt.err == nil && !body.closed {
				t.Fatal("response body was not closed")
			}
		})
	}
}

// joinResponseBody records whether a response was closed before another attempt.
type joinResponseBody struct {
	io.Reader
	closed bool
}

// Close records that the caller released the response body.
func (b *joinResponseBody) Close() error {
	b.closed = true
	return nil
}

func newJoinTestClient(httpClient *http.Client, connectionID uuid.UUID) *Client {
	client := New(httpClient, rta.NewProvider(
		subscriberFunc(func(context.Context, *rta.Subscription) error { return nil }),
		nil,
	), xsts.UserInfo{XUID: "1"}, nil)
	client.subscriptionData.Store(&subscriptionData{ConnectionID: connectionID})
	return client
}
