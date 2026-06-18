package social

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUserByGamerTagMatchesUniqueModernGamerTag(t *testing.T) {
	c := newPeopleHubSearchClient(t, "Player#1234", []User{
		{
			GamerTag:             "Player1234",
			ModernGamerTag:       "Player",
			ModernGamerTagSuffix: "1234",
			UniqueModernGamerTag: "Player#1234",
		},
	})

	user, err := c.UserByGamerTag(context.Background(), "Player#1234")
	if err != nil {
		t.Fatalf("UserByGamerTag returned error: %v", err)
	}
	if user.UniqueModernGamerTag != "Player#1234" {
		t.Fatalf("unique modern gamertag = %q, want Player#1234", user.UniqueModernGamerTag)
	}
}

func TestUserByGamerTagSkipsFuzzySearchResults(t *testing.T) {
	c := newPeopleHubSearchClient(t, "Player#1234", []User{
		{GamerTag: "OtherPlayer"},
		{
			GamerTag:             "Player1234",
			ModernGamerTag:       "Player",
			ModernGamerTagSuffix: "1234",
		},
	})

	user, err := c.UserByGamerTag(context.Background(), "Player#1234")
	if err != nil {
		t.Fatalf("UserByGamerTag returned error: %v", err)
	}
	if user.GamerTag != "Player1234" {
		t.Fatalf("gamertag = %q, want Player1234", user.GamerTag)
	}
}

func TestUserByGamerTagRejectsFuzzyOnlyResults(t *testing.T) {
	c := newPeopleHubSearchClient(t, "Player#1234", []User{
		{GamerTag: "OtherPlayer"},
		{ModernGamerTag: "Player"},
	})

	_, err := c.UserByGamerTag(context.Background(), "Player#1234")
	if err == nil {
		t.Fatal("UserByGamerTag returned nil error, want no exact match")
	}
	if !strings.Contains(err.Error(), "user not found") {
		t.Fatalf("UserByGamerTag error = %v, want user not found", err)
	}
}

func newPeopleHubSearchClient(t *testing.T, query string, users []User) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != query {
			t.Fatalf("query = %q, want %s", got, query)
		}
		if err := json.NewEncoder(w).Encode(batchResponse{Users: users}); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	oldEndpoint := peopleHubEndpoint
	peopleHubEndpoint = u
	t.Cleanup(func() {
		peopleHubEndpoint = oldEndpoint
	})

	return &Client{client: srv.Client()}
}
