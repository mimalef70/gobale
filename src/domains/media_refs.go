package domains

import (
	"encoding/json"
	"sort"
)

// ReferencedMediaIDs returns only reviewed local media fields. Provider file
// identifiers, quoted attachments and arbitrary nested strings are not local
// assets. The outer field is retained for older persisted mutation requests.
func ReferencedMediaIDs(request SendRequest) ([]string, error) {
	seen := make(map[string]bool)
	if request.MediaID != "" {
		seen[request.MediaID] = true
	}
	switch request.Operation {
	case "message.album":
		var payload struct {
			Items []struct {
				MediaID string `json:"media_id"`
			} `json:"items"`
		}
		if err := json.Unmarshal(request.Payload, &payload); err != nil || len(payload.Items) < 2 || len(payload.Items) > 10 {
			return nil, E("INVALID_MEDIA_REFERENCE", "stored album references cannot be decoded", 500)
		}
		for _, item := range payload.Items {
			if !ValidOpaqueID(item.MediaID) {
				return nil, E("INVALID_MEDIA_REFERENCE", "stored album media reference is invalid", 500)
			}
			seen[item.MediaID] = true
		}
	case "account.avatar", "group.photo", "story.add":
		var payload struct {
			MediaID string `json:"media_id"`
		}
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			return nil, E("INVALID_MEDIA_REFERENCE", "stored media reference cannot be decoded", 500)
		}
		if payload.MediaID != "" {
			seen[payload.MediaID] = true
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
