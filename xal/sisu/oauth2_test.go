package sisu

import (
	"context"
	"net/http"
	"testing"

	"golang.org/x/oauth2"
)

func TestOAuth2ContextClientIgnoresTypedNil(t *testing.T) {
	var client *http.Client
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	if got := oauth2ContextClient(ctx); got != http.DefaultClient {
		t.Fatalf("client = %p, want default client %p", got, http.DefaultClient)
	}
}

func TestOAuth2ContextClientUsesConfiguredClient(t *testing.T) {
	client := &http.Client{}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
	if got := oauth2ContextClient(ctx); got != client {
		t.Fatalf("client = %p, want %p", got, client)
	}
}
