package nsal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/df-mc/go-xsapi/v2/xal"
)

func TestResolverPrefersEarlierTitleData(t *testing.T) {
	index := 0
	resolver := ResolverConfig{
		TitleIDs: []string{},
		Titles: []*TitleData{
			{
				Endpoints: []Endpoint{{
					Protocol:             "https",
					Host:                 "*.playfabapi.com",
					HostType:             HostTypeWildcard,
					RelyingParty:         "current",
					SignaturePolicyIndex: &index,
				}},
				SignaturePolicies: []SignaturePolicy{{Version: 2}},
			},
			{
				Endpoints: []Endpoint{{
					Protocol:             "https",
					Host:                 "*.playfabapi.com",
					HostType:             HostTypeWildcard,
					RelyingParty:         "default",
					SignaturePolicyIndex: &index,
				}},
				SignaturePolicies: []SignaturePolicy{{Version: 1}},
			},
		},
	}.New(&transportTokenSource{})

	endpoint, policy, err := resolver.Resolve(context.Background(), mustParseURL(t, "https://20ca2.playfabapi.com/Client/LoginWithXbox"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if endpoint.RelyingParty != "current" {
		t.Fatalf("RelyingParty = %q, want current", endpoint.RelyingParty)
	}
	if policy.Version != 2 {
		t.Fatalf("SignaturePolicy.Version = %d, want 2", policy.Version)
	}
}

func TestResolverFallsBackToLaterTitleData(t *testing.T) {
	resolver := ResolverConfig{
		TitleIDs: []string{},
		Titles: []*TitleData{
			{
				Endpoints: []Endpoint{{
					Protocol:     "https",
					Host:         "title.mgt.xboxlive.com",
					HostType:     HostTypeFQDN,
					RelyingParty: "current",
				}},
			},
			{
				Endpoints: []Endpoint{{
					Protocol:     "https",
					Host:         "*.xboxlive.com",
					HostType:     HostTypeWildcard,
					RelyingParty: "default",
				}},
			},
		},
	}.New(&transportTokenSource{})

	endpoint, _, err := resolver.Resolve(context.Background(), mustParseURL(t, "https://peoplehub.xboxlive.com/users/me/people"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if endpoint.RelyingParty != "default" {
		t.Fatalf("RelyingParty = %q, want default", endpoint.RelyingParty)
	}
}

func TestResolverConfigNewDefaultsTitleIDs(t *testing.T) {
	resolver := NewResolver(&transportTokenSource{})
	if got, want := resolver.conf.TitleIDs, []string{"current", "default"}; !slices.Equal(got, want) {
		t.Fatalf("TitleIDs = %v, want %v", got, want)
	}
}

func TestResolverConfigNewPreservesEmptyTitleIDs(t *testing.T) {
	resolver := ResolverConfig{TitleIDs: []string{}}.New(&transportTokenSource{})
	if got := resolver.conf.TitleIDs; len(got) != 0 {
		t.Fatalf("TitleIDs = %v, want empty", got)
	}
}

func TestResolverTokenAndSignatureUsesResolvedRelyingParty(t *testing.T) {
	src := &transportTokenSource{token: authorizationToken("XBL3.0 x=uhs;token")}
	resolver := testResolver(src)

	token, _, err := resolver.TokenAndSignature(context.Background(), mustParseURL(t, "https://multiplayer.minecraft.net/authentication"))
	if err != nil {
		t.Fatalf("TokenAndSignature: %v", err)
	}
	if token != src.token {
		t.Fatalf("token = %v, want source token", token)
	}
	if got := src.relyingParty; got != "https://multiplayer.minecraft.net/" {
		t.Fatalf("relying party = %q, want https://multiplayer.minecraft.net/", got)
	}
}

func TestResolverDefaultTitleLoadBypassesNSALTransport(t *testing.T) {
	resetDefaultTitle(t)

	src := &transportTokenSource{token: authorizationToken("unexpected")}
	resolver := ResolverConfig{TitleIDs: []string{"default"}}.New(src)
	client := &http.Client{Transport: &Transport{
		Base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.String(); got != "https://title.mgt.xboxlive.com/titles/default/endpoints?type=1" {
				t.Fatalf("request URL = %q, want default title endpoint", got)
			}
			if got := req.Header.Get("Authorization"); got != "" {
				t.Fatalf("Authorization = %q, want empty", got)
			}
			if got := req.Header.Get("Signature"); got != "" {
				t.Fatalf("Signature = %q, want empty", got)
			}
			return defaultTitleResponse(), nil
		}),
		Resolver: resolver,
	}}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), xal.HTTPClient, client), time.Second)
	defer cancel()

	endpoint, _, err := resolver.Resolve(ctx, mustParseURL(t, "https://sessiondirectory.xboxlive.com/handles"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if endpoint.RelyingParty != authorizationRelyingParty {
		t.Fatalf("RelyingParty = %q, want %q", endpoint.RelyingParty, authorizationRelyingParty)
	}
	if src.called {
		t.Fatal("token source was called while loading default title data")
	}
}

func TestResolverFallsBackAfterTitleLoadError(t *testing.T) {
	resetDefaultTitle(t)

	currentErr := errors.New("current title unavailable")
	src := &transportTokenSource{err: currentErr}
	resolver := ResolverConfig{TitleIDs: []string{"current", "default"}}.New(src)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return defaultTitleResponse(), nil
	})}
	ctx := context.WithValue(context.Background(), xal.HTTPClient, client)

	endpoint, _, err := resolver.Resolve(ctx, mustParseURL(t, "https://sessiondirectory.xboxlive.com/handles"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if endpoint.RelyingParty != authorizationRelyingParty {
		t.Fatalf("RelyingParty = %q, want %q", endpoint.RelyingParty, authorizationRelyingParty)
	}
	if !src.called {
		t.Fatal("token source was not called for current title data")
	}
}

func TestResolverLoadsHigherPrecedenceTitleBeforeCachedDefault(t *testing.T) {
	src := &transportTokenSource{
		token:    authorizationToken("XBL3.0 x=uhs;token"),
		proofKey: mustGenerateKey(t),
	}
	resolver := ResolverConfig{TitleIDs: []string{"current", "default"}}.New(src)
	resolver.cached["default"] = titleData("*.playfabapi.com", "default")
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != "https://title.mgt.xboxlive.com/titles/current/endpoints" {
			t.Fatalf("request URL = %q, want current title endpoint", got)
		}
		return titleDataResponse("*.playfabapi.com", "current"), nil
	})}
	ctx := context.WithValue(context.Background(), xal.HTTPClient, client)

	endpoint, _, err := resolver.Resolve(ctx, mustParseURL(t, "https://20ca2.playfabapi.com/Client/LoginWithXbox"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if endpoint.RelyingParty != "current" {
		t.Fatalf("RelyingParty = %q, want current", endpoint.RelyingParty)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	return u
}

func resetDefaultTitle(t *testing.T) {
	t.Helper()
	defaultTitleMu.Lock()
	previous := defaultTitle
	defaultTitle = nil
	defaultTitleMu.Unlock()
	t.Cleanup(func() {
		defaultTitleMu.Lock()
		defaultTitle = previous
		defaultTitleMu.Unlock()
	})
}

func defaultTitleResponse() *http.Response {
	return titleDataResponse("*.xboxlive.com", authorizationRelyingParty)
}

func titleDataResponse(host, relyingParty string) *http.Response {
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

func titleData(host, relyingParty string) *TitleData {
	return &TitleData{Endpoints: []Endpoint{{
		Protocol:     "https",
		Host:         host,
		HostType:     HostTypeWildcard,
		RelyingParty: relyingParty,
		TokenType:    "JWT",
	}}}
}
