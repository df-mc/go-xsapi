package xsapi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/mpsd"
	"github.com/df-mc/go-xsapi/v2/presence"
	"github.com/df-mc/go-xsapi/v2/rta"
	"github.com/df-mc/go-xsapi/v2/social"
	"github.com/df-mc/go-xsapi/v2/xal/nsal"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"github.com/df-mc/go-xsapi/v2/xal/xasu"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"github.com/google/uuid"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClientConfigNewKeepsDefaultEagerRTA(t *testing.T) {
	dialErr := errors.New("dial failed")
	_, dialCalls := stubClientDependencies(t, dialErr)

	var config ClientConfig
	_, err := config.New(context.Background(), validTokenSource{})
	if !errors.Is(err, dialErr) {
		t.Fatalf("New error = %v, want %v", err, dialErr)
	}
	if got := dialCalls.Load(); got != 1 {
		t.Fatalf("RTA dial calls = %d, want 1", got)
	}
}

func TestClientConfigNewLazyRTADoesNotDial(t *testing.T) {
	_, dialCalls := stubClientDependencies(t, errors.New("dial should be deferred"))

	client, err := (ClientConfig{RTAMode: RTALazy}).New(context.Background(), validTokenSource{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if got := dialCalls.Load(); got != 0 {
		t.Fatalf("RTA dial calls = %d, want 0", got)
	}
	if client.RTA() != nil {
		t.Fatal("lazy client has an RTA connection before demand")
	}
}

func TestClientConfigNewDisabledRTADoesNotDial(t *testing.T) {
	_, dialCalls := stubClientDependencies(t, errors.New("dial should be disabled"))

	client, err := (ClientConfig{RTAMode: RTADisabled}).New(context.Background(), validTokenSource{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if got := dialCalls.Load(); got != 0 {
		t.Fatalf("RTA dial calls = %d, want 0", got)
	}
	if client.RTA() != nil {
		t.Fatal("disabled client has an RTA connection")
	}
}

func TestLazyRTAOperationDialsOnDemand(t *testing.T) {
	dialErr := errors.New("dial failed")
	_, dialCalls := stubClientDependencies(t, dialErr)
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected request")
	})}
	client, err := (ClientConfig{HTTPClient: httpClient, RTAMode: RTALazy}).New(context.Background(), validTokenSource{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = client.MPSD().Join(context.Background(), uuid.New(), mpsd.JoinConfig{})
	if !errors.Is(err, dialErr) {
		t.Fatalf("Join error = %v, want %v", err, dialErr)
	}
	if got := dialCalls.Load(); got != 1 {
		t.Fatalf("RTA dial calls = %d, want 1", got)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0", got)
	}
}

func TestDisabledRTAOperationFailsWithoutDialing(t *testing.T) {
	_, dialCalls := stubClientDependencies(t, errors.New("dial should be disabled"))
	var requests atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected request")
	})}
	client, err := (ClientConfig{HTTPClient: httpClient, RTAMode: RTADisabled}).New(context.Background(), validTokenSource{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	_, err = client.MPSD().Join(context.Background(), uuid.New(), mpsd.JoinConfig{})
	if !errors.Is(err, rta.ErrUnavailable) {
		t.Fatalf("Join error = %v, want %v", err, rta.ErrUnavailable)
	}
	if got := dialCalls.Load(); got != 0 {
		t.Fatalf("RTA dial calls = %d, want 0", got)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("HTTP requests = %d, want 0", got)
	}
}

func TestClientConfigNewRejectsInvalidRTAMode(t *testing.T) {
	_, err := (ClientConfig{RTAMode: RTAMode(999)}).New(context.Background(), stubTokenSource{})
	if err == nil {
		t.Fatal("New returned nil error, want invalid RTA mode error")
	}
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

func TestClientRoundTripUsesConfiguredHTTPClientForLazyNSAL(t *testing.T) {
	originalDefaultTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("http.DefaultClient should not be used for lazy NSAL resolution")
		return nil, nil
	})
	t.Cleanup(func() {
		http.DefaultTransport = originalDefaultTransport
	})

	key := mustGenerateECDSAKey(t)
	src := &recordingTokenSource{token: testXSTSToken(time.Now().Add(time.Hour)), proofKey: key}
	var titleRequested, finalRequested bool
	configuredClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://title.mgt.xboxlive.com/titles/current/endpoints":
			titleRequested = true
			if got := req.Header.Get("Authorization"); got == "" {
				t.Fatal("current title request missing Authorization header")
			}
			if got := req.Header.Get("Signature"); got == "" {
				t.Fatal("current title request missing Signature header")
			}
			return nsalTitleDataResponse("*.playfabapi.com", "https://playfabapi.com"), nil
		case "https://20ca2.playfabapi.com/Client/LoginWithXbox":
			finalRequested = true
			if got := req.Header.Get("Authorization"); got == "" {
				t.Fatal("final request missing Authorization header")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		default:
			t.Fatalf("unexpected request URL: %s", req.URL)
			return nil, nil
		}
	})}
	client := &Client{
		config: ClientConfig{HTTPClient: configuredClient},
		src:    src,
	}
	client.client = new(http.Client)
	*client.client = *configuredClient
	client.client.Transport = client
	client.transport = &nsal.Transport{
		Base:     client.baseTransport(),
		Resolver: nsal.NewResolver(src),
	}

	req, err := http.NewRequest(http.MethodPost, "https://20ca2.playfabapi.com/Client/LoginWithXbox", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if !titleRequested {
		t.Fatal("current title endpoint was not requested")
	}
	if !finalRequested {
		t.Fatal("final request was not sent")
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
	relyingParty string
	proofKey     *ecdsa.PrivateKey
}

func (src *recordingTokenSource) XSTSToken(_ context.Context, relyingParty string) (*xsts.Token, error) {
	src.relyingParty = relyingParty
	return src.token, nil
}

func (*recordingTokenSource) DeviceToken(context.Context) (*xasd.Token, error) {
	return nil, errors.New("unexpected device token request")
}

func (src *recordingTokenSource) ProofKey() *ecdsa.PrivateKey {
	return src.proofKey
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

func mustGenerateECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func nsalTitleDataResponse(host, relyingParty string) *http.Response {
	body := fmt.Sprintf(`{
		"EndPoints": [{
			"Protocol": "https",
			"Host": %q,
			"HostType": "wildcard",
			"RelyingParty": %q,
			"TokenType": "JWT"
		}]
	}`, host, relyingParty)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type validTokenSource struct{}

func (validTokenSource) XSTSToken(context.Context, string) (*xsts.Token, error) {
	return &xsts.Token{
		Token: "token",
		DisplayClaims: xsts.DisplayClaims{UserInfo: []xsts.UserInfo{{
			UserInfo: xasu.UserInfo{UserHash: "uhs"},
			XUID:     "2533274799999999",
		}}},
	}, nil
}

func (validTokenSource) DeviceToken(context.Context) (*xasd.Token, error) {
	return nil, errors.New("unexpected device token request")
}

func (validTokenSource) ProofKey() *ecdsa.PrivateKey {
	return nil
}

func stubClientDependencies(t *testing.T, dialErr error) (*atomic.Int32, *atomic.Int32) {
	t.Helper()
	oldDialRTA := dialRTA
	var resolverCalls atomic.Int32
	var dialCalls atomic.Int32
	dialRTA = func(context.Context, *http.Client, *slog.Logger) (*rta.Conn, error) {
		dialCalls.Add(1)
		return nil, dialErr
	}
	t.Cleanup(func() {
		dialRTA = oldDialRTA
	})
	return &resolverCalls, &dialCalls
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
