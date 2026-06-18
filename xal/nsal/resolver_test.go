package nsal

import (
	"net/url"
	"testing"
)

func TestResolverPrefersCurrentTitleData(t *testing.T) {
	index := 0
	resolver := &Resolver{
		Current: &TitleData{
			Endpoints: []Endpoint{{
				Protocol:             "https",
				Host:                 "*.playfabapi.com",
				HostType:             HostTypeWildcard,
				RelyingParty:         "current",
				SignaturePolicyIndex: &index,
			}},
			SignaturePolicies: []SignaturePolicy{{Version: 2}},
		},
		Default: &TitleData{
			Endpoints: []Endpoint{{
				Protocol:             "https",
				Host:                 "*.playfabapi.com",
				HostType:             HostTypeWildcard,
				RelyingParty:         "default",
				SignaturePolicyIndex: &index,
			}},
			SignaturePolicies: []SignaturePolicy{{Version: 1}},
		},
	}

	endpoint, policy, ok := resolver.Match(mustParseURL(t, "https://20ca2.playfabapi.com/Client/LoginWithXbox"))
	if !ok {
		t.Fatal("Match returned ok=false")
	}
	if endpoint.RelyingParty != "current" {
		t.Fatalf("RelyingParty = %q, want current", endpoint.RelyingParty)
	}
	if policy.Version != 2 {
		t.Fatalf("SignaturePolicy.Version = %d, want 2", policy.Version)
	}
}

func TestResolverFallsBackToDefaultTitleData(t *testing.T) {
	resolver := &Resolver{
		Current: &TitleData{
			Endpoints: []Endpoint{{
				Protocol:     "https",
				Host:         "title.mgt.xboxlive.com",
				HostType:     HostTypeFQDN,
				RelyingParty: "current",
			}},
		},
		Default: &TitleData{
			Endpoints: []Endpoint{{
				Protocol:     "https",
				Host:         "*.xboxlive.com",
				HostType:     HostTypeWildcard,
				RelyingParty: "default",
			}},
		},
	}

	endpoint, _, ok := resolver.Match(mustParseURL(t, "https://peoplehub.xboxlive.com/users/me/people"))
	if !ok {
		t.Fatal("Match returned ok=false")
	}
	if endpoint.RelyingParty != "default" {
		t.Fatalf("RelyingParty = %q, want default", endpoint.RelyingParty)
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
