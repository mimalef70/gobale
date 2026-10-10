package domains

import "testing"

// This literal was computed from the GoBale 2.2 connection identity contract.
// Branding changes must not invalidate guards already saved by consumers.
func TestConnectionInstanceSurvivesProductRename(t *testing.T) {
	d := Device{ConnectionID: "synthetic-connection-before-rename"}
	const retained = "3c16c66fc825c30b4ce5dff297fcdf61d2bf9476862454ddd8fcbeec11ec70cd"
	if got := d.InstanceToken(); got != retained {
		t.Fatalf("persisted instance guard changed: %s", got)
	}
	d.ID = "new-local-alias"
	d.Provider = ProviderRubika
	if d.InstanceToken() != retained {
		t.Fatal("alias or provider changed the immutable connection guard")
	}
}
