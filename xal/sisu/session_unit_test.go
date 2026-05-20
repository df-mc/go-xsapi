package sisu

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/df-mc/go-xsapi/v2/xal/xasd"
	"golang.org/x/oauth2"
)

func TestErrorCodeSignInCountByDeviceTypeExceededStringSpellsDevice(t *testing.T) {
	got := ErrorCodeSignInCountByDeviceTypeExceeded.String()
	if strings.Contains(got, "devie") {
		t.Fatalf("ErrorCodeSignInCountByDeviceTypeExceeded.String() = %q, contains typo", got)
	}
	if !strings.Contains(got, "device") {
		t.Fatalf("ErrorCodeSignInCountByDeviceTypeExceeded.String() = %q, want device message", got)
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
	if !errors.Is(err, errDeviceTokenAbsent) {
		t.Fatalf("authorize error = %v, want %v", err, errDeviceTokenAbsent)
	}
}

func TestAuthorizeRejectsNilProofKey(t *testing.T) {
	session := (Config{}).New(staticMSATokenSource{}, &SessionConfig{
		DeviceTokenSource: staticDeviceTokenSource{token: &xasd.Token{Token: "device-token"}},
	})

	_, err := session.authorize(context.Background())
	if !errors.Is(err, errProofKeyAbsent) {
		t.Fatalf("authorize error = %v, want %v", err, errProofKeyAbsent)
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
