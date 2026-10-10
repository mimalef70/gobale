package balemeow

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
	"google.golang.org/protobuf/proto"
)

func (c *Client) StartAuth(ctx context.Context, phone string) (domains.Challenge, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.opts.AppID == 0 || c.opts.AppID > 2147483647 || c.opts.APIKey == "" {
		return domains.Challenge{}, boundedError("CLIENT_IDENTITY_REQUIRED", "configure the Bale app ID and API key before authentication", 503)
	}
	normalized, err := normalizePhone(phone)
	if err != nil {
		return domains.Challenge{}, err
	}
	c.mu.Lock()
	authenticated := c.session != nil && c.status.Auth == "authenticated"
	c.mu.Unlock()
	if authenticated {
		return domains.Challenge{}, boundedError("ALREADY_AUTHENTICATED", "logout before starting another authentication", 409)
	}
	// Invalidate the previous challenge before cookies or transport are replaced.
	// A failed replacement request must not leave a seemingly usable old code.
	c.mu.Lock()
	c.challenge = nil
	c.status.Auth = "auth_required"
	c.mu.Unlock()
	// Starting a fresh login must not inherit cookies from a revoked challenge.
	c.clearCookies()
	hash, err := randomHex(16)
	if err != nil {
		return domains.Challenge{}, err
	}
	id, err := randomHex(16)
	if err != nil {
		return domains.Challenge{}, err
	}
	hashBytes, _ := hex.DecodeString(hash)
	number, _ := strconv.ParseInt(normalized, 10, 64)
	result, err := c.unary(ctx, "StartPhoneAuth", &wire.StartPhoneAuthRequest{PhoneNumber: number, AppId: int32(c.opts.AppID), ApiKey: c.opts.APIKey, DeviceHash: hashBytes, DeviceTitle: c.opts.DeviceTitle})
	if err != nil {
		return domains.Challenge{}, err
	}
	reply := &wire.StartPhoneAuthResponse{}
	if decode(result, reply) != nil || reply.TransactionHash == "" || len(reply.TransactionHash) > 4096 {
		return domains.Challenge{}, protocolError()
	}
	metadata, err := authMetadata(reply, time.Now())
	if err != nil {
		return domains.Challenge{}, err
	}
	challenge := &authChallenge{id: id, transaction: reply.TransactionHash, phone: "+" + normalized, deviceHash: hash, expires: metadata.ExpiresAt}
	c.mu.Lock()
	c.challenge = challenge
	c.status.Auth = "awaiting_code"
	c.status.LastError = ""
	c.mu.Unlock()
	metadata.ID, metadata.State = id, "awaiting_code"
	return metadata, nil
}

func (c *Client) SubmitCode(ctx context.Context, challengeID, code string) (*domains.Session, error) {
	normalized, err := normalizeDigits(code, false)
	if err != nil || len(normalized) < 3 || len(normalized) > 12 {
		return nil, boundedError("INVALID_CODE", "code must contain 3 to 12 digits", 400)
	}
	return c.authenticate(ctx, challengeID, normalized, false)
}
func (c *Client) SubmitPassword(ctx context.Context, challengeID, password string) (*domains.Session, error) {
	if password == "" || len(password) > 4096 {
		return nil, boundedError("INVALID_PASSWORD", "password is required and must be at most 4096 bytes", 400)
	}
	return c.authenticate(ctx, challengeID, password, true)
}
func (c *Client) authenticate(ctx context.Context, challengeID, secret string, password bool) (*domains.Session, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.mu.Lock()
	ch := c.challenge
	c.mu.Unlock()
	if ch == nil || ch.id != challengeID || time.Now().After(ch.expires) {
		return nil, boundedError("CHALLENGE_EXPIRED", "authentication challenge is absent or expired", 400)
	}
	var request proto.Message
	method := "ValidateCode"
	if password {
		method = "ValidatePassword"
		request = &wire.ValidatePasswordRequest{TransactionHash: ch.transaction, Password: secret, IsJwt: &wire.BoolValue{Value: true}, Language: 1}
	} else {
		request = &wire.ValidateCodeRequest{TransactionHash: ch.transaction, Code: secret, IsJwt: &wire.BoolValue{Value: true}, Language: 1}
	}
	data, err := c.unary(ctx, method, request)
	if err != nil {
		var e *domains.Error
		if errors.As(err, &e) && e.Code == "PASSWORD_REQUIRED" {
			c.mu.Lock()
			c.status.Auth = "awaiting_password"
			c.mu.Unlock()
			return nil, nil
		}
		return nil, err
	}
	response := &wire.AuthResponse{}
	if decode(data, response) != nil || response.User == nil || response.User.Id == 0 || response.Jwt == nil || response.Jwt.Value == "" {
		return nil, protocolError()
	}
	c.rememberRef("user", &wire.PeerRef{Id: response.User.Id, AccessHash: response.User.AccessHash})
	privateData := sessionData{}
	_ = json.Unmarshal(c.snapshotCookies(), &privateData)
	privateData.SelfAccessHash = strconv.FormatInt(response.User.AccessHash, 10)
	storedData, _ := json.Marshal(privateData)
	session := &domains.Session{Provider: domains.ProviderBale, Version: 1, UserID: strconv.FormatUint(uint64(response.User.Id), 10), Token: response.Jwt.Value, DeviceHash: ch.deviceHash, Phone: ch.phone, Data: storedData}
	c.mu.Lock()
	if c.session != nil && c.session.UserID != session.UserID {
		c.mu.Unlock()
		return nil, boundedError("ACCOUNT_CONFLICT", "use a new device for a different account", 409)
	}
	stored := *session
	c.session = &stored
	c.challenge = nil
	c.status.Auth = "authenticated"
	c.status.LastError = ""
	c.mu.Unlock()
	return session, nil
}

