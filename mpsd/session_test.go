package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testResponse(req *http.Request, statusCode int, header http.Header, body []byte) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     header,
		Request:    req,
	}
}

func testSession(ref SessionReference, client *Client, cache SessionDescription) *Session {
	return &Session{
		client: client,
		ref:    ref,
		cache:  cache,
		closed: make(chan struct{}),
	}
}

func assertDeletedSession(t testing.TB, client *Client, session *Session) {
	t.Helper()

	if got := session.etag; got != "" {
		t.Fatalf("etag = %q, want empty", got)
	}
	if err := session.Context().Err(); err != context.Canceled {
		t.Fatalf("session context err = %v, want %v", err, context.Canceled)
	}
	if client != nil {
		if _, ok := client.sessions[session.ref.URL().String()]; ok {
			t.Fatal("session still registered after delete")
		}
	}
}

func TestSessionConstantsReturnsDetachedCopy(t *testing.T) {
	session := testSessionWithCache()

	constants := session.Constants()
	constants.System.Initiators[0] = "mutated-initiator"
	constants.Custom[0] = 'X'

	if got := session.cache.Constants.System.Initiators[0]; got != "original-initiator" {
		t.Fatalf("cache initiators mutated: got %q", got)
	}
	if got := string(session.cache.Constants.Custom); got != `{"constant":"original"}` {
		t.Fatalf("cache constants custom mutated: got %s", got)
	}
}

