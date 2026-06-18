package xsapi

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/mpsd"
	"github.com/df-mc/go-xsapi/v2/presence"
	"github.com/df-mc/go-xsapi/v2/social"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientCloseContextRetriesAfterSubclientFailure(t *testing.T) {
	userInfo := xsts.UserInfo{XUID: "2533274799999999"}
	presenceErr := errors.New("presence remove failed")
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.Method != http.MethodDelete {
			t.Fatalf("request method = %s, want DELETE", req.Method)
		}
		return nil, presenceErr
	})}

	client := &Client{
		mpsd:     mpsd.New(httpClient, nil, userInfo, nil),
		social:   social.New(httpClient, nil, userInfo, nil),
		presence: presence.New(httpClient, userInfo),
	}

	if err := client.CloseContext(context.Background()); !errors.Is(err, presenceErr) {
		t.Fatalf("first close error = %v, want %v", err, presenceErr)
	}
	if client.closed.Load() {
		t.Fatal("client was marked closed after failed cleanup")
	}

	if err := client.CloseContext(context.Background()); !errors.Is(err, presenceErr) {
		t.Fatalf("second close error = %v, want %v", err, presenceErr)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("presence close attempts = %d, want 2", got)
	}
}

func TestClientRoundTripFailsAfterClose(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatal("base transport should not be reached after close")
		return nil, nil
	})}
	client := &Client{
		config: ClientConfig{HTTPClient: httpClient},
		src:    stubTokenSource{},
	}
	client.closed.Store(true)

	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = client.RoundTrip(req)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("RoundTrip error = %v, want %v", err, net.ErrClosed)
	}
}

func TestClientRoundTripClosesRequestBodyAfterClose(t *testing.T) {
	client := &Client{}
	client.closed.Store(true)

	body := &closingBody{ReadCloser: io.NopCloser(&zeroReader{})}
	req, err := http.NewRequest(http.MethodPost, "https://example.com", body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = client.RoundTrip(req)
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("RoundTrip error = %v, want %v", err, net.ErrClosed)
	}
	if !body.closed.Load() {
		t.Fatal("request body was not closed")
	}
}

func TestNSALTokenSourceReusesValidAuthorizationToken(t *testing.T) {
	cachedToken := testXSTSToken(time.Now().Add(time.Hour))
	src := nsalTokenSource{
		TokenSource:        stubTokenSource{},
		authorizationToken: cachedToken,
	}
	token, err := src.Token(context.Background(), "http://xboxlive.com")
	if err != nil {
		t.Fatalf("Token returned error: %v", err)
	}
	if token != cachedToken {
		t.Fatal("Token did not reuse valid authorization token")
	}
}

func TestNSALTokenSourceRefreshesExpiredAuthorizationToken(t *testing.T) {
	freshToken := testXSTSToken(time.Now().Add(time.Hour))
	tokenSource := &recordingTokenSource{token: freshToken}
	src := nsalTokenSource{
		TokenSource:        tokenSource,
		authorizationToken: testXSTSToken(time.Now().Add(-time.Hour)),
	}
	token, err := src.Token(context.Background(), "http://xboxlive.com")
	if err != nil {
		t.Fatalf("Token returned error: %v", err)
	}
	if token != freshToken {
		t.Fatal("Token did not refresh expired authorization token")
	}
	if got := tokenSource.relyingParty.Load(); got != "http://xboxlive.com" {
		t.Fatalf("XSTSToken relying party = %q, want %q", got, "http://xboxlive.com")
	}
}

type stubTokenSource struct{}

func (stubTokenSource) XSTSToken(context.Context, string) (*xsts.Token, error) {
	return nil, errors.New("unexpected XSTS request")
}

func (stubTokenSource) DeviceToken(context.Context) (*xasd.Token, error) {
	return nil, errors.New("unexpected device token request")
}

func (stubTokenSource) ProofKey() *ecdsa.PrivateKey {
	return nil
}

type recordingTokenSource struct {
	token        *xsts.Token
	relyingParty atomic.Value
}

func (src *recordingTokenSource) XSTSToken(_ context.Context, relyingParty string) (*xsts.Token, error) {
	src.relyingParty.Store(relyingParty)
	return src.token, nil
}

func (*recordingTokenSource) DeviceToken(context.Context) (*xasd.Token, error) {
	return nil, errors.New("unexpected device token request")
}

func (*recordingTokenSource) ProofKey() *ecdsa.PrivateKey {
	return nil
}

func testXSTSToken(notAfter time.Time) *xsts.Token {
	return &xsts.Token{
		Token:    "token",
		NotAfter: notAfter,
		DisplayClaims: xsts.DisplayClaims{
			UserInfo: []xsts.UserInfo{{}},
		},
	}
}

type zeroReader struct{}

func (*zeroReader) Read(p []byte) (int, error) {
	return 0, io.EOF
}

type closingBody struct {
	io.ReadCloser
	closed atomic.Bool
}

func (b *closingBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}