func requiresPassword(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch s {
	case "PASSWORD_REQUIRED", "SESSION_PASSWORD_NEEDED", "PASSWORD_NEEDED", "2FA_REQUIRED", "PASSWORD NEEDED FOR LOGIN":
		return true
	}
	return false
}

func normalizeDigits(s string, phone bool) (string, error) {
	var out strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '0' && r <= '9':
			out.WriteRune(r)
		case r >= '۰' && r <= '۹':
			out.WriteByte(byte(r-'۰') + '0')
		case r >= '٠' && r <= '٩':
			out.WriteByte(byte(r-'٠') + '0')
		case phone && (r == '+' || r == ' ' || r == '-' || r == '(' || r == ')'):
		default:
			return "", errors.New("invalid digit")
		}
	}
	return out.String(), nil
}
func normalizePhone(s string) (string, error) {
	n, err := normalizeDigits(s, true)
	if strings.HasPrefix(n, "0098") {
		n = n[2:]
	}
	if len(n) == 11 && strings.HasPrefix(n, "09") {
		n = "98" + n[1:]
	}
	if err != nil || len(n) < 8 || len(n) > 15 || n[0] == '0' {
		return "", boundedError("INVALID_PHONE", "phone must be an international number", 400)
	}
	return n, nil
}

func (c *Client) unary(ctx context.Context, method string, message proto.Message) ([]byte, error) {
	data, err := proto.Marshal(message)
	if err != nil {
		return nil, protocolError()
	}
	if int64(len(data)) > c.opts.MaxFrameBytes {
		return nil, boundedError("FRAME_TOO_LARGE", "authentication request exceeds frame limit", 413)
	}
	base, err := url.Parse(c.opts.GRPCEndpoint)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") || base.User != nil {
		return nil, boundedError("INVALID_CONFIG", "invalid gRPC-Web endpoint", 500)
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/bale.auth.v1.Auth/" + method
	base.RawQuery = ""
	base.Fragment = ""
	callCtx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, base.String(), bytes.NewReader(grpcFrame(0, data)))
	if err != nil {
		return nil, boundedError("INVALID_CONFIG", "invalid gRPC-Web request", 500)
	}
	request.Header.Set("Content-Type", "application/grpc-web+proto")
	request.Header.Set("Accept", "application/grpc-web+proto")
	request.Header.Set("X-Grpc-Web", "1")
	request.Header.Set("Origin", c.opts.Origin)
	c.mu.Lock()
	token := ""
	if c.session != nil {
		token = c.session.Token
	}
	c.mu.Unlock()
	for _, item := range c.metadata(token).Items {
		request.Header.Set(item.Key, item.Value.StringValue)
	}
	response, err := c.opts.HTTPClient.Do(request)
	if err != nil {
		return nil, boundedError("AUTH_REQUEST_FAILED", "authentication request failed; its remote outcome is unknown", 502)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, boundedError("AUTH_HTTP_ERROR", "authentication endpoint returned an unexpected HTTP status", 502)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.opts.MaxFrameBytes+1))
	if err != nil {
		return nil, boundedError("AUTH_REQUEST_FAILED", "authentication response was interrupted", 502)
	}
	if int64(len(body)) > c.opts.MaxFrameBytes {
		return nil, boundedError("FRAME_TOO_LARGE", "authentication response exceeds frame limit", 502)
	}
	payload, err := parseGRPCWeb(body, response.Header.Get("Grpc-Status"), response.Header.Get("Grpc-Message"))
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func grpcFrame(flag byte, data []byte) []byte {
	b := make([]byte, 5+len(data))
	b[0] = flag
	binary.BigEndian.PutUint32(b[1:5], uint32(len(data)))
	copy(b[5:], data)
	return b
}

func parseGRPCWeb(data []byte, headerStatus, headerMessage string) ([]byte, error) {
	if headerStatus != "" {
		headerCode, err := strconv.ParseInt(headerStatus, 10, 32)
		if err != nil {
			return nil, protocolError()
		}
		if headerCode != 0 {
			return nil, authProviderError(int32(headerCode), headerMessage)
		}
	}
	payload := []byte(nil)
	seenData, seenTrailer := false, false
	status, message := headerStatus, headerMessage
	for len(data) > 0 {
		if len(data) < 5 {
			return nil, protocolError()
		}
		flag := data[0]
		length := uint64(binary.BigEndian.Uint32(data[1:5]))
		data = data[5:]
		if length > uint64(len(data)) {
			return nil, protocolError()
		}
		frame := data[:int(length)]
		data = data[int(length):]
		switch flag {
		case 0:
			if seenData || seenTrailer {
				return nil, protocolError()
			}
			seenData = true
			payload = frame
		case 128:
			if seenTrailer {
				return nil, protocolError()
			}
			seenTrailer = true
			for _, line := range strings.Split(string(frame), "\r\n") {
				key, value, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "grpc-status":
					status = strings.TrimSpace(value)
				case "grpc-message":
					message = strings.TrimSpace(value)
				}
			}
		default:
			return nil, protocolError()
		}
	}
	// gRPC-Web explicitly reports success in trailers (or a trailers-only header).
	// Missing status cannot be interpreted as a successful authenticated response.
	code, err := strconv.ParseInt(status, 10, 32)
	if err != nil {
		return nil, protocolError()
	}
	if code != 0 {
		decoded, e := url.PathUnescape(message)
		if e == nil {
			message = decoded
		}
		return nil, authProviderError(int32(code), message)
	}
	if !seenData {
		return nil, protocolError()
	}
	return payload, nil
}
