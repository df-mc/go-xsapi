package mpsd

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestSessionUpdateReturnsDeletedOnNoContent(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}
	oldState := SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
	}

	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodPut {
			t.Fatalf("request method = %s, want PUT", req.Method)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     http.StatusText(http.StatusNoContent),
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	session := &Session{
		client: &Client{client: httpClient},
		ref:    ref,
		etag:   `"old-etag"`,
		cache:  oldState,
		closed: make(chan struct{}),
	}

	deleted, err := session.update(context.Background(), SessionDescription{
		Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"patched"}`)},
	}, nil)
	if err != nil {
		t.Fatalf("update returned error: %v", err)
	}
	if !deleted {
		t.Fatal("update did not report deleted on 204")
	}

	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if got := session.etag; got != `"old-etag"` {
		t.Fatalf("etag = %q, want unchanged", got)
	}
	if got := string(session.cache.Properties.Custom); got != `{"property":"old"}` {
		t.Fatalf("cache mutated during update: got %s", got)
	}
}

func TestSessionSetCustomPropertiesMarksDeletedOnNoContent(t *testing.T) {
	ref := SessionReference{
		ServiceConfigID: uuid.New(),
		TemplateName:    "template",
		Name:            "SESSION",
	}

	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Status:     http.StatusText(http.StatusNoContent),
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}

	session := &Session{
		client: &Client{client: httpClient},
		ref:    ref,
		etag:   `"old-etag"`,
		cache: SessionDescription{
			Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
		},
		closed: make(chan struct{}),
	}

	if err := session.SetCustomProperties(context.Background(), json.RawMessage(`{"property":"patched"}`)); err != nil {
		t.Fatalf("SetCustomProperties returned error: %v", err)
	}
	if session.cache.Properties != nil {
		t.Fatalf("cache properties not cleared: %+v", session.cache.Properties)
	}
	if session.cache.Constants != nil || session.cache.Members != nil || session.cache.RoleTypes != nil {
		t.Fatalf("cache not cleared after delete: %+v", session.cache)
	}
	if got := session.etag; got != "" {
		t.Fatalf("etag = %q, want empty", got)
	}
	if err := session.Context().Err(); err != context.Canceled {
		t.Fatalf("session context err = %v, want %v", err, context.Canceled)
	}
	if err := session.Sync(context.Background()); err != net.ErrClosed {
		t.Fatalf("Sync error = %v, want %v", err, net.ErrClosed)
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
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body: io.NopCloser(bytes.NewReader([]byte(`{
				"properties": {
					"custom": {"property":"patched"}
				}
			}`))),
			Header:  header,
			Request: req,
		}, nil
	})}

	client := &Client{
		client:   httpClient,
		sessions: map[string]*Session{},
	}
	session := &Session{
		client: client,
		ref:    ref,
		etag:   `"old-etag"`,
		cache: SessionDescription{
			Properties: &SessionProperties{Custom: json.RawMessage(`{"property":"old"}`)},
		},
		closed: make(chan struct{}),
	}
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
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     http.StatusText(http.StatusOK),
			Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			Header:     header,
			Request:    req,
		}, nil
	})}

	session := &Session{
		client: &Client{
			client:   httpClient,
			sessions: map[string]*Session{},
		},
		ref:    ref,
		closed: make(chan struct{}),
	}
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
