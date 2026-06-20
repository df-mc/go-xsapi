package sisu

import (
	"context"
	"crypto/ecdsa"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal"
	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"golang.org/x/oauth2"
)

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
