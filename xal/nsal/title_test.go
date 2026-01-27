package nsal

import (
	"net/url"
	"testing"
)

// TestDefault simulates obtaining the default title in NSAL.
// It typically contains Xbox Live services such as MPSD or RTA.
func TestDefault(t *testing.T) {
	title, err := Default(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range title.Endpoints {
		t.Logf("%#v", endpoint)
		t.Log(title.SignaturePolicies[endpoint.SignaturePolicyIndex])
	}
}

// TestMatch demonstrates resolving an Endpoint with a SignaturePolicy
// based on the request URL using the default TitleData.
func TestMatch(t *testing.T) {
	title, err := Default(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// We test the Default title data with the URL for MPSD,
	// which should match for the *.xboxlive.com wildcard rule.
	requestURL, err := url.Parse("https://sessiondirectory.xboxlive.com")
	if err != nil {
		t.Fatalf("error parsing URL: %s", err)
	}
	endpoint, _, ok := title.Match(requestURL)
	if !ok {
		t.Fatal("no match")
	}
	t.Logf("%#v", endpoint)
}
