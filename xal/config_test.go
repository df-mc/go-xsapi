package xal

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestContextClientDefaultHasTimeout(t *testing.T) {
	client := ContextClient(context.Background())
	if client == http.DefaultClient {
		t.Fatal("client = http.DefaultClient, want bounded default client")
	}
	if client.Timeout != 30*time.Second {
		t.Fatalf("client timeout = %v, want %v", client.Timeout, 30*time.Second)
	}
}

func TestContextClientPreservesContextClient(t *testing.T) {
	client := &http.Client{}
	ctx := context.WithValue(context.Background(), HTTPClient, client)
	if got := ContextClient(ctx); got != client {
		t.Fatalf("client = %p, want context client %p", got, client)
	}
}