func TestSessionPropertiesReturnsDetachedCopy(t *testing.T) {
	session := testSessionWithCache()

	properties := session.Properties()
	properties.System.Keywords[0] = "mutated-keyword"
	properties.Custom[0] = 'X'

	if got := session.cache.Properties.System.Keywords[0]; got != "original-keyword" {
		t.Fatalf("cache keywords mutated: got %q", got)
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"original"}` {
		t.Fatalf("cache properties custom mutated: got %s", got)
	}
}

func TestSessionMemberReturnsDetachedCopy(t *testing.T) {
	session := testSessionWithCache()

	member, ok := session.Member("me")
	if !ok {
		t.Fatal("expected member snapshot")
	}
	member.Properties.System.Subscription.ChangeTypes[0] = ChangeTypeHost
	member.Properties.System.SecureDeviceAddress[0] = 9

	gotMember := session.cache.Members["me"]
	if got := gotMember.Properties.System.Subscription.ChangeTypes[0]; got != ChangeTypeEverything {
		t.Fatalf("cache change types mutated: got %q", got)
	}
	if got := gotMember.Properties.System.SecureDeviceAddress[0]; got != 1 {
		t.Fatalf("cache secure device address mutated: got %d", got)
	}
}

func TestSessionMembersReturnsDetachedCopies(t *testing.T) {
	session := testSessionWithCache()

	for _, listed := range session.Members() {
		listed.Constants.System.XUID = "mutated-from-iterator"
		listed.Properties.Custom[0] = 'Y'
	}

	gotMember := session.cache.Members["me"]
	if got := gotMember.Constants.System.XUID; got != "original-xuid" {
		t.Fatalf("cache member xuid mutated: got %q", got)
	}
	if got := string(gotMember.Properties.Custom); got != `{"memberProperty":"original"}` {
		t.Fatalf("cache member properties custom mutated: got %s", got)
	}
}

func TestSessionRefreshConnectionWhileReturnsNetErrClosedWhenShouldContinueStops(t *testing.T) {
	session := testSession(SessionReference{}, nil, SessionDescription{})
	err := session.refreshConnectionWhile(context.Background(), uuid.New(), func() bool { return false })
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("refreshConnectionWhile error = %v, want %v", err, net.ErrClosed)
	}
}

func testSessionWithCache() *Session {
	return &Session{
		cache: SessionDescription{
			Constants: &SessionConstants{
				System: &SessionConstantsSystem{
					Visibility:   "open",
					Initiators:   []string{"original-initiator"},
					Capabilities: json.RawMessage(`{"capability":true}`),
				},
				Custom: json.RawMessage(`{"constant":"original"}`),
			},
			Properties: &SessionProperties{
				System: &SessionPropertiesSystem{
					Keywords:                         []string{"original-keyword"},
					Turn:                             []uint32{1},
					Matchmaking:                      json.RawMessage(`{"mode":"original"}`),
					ServerConnectionStringCandidates: json.RawMessage(`["original"]`),
				},
				Custom: json.RawMessage(`{"property":"original"}`),
			},
			Members: map[string]*MemberDescription{
				"me": {
					Constants: &MemberConstants{
						System: &MemberConstantsSystem{
							XUID:       "original-xuid",
							Initialize: true,
						},
						Custom: json.RawMessage(`{"memberConstant":"original"}`),
					},
					Properties: &MemberProperties{
						System: &MemberPropertiesSystem{
							Active:              true,
							Connection:          uuid.New(),
							Subscription:        &MemberPropertiesSystemSubscription{ID: "SUB", ChangeTypes: []string{ChangeTypeEverything}},
							SecureDeviceAddress: []byte{1, 2, 3},
						},
						Custom: json.RawMessage(`{"memberProperty":"original"}`),
					},
				},
			},
		},
	}
}

func TestSessionSetCustomPropertiesMarksDeletedOnNoContent(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return testResponse(req, http.StatusNoContent, nil, nil), nil
	})}

	session := testSession(ref, &Client{client: httpClient}, SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	})
	session.etag = `"old-etag"`

	if err := session.SetCustomProperties(context.Background(), json.RawMessage(`{"property":"patched"}`)); err != nil {
		t.Fatalf("SetCustomProperties returned error: %v", err)
	}
	if session.cache.Properties != nil {
		t.Fatalf("cache properties not cleared: %+v", session.cache.Properties)
	}
	if session.cache.Constants != nil || session.cache.Members != nil || session.cache.RoleTypes != nil {
		t.Fatalf("cache not cleared after delete: %+v", session.cache)
	}
	assertDeletedSession(t, nil, session)
	if err := session.Sync(context.Background()); err != net.ErrClosed {
		t.Fatalf("Sync error = %v, want %v", err, net.ErrClosed)
	}
}

func TestSessionSetCustomPropertiesRetriesConflictWithLatestETag(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			if req.Method != http.MethodPut {
				t.Fatalf("request method = %s, want PUT", req.Method)
			}
			if got := req.Header.Get("If-Match"); got != `"etag-1"` {
				t.Fatalf("If-Match = %q, want %q", got, `"etag-1"`)
			}
			return testResponse(req, http.StatusPreconditionFailed, nil, nil), nil
		case 2:
			if req.Method != http.MethodGet {
				t.Fatalf("request method = %s, want GET", req.Method)
			}
			if got := req.Header.Get("If-None-Match"); got != `"etag-1"` {
				t.Fatalf("If-None-Match = %q, want %q", got, `"etag-1"`)
			}
			header := make(http.Header)
			header.Set("ETag", `"etag-2"`)
			return testResponse(req, http.StatusOK, header, []byte(`{
				"properties": {
					"custom": {"property":"server"}
				}
			}`)), nil
		case 3:
			if req.Method != http.MethodPut {
				t.Fatalf("request method = %s, want PUT", req.Method)
			}
			if got := req.Header.Get("If-Match"); got != `"etag-2"` {
				t.Fatalf("If-Match = %q, want %q", got, `"etag-2"`)
			}
			header := make(http.Header)
			header.Set("ETag", `"etag-3"`)
			return testResponse(req, http.StatusOK, header, []byte(`{
				"properties": {
					"custom": {"property":"patched"}
				}
			}`)), nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, req.Method, req.URL)
			return nil, nil
		}
	})}

	session := testSession(ref, &Client{client: httpClient}, SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	})
	session.etag = `"etag-1"`

	if err := session.SetCustomProperties(context.Background(), json.RawMessage(`{"property":"patched"}`)); err != nil {
		t.Fatalf("SetCustomProperties returned error: %v", err)
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"patched"}` {
		t.Fatalf("cache custom = %s, want patched response", got)
	}
	if got := session.etag; got != `"etag-3"` {
		t.Fatalf("etag = %q, want %q", got, `"etag-3"`)
	}
	if requests != 3 {
		t.Fatalf("requests = %d, want 3", requests)
	}
}

