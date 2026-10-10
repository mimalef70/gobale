package storage

import "github.com/mimalef70/goomni/src/domains"

// Configuration is read under the same transaction as event/delivery acceptance.
// A device URL overrides global routes even when its filter rejects the event.
func resolveWebhookTargets(cfg domains.WebhookConfig, event domains.Event, targets []WebhookTarget) []WebhookTarget {
	result := []WebhookTarget{}
	globalAllowed := true
	for _, t := range targets {
		if t.CurrentDevice {
			globalAllowed = cfg.URL == "" || t.MergeGlobal
			break
		}
	}
	for _, t := range targets {
		if t.CurrentDevice {
			t = WebhookTarget{URL: cfg.URL, Secret: cfg.Secret, Revision: cfg.Revision, Device: true}
		}
		if t.Device {
			if t.URL == "" || !domains.EventAllowed(event.Type, cfg.Events) || !cfg.Filter.Matches(event) {
				continue
			}
		} else if !globalAllowed || !domains.EventAllowed(event.Type, t.Events) {
			continue
		}
		result = append(result, t)
	}
	return result
}
