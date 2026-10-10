package eitaameow

import "github.com/mimalef70/goomni/src/domains"

func codeDelivery(constructor string) string {
	switch constructor {
	case "auth.sentCodeTypeSms", "auth.codeTypeSms":
		return "sms"
	case "auth.sentCodeTypeCall", "auth.codeTypeCall":
		return "call"
	case "auth.sentCodeTypeFlashCall", "auth.codeTypeFlashCall":
		return "flash_call"
	case "auth.sentCodeTypeApp":
		return "app"
	default:
		return "unknown"
	}
}

// The caller holds authMu; no private phone/code/hash is part of this value.
func (c *Client) publicChallenge() domains.Challenge {
	out := c.challenge.Public
	out.AvailableDeliveries = append([]string{}, out.AvailableDeliveries...)
	if out.ResendAfterSeconds != nil {
		seconds := *out.ResendAfterSeconds
		out.ResendAfterSeconds = &seconds
	}
	return out
}
func (c *Client) CurrentChallenge() domains.Challenge {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.publicChallenge()
}

var _ domains.AuthChallengeSource = (*Client)(nil)
