package balemeow

import (
	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"strings"
	"time"
)

// authMetadata follows the reviewed web 173855 response. Field 8 is the resend
// cooldown despite its legacy code_timeout name; code lifetime is field 9.
func authMetadata(r *wire.StartPhoneAuthResponse, now time.Time) (domains.Challenge, error) {
	out := domains.Challenge{Delivery: "unknown", AvailableDeliveries: []string{"unknown"}, ExpiresAt: now.Add(10 * time.Minute), SentCodeType: r.SentCodeType, NextSendCodeType: r.NextSendCodeType}
	if r.SentCodeType < 0 || r.NextSendCodeType < 0 || len(r.AvailableSendCodeTypes) > 32 {
		return out, protocolError()
	}
	if r.CodeTimeout != nil {
		n := r.CodeTimeout.Value
		if n < 0 || n > 86400 {
			return out, protocolError()
		}
		expiry := now.Add(time.Duration(n) * time.Second)
		if expiry.Before(out.ExpiresAt) {
			out.ExpiresAt = expiry
		}
	} else if r.CodeExpirationDate != nil {
		n := r.CodeExpirationDate.Value
		if n <= 0 {
			return out, protocolError()
		}
		expiry := time.UnixMilli(n)
		if expiry.Before(out.ExpiresAt) {
			out.ExpiresAt = expiry
		}
	}
	if r.NextSendCodeWaitTime != nil {
		n := r.NextSendCodeWaitTime.Value
		if n < 0 || n > 86400 {
			return out, protocolError()
		}
		out.ResendAfterSeconds = &n
	}
	for _, v := range r.AvailableSendCodeTypes {
		if v < 0 {
			return out, protocolError()
		}
		out.AvailableSendCodeTypes = append(out.AvailableSendCodeTypes, v)
	}
	return out, nil
}

// Exact known authentication phrases only; arbitrary server input never becomes
// a public error message. These phrases are also present in official web 173855.
func authProviderError(code int32, message string) error {
	switch strings.ToUpper(strings.TrimSpace(message)) {
	case "PHONE_CODE_INVALID", "WRONG CODE":
		return boundedError("INVALID_CODE", "the verification code is invalid", 400)
	case "PHONE_CODE_EXPIRED", "OTP NOT VALIDATED":
		return boundedError("CHALLENGE_EXPIRED", "the verification challenge has expired", 400)
	case "WRONG PASSWORD":
		return boundedError("INVALID_PASSWORD", "the account password is invalid", 400)
	case "PHONE_NUMBER_INVALID":
		return boundedError("INVALID_PHONE", "provider rejected the phone number", 400)
	case "PHONE_NUMBER_UNOCCUPIED":
		return boundedError("ACCOUNT_NOT_REGISTERED", "this phone number is not registered", 400)
	case "PHONE NUMBER IS BLOCKED", "PHONE_NUMBER_TEMPORARY_BLOCKED":
		return boundedError("ACCOUNT_BLOCKED", "provider has restricted authentication for this account", 403)
	case "PHONE AUTH LIMIT EXCEEDED":
		return boundedError("PROVIDER_RATE_LIMITED", "provider authentication rate limit reached", 429)
	}
	return providerError(code, message)
}
