package social

import "testing"

func TestUserMatchesGamertagModernForms(t *testing.T) {
	user := User{
		GamerTag:             "Player1234",
		ModernGamerTag:       "Player",
		ModernGamerTagSuffix: "1234",
		UniqueModernGamerTag: "Player#1234",
	}
	for _, gamertag := range []string{"Player1234", "Player#1234", "player#1234"} {
		if !user.matchesGamertag(gamertag) {
			t.Fatalf("matchesGamertag(%q) = false, want true", gamertag)
		}
	}
}
