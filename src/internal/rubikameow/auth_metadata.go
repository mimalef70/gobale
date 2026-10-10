package rubikameow

import "github.com/mimalef70/goomni/src/domains"

// send_type comes from the provider response, not the requested SMS transport.
func codeDelivery(value string) string {
	switch value {
	case "SMS":
		return "sms"
	case "Internal":
		return "app"
	case "CallCode":
		return "call"
	default:
		return "unknown"
	}
}
func (c *Client) publicChallenge() domains.Challenge {
	out := c.challenge.Public
	out.AvailableDeliveries = append([]string{}, out.AvailableDeliveries...)
	if out.ResendAfterSeconds != nil {
		seconds := *out.ResendAfterSeconds
		out.ResendAfterSeconds = &seconds
	}
	return out
}
