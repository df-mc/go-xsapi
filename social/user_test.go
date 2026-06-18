package social

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestUserByGamerTagMatchesUniqueModernGamerTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "Player#1234" {
			t.Fatalf("query = %q, want Player#1234", got)
		}
		if err := json.NewEncoder(w).Encode(batchResponse{Users: []User{
			{
				GamerTag:             "Player1234",
				ModernGamerTag:       "Player",
				ModernGamerTagSuffix: "1234",
				UniqueModernGamerTag: "Player#1234",
			},
		}}); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	oldEndpoint := peopleHubEndpoint
	peopleHubEndpoint = u
	t.Cleanup(func() {
		peopleHubEndpoint = oldEndpoint
	})

	c := &Client{client: srv.Client()}
	user, err := c.UserByGamerTag(context.Background(), "Player#1234")
	if err != nil {
		t.Fatalf("UserByGamerTag returned error: %v", err)
	}
	if user.UniqueModernGamerTag != "Player#1234" {
		t.Fatalf("unique modern gamertag = %q, want Player#1234", user.UniqueModernGamerTag)
	}
}
