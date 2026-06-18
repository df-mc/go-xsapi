package nsal

import (
	"context"
	"net/url"
	"slices"
	"testing"
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
	}.New(nil)

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
	}.New(nil)

	endpoint, _, err := resolver.Resolve(context.Background(), mustParseURL(t, "https://peoplehub.xboxlive.com/users/me/people"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if endpoint.RelyingParty != "default" {
		t.Fatalf("RelyingParty = %q, want default", endpoint.RelyingParty)
	}
}

func TestResolverSkipsNilTitleData(t *testing.T) {
	resolver := ResolverConfig{
		TitleIDs: []string{},
		Titles: []*TitleData{
			nil,
			{
				Endpoints: []Endpoint{{
					Protocol:     "https",
					Host:         "*.xboxlive.com",
					HostType:     HostTypeWildcard,
					RelyingParty: "default",
				}},
			},
		},
	}.New(nil)

	endpoint, _, err := resolver.Resolve(context.Background(), mustParseURL(t, "https://peoplehub.xboxlive.com/users/me/people"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if endpoint.RelyingParty != "default" {
		t.Fatalf("RelyingParty = %q, want default", endpoint.RelyingParty)
	}
}

func TestResolverConfigNewDefaultsTitleIDs(t *testing.T) {
	resolver := NewResolver(nil)
	if got, want := resolver.titleIDs(), []string{"current", "default"}; !slices.Equal(got, want) {
		t.Fatalf("TitleIDs = %v, want %v", got, want)
	}
}

func TestResolverConfigNewPreservesEmptyTitleIDs(t *testing.T) {
	resolver := ResolverConfig{TitleIDs: []string{}}.New(nil)
	if got := resolver.titleIDs(); len(got) != 0 {
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

func TestNilResolverDoesNotMatch(t *testing.T) {
	var resolver *Resolver

	if _, _, ok := resolver.Match(mustParseURL(t, "https://peoplehub.xboxlive.com/users/me/people")); ok {
		t.Fatal("nil Resolver matched URL")
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
