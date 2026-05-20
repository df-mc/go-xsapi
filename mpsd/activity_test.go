package mpsd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/df-mc/go-xsapi/v2/xal/xsts"
	"github.com/google/uuid"
)

func TestActivitiesUsesPeopleSocialGroupFilter(t *testing.T) {
	var requestBody map[string]any
	userXUID := "2533274799999999"
	client := &Client{
		userInfo: xsts.UserInfo{XUID: userXUID},
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			defer func() { _ = req.Body.Close() }()
			if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return testResponse(req, http.StatusOK, nil, []byte(`{"results":[]}`)), nil
		})},
	}

	if _, err := client.Activities(context.Background(), uuid.New()); err != nil {
		t.Fatalf("Activities returned error: %v", err)
	}

	owners, ok := requestBody["owners"].(map[string]any)
	if !ok {
		t.Fatalf("owners = %#v, want object", requestBody["owners"])
	}
	people, ok := owners["people"].(map[string]any)
	if !ok {
		t.Fatalf("owners.people = %#v, want object", owners["people"])
	}
	if got := people["moniker"]; got != "people" {
		t.Fatalf("owners.people.moniker = %#v, want %q", got, "people")
	}
	if got := people["monikerXuid"]; got != userXUID {
		t.Fatalf("owners.people.monikerXuid = %#v, want %q", got, userXUID)
	}
	if _, ok := owners["xuids"]; ok {
		t.Fatalf("owners.xuids = %#v, want omitted for social-group query", owners["xuids"])
	}
}

func TestActivitiesRequiresCallerXUID(t *testing.T) {
	requests := 0
	client := &Client{
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return testResponse(req, http.StatusOK, nil, []byte(`{"results":[]}`)), nil
		})},
	}

	_, err := client.Activities(context.Background(), uuid.New())
	if !errors.Is(err, errActivitiesRequiresCallerXUID) {
		t.Fatalf("Activities error = %v, want %v", err, errActivitiesRequiresCallerXUID)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestActivitiesForUsersRequiresAtLeastOneXUID(t *testing.T) {
	requests := 0
	client := &Client{
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requests++
			return testResponse(req, http.StatusOK, nil, []byte(`{"results":[]}`)), nil
		})},
	}

	_, err := client.ActivitiesForUsers(context.Background(), uuid.New(), nil)
	if !errors.Is(err, errActivitiesRequiresXUID) {
		t.Fatalf("ActivitiesForUsers error = %v, want %v", err, errActivitiesRequiresXUID)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestActivitiesForUsersEncodesOnlyXUIDFilter(t *testing.T) {
	var requestBody map[string]any
	xuids := []string{"123", "456"}
	client := &Client{
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			defer func() { _ = req.Body.Close() }()
			if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			return testResponse(req, http.StatusOK, nil, []byte(`{"results":[]}`)), nil
		})},
	}

	if _, err := client.ActivitiesForUsers(context.Background(), uuid.New(), xuids); err != nil {
		t.Fatalf("ActivitiesForUsers returned error: %v", err)
	}

	owners, ok := requestBody["owners"].(map[string]any)
	if !ok {
		t.Fatalf("owners = %#v, want object", requestBody["owners"])
	}
	if _, ok := owners["people"]; ok {
		t.Fatalf("owners.people = %#v, want omitted", owners["people"])
	}
	gotXUIDs, ok := owners["xuids"].([]any)
	if !ok {
		t.Fatalf("owners.xuids = %#v, want array", owners["xuids"])
	}
	if len(gotXUIDs) != len(xuids) {
		t.Fatalf("owners.xuids length = %d, want %d", len(gotXUIDs), len(xuids))
	}
	for i, xuid := range xuids {
		if gotXUIDs[i] != xuid {
			t.Fatalf("owners.xuids[%d] = %#v, want %q", i, gotXUIDs[i], xuid)
		}
	}
}