func TestSessionSetCustomPropertiesTreatsConflictFollowedByDeleteAsDeleted(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	requests := 0
	client := &Client{
		sessions: map[string]*Session{},
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		switch requests {
		case 1:
			if req.Method != http.MethodPut {
				t.Fatalf("request method = %s, want PUT", req.Method)
			}
			return testResponse(req, http.StatusPreconditionFailed, nil, nil), nil
		case 2:
			if req.Method != http.MethodGet {
				t.Fatalf("request method = %s, want GET", req.Method)
			}
			return testResponse(req, http.StatusNoContent, nil, nil), nil
		default:
			t.Fatalf("unexpected request %d: %s %s", requests, req.Method, req.URL)
			return nil, nil
		}
	})}
	client.client = httpClient

	session := testSession(ref, client, SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	})
	session.etag = `"etag-1"`
	client.sessions[ref.URL().String()] = session

	if err := session.SetCustomProperties(context.Background(), json.RawMessage(`{"property":"patched"}`)); err != nil {
		t.Fatalf("SetCustomProperties returned error: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	assertDeletedSession(t, client, session)
}

func TestSessionSyncMarksDeletedOnNoContent(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("request method = %s, want GET", req.Method)
		}
		return testResponse(req, http.StatusNoContent, nil, nil), nil
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := testSession(ref, client, SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	})
	session.etag = `"old-etag"`
	client.sessions[ref.URL().String()] = session

	if err := session.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	if session.cache.Properties != nil || session.cache.Constants != nil || session.cache.Members != nil || session.cache.RoleTypes != nil {
		t.Fatalf("cache not cleared after deletion: %+v", session.cache)
	}
	assertDeletedSession(t, client, session)
}

