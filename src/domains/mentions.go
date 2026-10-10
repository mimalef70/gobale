package domains

// ValidateMentions validates bounded opaque identifiers without performing lookup or
// guessing identities from names/phones. The native protocol owns wire encoding.
func ValidateMentions(text string, mentions []string) error {
	if len(mentions) > 100 || (len(mentions) > 0 && text == "") {
		return E("INVALID_REQUEST", "mentions require text or caption and at most 100 unique user IDs", 400)
	}
	seen := make(map[string]bool, len(mentions))
	for _, id := range mentions {
		if !ValidOpaqueID(id) || seen[id] {
			return E("INVALID_REQUEST", "mentions must contain unique provider user IDs", 400)
		}
		seen[id] = true
	}
	return nil
}
