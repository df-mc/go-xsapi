package sisu

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"github.com/df-mc/go-xsapi/v2/xal/xast"
	"github.com/df-mc/go-xsapi/v2/xal/xasu"
	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"golang.org/x/oauth2"
)

func TestErrorCodeWireValues(t *testing.T) {
	t.Parallel()

	cases := []struct {
		wire string
		want ErrorCode
	}{
		{wire: "2148916227", want: ErrorCodeAccountSuspended},
		{wire: "2148916233", want: ErrorCodeAccountCreationRequired},
		{wire: "2148916236", want: ErrorCodeAgeVerificationRequired},
	}
	for _, tc := range cases {
		t.Run(tc.wire, func(t *testing.T) {
			t.Parallel()
			n, err := strconv.ParseUint(tc.wire, 10, 32)
			if err != nil {
				t.Fatalf("ParseUint(%q): %v", tc.wire, err)
			}
			if got := ErrorCode(n); got != tc.want {
				t.Fatalf("ErrorCode(%d) = %#x, want %#x", n, got, tc.want)
			}
		})
	}

	var code ErrorCode
	if !errors.As(ErrorCodeAccountSuspended, &code) {
		t.Fatal("errors.As failed for ErrorCodeAccountSuspended")
	}
	if code != ErrorCodeAccountSuspended {
		t.Fatalf("errors.As code = %#x, want %#x", code, ErrorCodeAccountSuspended)
	}
}

func TestAccountCreationRequiredErrorDoesNotExposeSignupURL(t *testing.T) {
	signupURL, err := url.Parse("https://sisu.xboxlive.com/create?sig=sensitive")
	if err != nil {
		t.Fatalf("parse signup URL: %v", err)
	}
	errText := (&AccountCreationRequiredError{SignupURL: signupURL}).Error()
	if strings.Contains(errText, "sensitive") || strings.Contains(errText, signupURL.String()) {
		t.Fatalf("AccountCreationRequiredError.Error() leaked signup URL: %q", errText)
	}
}

func TestAuthorizeRejectsNilDeviceToken(t *testing.T) {
	session := (Config{}).New(staticMSATokenSource{}, &SessionConfig{
		DeviceTokenSource: staticDeviceTokenSource{},
	})

	_, err := session.authorize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "device token is invalid") {
		t.Fatalf("authorize error = %v, want invalid device token", err)
	}
}

func TestAuthorizeRejectsNilProofKey(t *testing.T) {
	session := (Config{}).New(staticMSATokenSource{}, &SessionConfig{
		DeviceTokenSource: staticDeviceTokenSource{token: &xasd.Token{
			Token:    "device-token",
			NotAfter: time.Now().Add(time.Hour),
		}},
	})

	_, err := session.authorize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "proof key is absent") {
		t.Fatalf("authorize error = %v, want absent proof key", err)
	}
}

func TestSessionDefaultHTTPClientHasTimeout(t *testing.T) {
	session := (Config{}).New(staticMSATokenSource{}, nil)
	if session.client != xal.ContextClient(context.Background()) {
		t.Fatalf("session client = %p, want XAL default client %p", session.client, xal.ContextClient(context.Background()))
	}
}

func TestXSTSTokenDoesNotHoldCacheLockDuringTokenRequest(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate proof key: %v", err)
	}
	validUntil := time.Now().Add(time.Hour)
	var session *Session
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://xsts.auth.xboxlive.com/xsts/authorize" {
			return nil, errors.New("unexpected request URL: " + req.URL.String())
		}
		if _, err := session.XSTSToken(req.Context(), defaultRelyingParty); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{
				"IssueInstant":"2026-01-01T00:00:00Z",
				"NotAfter":"` + validUntil.Format(time.RFC3339) + `",
				"Token":"playfab-xsts",
				"DisplayClaims":{"xui":[{"uhs":"user"}]}
			}`)),
			Request: req,
		}, nil
	})}
	session = (Config{}).New(staticMSATokenSource{}, &SessionConfig{
		DeviceTokenSource: staticDeviceTokenSource{
			token: &xasd.Token{
				Token:    "device-token",
				NotAfter: validUntil,
			},
			proofKey: key,
		},
		HTTPClient: client,
	})
	session.title = &xast.Token{
		Token:    "title-token",
		NotAfter: validUntil,
	}
	session.user = &xasu.Token{
		Token:    "user-token",
		NotAfter: validUntil,
	}
	session.xsts[defaultRelyingParty] = &xsts.Token{
		Token:    "default-xsts",
		NotAfter: validUntil,
		DisplayClaims: xsts.DisplayClaims{UserInfo: []xsts.UserInfo{{
			UserInfo: xasu.UserInfo{UserHash: "user"},
		}}},
	}

	ctx := context.WithValue(context.Background(), xal.HTTPClient, client)
	done := make(chan error, 1)
	go func() {
		_, err := session.XSTSToken(ctx, "https://b980a380.minecraft.playfabapi.com/")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("XSTSToken: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("XSTSToken deadlocked while requesting a non-default relying party")
	}
}

type staticMSATokenSource struct{}

func (staticMSATokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "msa-token"}, nil
}

type staticDeviceTokenSource struct {
	token    *xasd.Token
	proofKey *ecdsa.PrivateKey
}

func (s staticDeviceTokenSource) DeviceToken(context.Context) (*xasd.Token, error) {
	return s.token, nil
}

func (s staticDeviceTokenSource) ProofKey() *ecdsa.PrivateKey {
	return s.proofKey
}
