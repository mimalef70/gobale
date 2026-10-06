package balemeow

import (
	"errors"
	"github.com/mimalef70/gobale/src/domains"
)

// protocolFault identifies a local schema/coverage rejection without exposing
// provider payloads or user data. The public HTTP error remains PROTOCOL_ERROR.
type protocolFault struct {
	error
	code string
}

func (e *protocolFault) Unwrap() error { return e.error }
func updateFault(code string) error    { return &protocolFault{error: protocolError(), code: code} }
func diagnosticCode(err error, fallback string) string {
	var fault *protocolFault
	if errors.As(err, &fault) {
		return fault.code
	}
	var api *domains.Error
	if errors.As(err, &api) {
		switch api.Code {
		case "PROTOCOL_ERROR":
			return fallback + "_PROTOCOL"
		case "AUTH_REVOKED", "AUTH_REQUIRED":
			return fallback + "_AUTH"
		case "CONNECTION_UNAVAILABLE", "PROVIDER_READ_FAILED":
			return fallback + "_TRANSPORT"
		}
	}
	return fallback
}
func (c *Client) reportDiagnostic(code string) {
	if c.opts.OnDiagnostic != nil {
		c.opts.OnDiagnostic(Diagnostic{Code: code})
	}
}
func isProtocolFault(err error) bool { var f *protocolFault; return errors.As(err, &f) }
