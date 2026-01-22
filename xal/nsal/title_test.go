package nsal

import (
	"net/url"
	"testing"
)

func TestTitle(t *testing.T) {
	title, err := Default(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range title.Endpoints {
		t.Logf("%#v", endpoint)
		t.Log(title.SignaturePolicies[endpoint.SignaturePolicyIndex])
	}
}

func TestMatch(t *testing.T) {
	title, err := Default(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _, ok := title.Match(must(url.Parse("https://sessiondirectory.xboxlive.com")))
	if !ok {
		t.Fatal("no match")
	}
	t.Logf("%#v", endpoint)
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
