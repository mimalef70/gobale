package storage

import (
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/providers/bale"
)

// Policies contain no persistence or provider calls. The storage transaction
// supplies the original durable event and request, never a changed duplicate.
type proofPolicy func(string, domains.Event, domains.SendRequest) bool

func acceptancePolicy(provider domains.Provider, kind string) proofPolicy {
	if provider == domains.ProviderBale && kind == domains.BaleMessageEchoProof {
		return bale.OwnMessageProof
	}
	return nil
}
