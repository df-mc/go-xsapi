package mpsd

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestActivitySearchRequestOwnersAreExclusive(t *testing.T) {
	scid := uuid.MustParse("4fc10100-5f7a-4470-899b-280835760c07")

	// Explicit xuids must not carry the people moniker: the directory
	// rejects requests specifying both with 400 Bad Request.
	body, err := json.Marshal(activitySearchRequest(scid, []string{"123"}, "999"))
	if err != nil {
		t.Fatal(err)
	}
	var withXUIDs struct {
		Owners struct {
			XUIDs  []string        `json:"xuids"`
			People json.RawMessage `json:"people"`
		} `json:"owners"`
	}
	if err := json.Unmarshal(body, &withXUIDs); err != nil {
		t.Fatal(err)
	}
	if len(withXUIDs.Owners.XUIDs) != 1 || withXUIDs.Owners.XUIDs[0] != "123" {
		t.Fatalf("unexpected xuids in %s", body)
	}
	if withXUIDs.Owners.People != nil {
		t.Fatalf("people moniker must be omitted with explicit xuids: %s", body)
	}

	// Without xuids the caller's social group is used.
	body, err = json.Marshal(activitySearchRequest(scid, nil, "999"))
	if err != nil {
		t.Fatal(err)
	}
	var withPeople struct {
		Owners struct {
			XUIDs  json.RawMessage `json:"xuids"`
			People struct {
				Moniker     string `json:"moniker"`
				MonikerXUID string `json:"monikerXuid"`
			} `json:"people"`
		} `json:"owners"`
	}
	if err := json.Unmarshal(body, &withPeople); err != nil {
		t.Fatal(err)
	}
	if withPeople.Owners.XUIDs != nil {
		t.Fatalf("xuids must be omitted for social group search: %s", body)
	}
	if withPeople.Owners.People.Moniker != "people" || withPeople.Owners.People.MonikerXUID != "999" {
		t.Fatalf("unexpected people moniker in %s", body)
	}
}
