package notification

import (
	"encoding/json"
	"testing"
)

// GameTypes arrives as escaped JSON in a string and may be absent entirely.
func TestGameInviteLaunchInfoUnmarshal(t *testing.T) {
	var withTypes GameInviteLaunchInfo
	if err := json.Unmarshal([]byte(`{"mpsdHandleId":"4fc10100-5f7a-4470-899b-280835760c07","gameTypes":"{\"android\":{\"titleId\":\"1739947436\"}}"}`), &withTypes); err != nil {
		t.Fatalf("unmarshal with gameTypes: %v", err)
	}
	if _, ok := withTypes.GameTypes["android"]; !ok {
		t.Fatalf("GameTypes = %v, want android entry", withTypes.GameTypes)
	}

	var withoutTypes GameInviteLaunchInfo
	if err := json.Unmarshal([]byte(`{"mpsdHandleId":"4fc10100-5f7a-4470-899b-280835760c07"}`), &withoutTypes); err != nil {
		t.Fatalf("unmarshal without gameTypes: %v", err)
	}
	if withoutTypes.GameTypes == nil || len(withoutTypes.GameTypes) != 0 {
		t.Fatalf("GameTypes = %v, want empty non-nil map", withoutTypes.GameTypes)
	}
}
