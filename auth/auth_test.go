package auth

import (
	"encoding/json"
	"testing"
)

// The model list's subscription filter depends on this flag surviving decode:
// GetFollowedUsers merges free follows and active subscriptions into one slice,
// and only "subscribed" tells them apart.
func TestFollowedModelDecodesSubscribedFlag(t *testing.T) {
	body := `{
		"success": true,
		"response": [
			{"id": "1", "username": "paid", "subscribed": true, "timelineStats": {"imageCount": 3}},
			{"id": "2", "username": "free", "subscribed": false},
			{"id": "3", "username": "unset"}
		]
	}`

	var decoded struct {
		Success  bool            `json:"success"`
		Response []FollowedModel `json:"response"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if len(decoded.Response) != 3 {
		t.Fatalf("expected 3 models, got %d", len(decoded.Response))
	}
	if !decoded.Response[0].Subscribed {
		t.Error("subscribed=true was not decoded")
	}
	if decoded.Response[1].Subscribed || decoded.Response[2].Subscribed {
		t.Error("subscribed=false/absent must decode as false")
	}
	if decoded.Response[0].TimelineStats.ImageCount != 3 {
		t.Error("adding the field broke timelineStats decoding")
	}
}