func TestSessionSetCustomPropertiesTreatsBootstrapDeleteAsDeleted(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	requests := 0
	client := &Client{
		sessions: map[string]*Session{},
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodGet {
			t.Fatalf("request method = %s, want GET", req.Method)
		}
		return testResponse(req, http.StatusNoContent, nil, nil), nil
	})}
	client.client = httpClient

	session := testSession(ref, client, SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	})
	client.sessions[ref.URL().String()] = session

	if err := session.SetCustomProperties(context.Background(), json.RawMessage(`{"property":"patched"}`)); err != nil {
		t.Fatalf("SetCustomProperties returned error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	assertDeletedSession(t, client, session)
}

func TestSessionCloseContextReturnsErrorOnPreconditionFailed(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		if got := req.Header.Get("If-Match"); got != "*" {
			t.Fatalf("If-Match = %q, want *", got)
		}
		return testResponse(req, http.StatusPreconditionFailed, nil, nil), nil
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := testSession(ref, client, SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	})
	client.sessions[ref.URL().String()] = session

	err := session.CloseContext(context.Background())
	if err == nil {
		t.Fatal("CloseContext returned nil error, want precondition failure")
	}
	if err := session.Context().Err(); err != nil {
		t.Fatalf("session context err = %v, want nil", err)
	}
	if got, ok := client.sessions[ref.URL().String()]; !ok || got != session {
		t.Fatal("session was unregistered after failed close")
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"old"}` {
		t.Fatalf("cache custom = %s, want unchanged", got)
	}
}

func TestSessionSetMemberCustomPropertiesReturnsErrorOnPreconditionFailed(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		if got := req.Header.Get("If-Match"); got != "*" {
			t.Fatalf("If-Match = %q, want *", got)
		}
		return testResponse(req, http.StatusPreconditionFailed, nil, nil), nil
	})}

	session := testSession(ref, &Client{client: httpClient}, SessionDescription{
		Members: map[string]*MemberDescription{
			"me": {
				Properties: &MemberProperties{
					Custom: json.RawMessage(`{"memberProperty":"old"}`),
				},
			},
		},
	})

	err := session.SetMemberCustomProperties(context.Background(), "me", json.RawMessage(`{"memberProperty":"patched"}`))
	if err == nil {
		t.Fatal("SetMemberCustomProperties returned nil error, want precondition failure")
	}
	member := session.cache.Members["me"]
	if member == nil || member.Properties == nil {
		t.Fatalf("member cache missing after failed update: %+v", session.cache.Members["me"])
	}
	if got := string(member.Properties.Custom); got != `{"memberProperty":"old"}` {
		t.Fatalf("member custom = %s, want unchanged", got)
	}
	if err := session.Context().Err(); err != nil {
		t.Fatalf("session context err = %v, want nil", err)
	}
}

func TestSessionCloseContextClosesHandleWithoutClearingSyncedState(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		header := make(http.Header)
		header.Set("ETag", `"new-etag"`)
		return testResponse(req, http.StatusOK, header, []byte(`{
				"properties": {
					"custom": {"property":"patched"}
				}
			}`)), nil
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := testSession(ref, client, SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	})
	session.etag = `"old-etag"`
	client.sessions[ref.URL().String()] = session

	if err := session.CloseContext(context.Background()); err != nil {
		t.Fatalf("CloseContext returned error: %v", err)
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"patched"}` {
		t.Fatalf("cache custom = %s, want synced response", got)
	}
	if got := session.etag; got != `"new-etag"` {
		t.Fatalf("etag = %q, want %q", got, `"new-etag"`)
	}
	if _, ok := client.sessions[ref.URL().String()]; ok {
		t.Fatal("session still registered after close")
	}
	if err := session.Context().Err(); err != context.Canceled {
		t.Fatalf("session context err = %v, want %v", err, context.Canceled)
	}
	if err := session.Sync(context.Background()); err != net.ErrClosed {
		t.Fatalf("Sync error = %v, want %v", err, net.ErrClosed)
	}
}

func TestSessionCloseContextConcurrentCloseSendsSingleUpdate(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		if n := requests.Add(1); n == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		header := make(http.Header)
		header.Set("ETag", `"etag"`)
		return testResponse(req, http.StatusOK, header, []byte(`{}`)), nil
	})}

	session := testSession(ref, &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}, SessionDescription{})
	session.client.sessions[ref.URL().String()] = session

	// Hold closeMu so both goroutines contend on the same critical section once
	// released, instead of relying on scheduler timing.
	session.closeMu.Lock()
	errCh := make(chan error, 2)
	ready := make(chan struct{}, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			errCh <- session.CloseContext(context.Background())
		}()
	}
	<-ready
	<-ready
	session.closeMu.Unlock()
	<-firstStarted
	close(releaseFirst)

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("CloseContext returned error: %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	if err := session.Context().Err(); err != context.Canceled {
		t.Fatalf("session context err = %v, want %v", err, context.Canceled)
	}
}

func TestSessionCloseLockedDoesNotRemoveReplacementSession(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	client := &Client{
		sessions: map[string]*Session{},
	}
	oldSession := testSession(ref, client, SessionDescription{})
	client.sessions[ref.URL().String()] = oldSession

	oldSession.closeLocked()

	newSession := testSession(ref, client, SessionDescription{})
	client.sessions[ref.URL().String()] = newSession

	// A second close path for the old session must not unregister the replacement.
	oldSession.closeLocked()

	got, ok := client.sessions[ref.URL().String()]
	if !ok {
		t.Fatal("replacement session was removed")
	}
	if got != newSession {
		t.Fatalf("registered session = %p, want replacement %p", got, newSession)
	}
	if err := newSession.Context().Err(); err != nil {
		t.Fatalf("replacement session context err = %v, want nil", err)
	}
}
