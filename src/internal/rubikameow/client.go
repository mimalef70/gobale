package rubikameow

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mimalef70/goomni/src/domains"
)

type Config struct {
	DiscoveryEndpoint, APIEndpoint, SocketEndpoint string
	HTTPClient                                     *http.Client
	ConnectionID                                   string
	PersistSession                                 domains.SessionPersister
	LoadCheckpoint                                 func(context.Context, string) (string, error)
	PollPermit                                     func(context.Context) (func(), error)
	MediaSource                                    domains.MediaSource
	MaxMediaBytes                                  int64
	SaveMediaReference                             func(context.Context, domains.Peer, string, domains.ProviderMedia) (bool, error)
}
type privateSession struct {
	Auth       string `json:"auth"`
	PrivateKey []byte `json:"private_key"`
	UserID     string `json:"user_id"`
}
type authChallenge struct {
	Public           domains.Challenge
	Phone, Hash, Tmp string
}
type Client struct {
	cfg         Config
	http        *http.Client
	mu          sync.RWMutex
	authMu      sync.Mutex
	connectMu   sync.Mutex
	discoveryMu sync.Mutex
	session     privateSession
	key         *rsa.PrivateKey
	challenge   authChallenge
	status      domains.ConnectionStatus
	api, socket string
	cancel      context.CancelFunc
	done        chan struct{}
	rpcSlots    chan struct{}
}

func New(cfg Config) (*Client, error) {
	if cfg.DiscoveryEndpoint == "" {
		cfg.DiscoveryEndpoint = "https://getdcmess.iranlms.ir/"
	}
	if cfg.MaxMediaBytes == 0 {
		cfg.MaxMediaBytes = 64 << 20
	}
	if cfg.MaxMediaBytes < 1 || cfg.MaxMediaBytes > 1<<30 {
		return nil, errors.New("invalid Rubika media limit")
	}
	for _, endpoint := range []string{cfg.DiscoveryEndpoint, cfg.APIEndpoint} {
		if endpoint != "" && !validEndpoint(endpoint, false, cfg.HTTPClient != nil) {
			return nil, errors.New("invalid Rubika endpoint")
		}
	}
	if cfg.SocketEndpoint != "" && !validEndpoint(cfg.SocketEndpoint, true, cfg.HTTPClient != nil) {
		return nil, errors.New("invalid Rubika socket endpoint")
	}
	h := http.Client{Timeout: 45 * time.Second}
	if cfg.HTTPClient != nil {
		h = *cfg.HTTPClient
	}
	if h.Timeout <= 0 || h.Timeout > 45*time.Second {
		h.Timeout = 45 * time.Second
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &h, api: cfg.APIEndpoint, socket: cfg.SocketEndpoint, rpcSlots: make(chan struct{}, 4), status: domains.ConnectionStatus{Auth: "unauthenticated", Transport: "disconnected", Recovery: "degraded"}}, nil
}
func validEndpoint(raw string, socket, local bool) bool {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	scheme := "https"
	if socket {
		scheme = "wss"
	}
	if local && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return u.Scheme == scheme || (!socket && u.Scheme == "http") || (socket && u.Scheme == "ws")
	}
	if u.Scheme != scheme {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "iranlms.ir" && !strings.HasSuffix(host, ".iranlms.ir") && host != "rubika.ir" && !strings.HasSuffix(host, ".rubika.ir") {
		return false
	}
	return u.Port() == "" || u.Port() == "443" || (socket && u.Port() == "80")
}
func (c *Client) SetSessionPersister(p domains.SessionPersister) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.PersistSession = p
}
func (c *Client) Status() domains.ConnectionStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}
func (c *Client) setStatus(auth, transport, recovery, code string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if auth != "" {
		c.status.Auth = auth
	}
	if transport != "" {
		c.status.Transport = transport
	}
	if recovery != "" {
		c.status.Recovery = recovery
		if c.status.RecoveryIssue == "chat_state_expired" || c.status.RecoveryIssue == "message_state_expired" {
			c.status.Recovery = "gap_detected"
		}
	}
	c.status.LastError = code
}
func (c *Client) CurrentChallenge() domains.Challenge {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.publicChallenge()
}
func clientIdentity() object {
	return object{"app_name": "Main", "app_version": "2.4.6", "platform": "PWA", "package": "m.rubika.ir", "lang_code": "fa"}
}
func protocolError() error {
	return domains.E("PROVIDER_PROTOCOL_ERROR", "Rubika returned an unsupported response", 502)
}
func (c *Client) postJSON(ctx context.Context, endpoint string, data object, mutating bool) (object, error) {
	raw, err := json.Marshal(data)
	if err != nil || len(raw) > maxPayload {
		return nil, protocolError()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, protocolError()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://web.rubika.ir")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, &domains.Error{Code: "PROVIDER_TRANSPORT_ERROR", Message: "Rubika request did not complete", HTTP: 502, Ambiguous: mutating, Retryable: !mutating}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, &domains.Error{Code: "PROVIDER_HTTP_ERROR", Message: "Rubika returned an unsuccessful HTTP response", HTTP: 502, Ambiguous: mutating}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxPayload+1))
	if err != nil || len(body) > maxPayload {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubika response could not be read", HTTP: 502, Ambiguous: mutating}
	}
	result, err := jsonObject(body)
	if err != nil {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubika response could not be decoded", HTTP: 502, Ambiguous: mutating}
	}
	return result, nil
}
func (c *Client) discover(ctx context.Context) error {
	c.discoveryMu.Lock()
	defer c.discoveryMu.Unlock()
	c.mu.RLock()
	ready := c.api != "" && c.socket != ""
	c.mu.RUnlock()
	if ready {
		return nil
	}
	response, err := c.postJSON(ctx, c.cfg.DiscoveryEndpoint, object{"api_version": "4", "client": clientIdentity(), "method": "getDCs"}, false)
	if err != nil {
		return err
	}
	data := asObject(response["data"])
	urls, ok := data["default_api_urls"].([]any)
	if !ok || len(urls) == 0 || len(urls) > 32 {
		return protocolError()
	}
	endpoint, ok := urls[0].(string)
	if !ok || !validEndpoint(endpoint, false, c.cfg.HTTPClient != nil) {
		return protocolError()
	}
	sockets, ok := data["default_sockets"].([]any)
	if !ok || len(sockets) == 0 || len(sockets) > 32 {
		return protocolError()
	}
	socket, ok := sockets[0].(string)
	if !ok || !validEndpoint(socket, true, c.cfg.HTTPClient != nil) {
		return protocolError()
	}
	c.mu.Lock()
	if c.api == "" {
		c.api = endpoint
	}
	if c.socket == "" {
		c.socket = socket
	}
	c.mu.Unlock()
	return nil
}
func (c *Client) invoke(ctx context.Context, method string, input object, mutating bool, tmp string) (object, error) {
	select {
	case c.rpcSlots <- struct{}{}:
		defer func() { <-c.rpcSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := c.discover(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	auth, key, endpoint := c.session.Auth, c.key, c.api
	c.mu.RUnlock()
	anonymous := tmp != ""
	if anonymous {
		auth = tmp
	}
	aesKey, err := authKey(auth)
	if err != nil {
		return nil, domains.E("AUTH_REQUIRED", "account authentication is required", 401)
	}
	encoded, err := encrypt(object{"method": method, "input": input, "client": clientIdentity()}, aesKey)
	if err != nil {
		return nil, protocolError()
	}
	envelope := object{"api_version": "6", "data_enc": encoded}
	if anonymous {
		envelope["tmp_session"] = auth
	} else {
		if key == nil {
			return nil, domains.E("AUTH_REQUIRED", "account authentication is required", 401)
		}
		signature, e := sign(key, encoded)
		if e != nil {
			return nil, protocolError()
		}
		envelope["auth"] = transformAuth(auth)
		envelope["sign"] = signature
	}
	response, err := c.postJSON(ctx, endpoint, envelope, mutating)
	if err != nil {
		return nil, err
	}
	encodedReply := response.str("data_enc")
	if encodedReply == "" {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubika returned an unencrypted response", HTTP: 502, Ambiguous: mutating}
	}
	decoded, err := decrypt(encodedReply, aesKey)
	if err != nil {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubika returned an invalid encrypted response", HTTP: 502, Ambiguous: mutating}
	}
	if decoded.str("status") != "OK" {
		failure := rpcError(decoded.str("status_det"))
		var de *domains.Error
		if decoded.str("status") == "" || (errors.As(failure, &de) && de.Code == "PROVIDER_REJECTED") {
			return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubika returned an unsupported response status", HTTP: 502, Ambiguous: mutating}
		}
		return nil, failure
	}
	data := asObject(decoded["data"])
	if data == nil {
		return nil, &domains.Error{Code: "PROVIDER_PROTOCOL_ERROR", Message: "Rubika returned an unsupported response body", HTTP: 502, Ambiguous: mutating}
	}
	return data, nil
}
func rpcError(status string) error {
	code, httpStatus := "PROVIDER_REJECTED", 502
	switch status {
	case "INVALID_AUTH", "AUTH_REQUIRED":
		code, httpStatus = "AUTH_REQUIRED", 401
	case "NOT_REGISTERED":
		code, httpStatus = "DEVICE_REGISTRATION_REQUIRED", 503
	case "INVALID_INPUT", "INVALID_ARGUMENT":
		code, httpStatus = "PROVIDER_INVALID_ARGUMENT", 400
	case "TOO_REQUESTS", "TooManyRequests":
		code, httpStatus = "RATE_LIMITED", 429
	case "CodeIsInvalid":
		code, httpStatus = "INVALID_CODE", 400
	case "CodeIsExpired", "CodeIsUsed":
		code, httpStatus = "CODE_EXPIRED", 400
	case "SendPassKey":
		code, httpStatus = "PASSWORD_REQUIRED", 401
	case "InvalidPassKey":
		code, httpStatus = "INVALID_PASSWORD", 400
	case "ACCESS_DENIED":
		code, httpStatus = "PROVIDER_FORBIDDEN", 403
	}
	return &domains.Error{Code: code, Message: "Rubika rejected the request", HTTP: httpStatus}
}
func (c *Client) StartAuth(ctx context.Context, phone string) (domains.Challenge, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	phone = strings.TrimPrefix(phone, "+")
	if len(phone) < 7 || len(phone) > 16 {
		return domains.Challenge{}, domains.E("INVALID_PHONE", "international phone number is required", 400)
	}
	for _, r := range phone {
		if r < '0' || r > '9' {
			return domains.Challenge{}, domains.E("INVALID_PHONE", "international phone number is required", 400)
		}
	}
	tmp, e := randomAuth()
	if e != nil {
		return domains.Challenge{}, e
	}
	c.challenge = authChallenge{Phone: phone, Tmp: tmp, Public: domains.Challenge{ID: hex.EncodeToString([]byte(tmp[:16])), ExpiresAt: time.Now().Add(10 * time.Minute)}}
	if e = c.sendCode(ctx, ""); e != nil {
		c.challenge = authChallenge{}
		return domains.Challenge{}, e
	}
	return c.publicChallenge(), nil
}
func (c *Client) sendCode(ctx context.Context, password string) error {
	input := object{"phone_number": c.challenge.Phone, "send_type": "SMS"}
	if password != "" {
		input["pass_key"] = password
	}
	o, e := c.invoke(ctx, "sendCode", input, true, c.challenge.Tmp)
	if e != nil {
		return e
	}
	switch o.str("status") {
	case "SendPassKey":
		c.challenge.Public.State = "awaiting_password"
		c.setStatus("awaiting_password", "disconnected", "degraded", "")
		return nil
	case "InvalidPassKey", "TooManyRequests":
		return rpcError(o.str("status"))
	case "OK", "Sent":
	default:
		return protocolError()
	}
	hash := o.str("phone_code_hash")
	if hash == "" || len(hash) > 1024 {
		return protocolError()
	}
	c.challenge.Hash = hash
	c.challenge.Public.State = "awaiting_code"
	c.challenge.Public.Delivery = codeDelivery(o.str("send_type"))
	c.challenge.Public.AvailableDeliveries = []string{}
	if c.challenge.Public.Delivery != "unknown" {
		c.challenge.Public.AvailableDeliveries = []string{c.challenge.Public.Delivery}
	}
	if n := o.num("send_code_timeout"); n > 0 && n < 3600 {
		c.challenge.Public.ResendAfterSeconds = &n
	}
	c.setStatus("awaiting_code", "disconnected", "degraded", "")
	return nil
}
func (c *Client) validChallenge(id, state string) error {
	if c.challenge.Public.ID == "" || id != c.challenge.Public.ID || !time.Now().Before(c.challenge.Public.ExpiresAt) {
		return domains.E("CHALLENGE_EXPIRED", "login challenge is missing or expired", 400)
	}
	if c.challenge.Public.State != state {
		return domains.E("INVALID_AUTH_STATE", "unexpected login step", 409)
	}
	return nil
}
func (c *Client) SubmitPassword(ctx context.Context, id, password string) (*domains.Session, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if e := c.validChallenge(id, "awaiting_password"); e != nil {
		return nil, e
	}
	if password == "" || len(password) > 1024 {
		return nil, domains.E("INVALID_PASSWORD", "password is required", 400)
	}
	if e := c.sendCode(ctx, password); e != nil {
		return nil, e
	}
	return nil, nil
}
func (c *Client) SubmitCode(ctx context.Context, id, code string) (*domains.Session, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if e := c.validChallenge(id, "awaiting_code"); e != nil {
		return nil, e
	}
	if code == "" || len(code) > 32 {
		return nil, domains.E("INVALID_CODE", "code is required", 400)
	}
	public, private, e := createKeys()
	if e != nil {
		return nil, e
	}
	key, e := parsePrivateKey(private)
	if e != nil {
		return nil, e
	}
	o, e := c.invoke(ctx, "signIn", object{"phone_code": code, "phone_number": c.challenge.Phone, "phone_code_hash": c.challenge.Hash, "public_key": public}, true, c.challenge.Tmp)
	if e != nil {
		return nil, e
	}
	if o.str("status") != "OK" {
		return nil, rpcError(o.str("status"))
	}
	auth, e := decryptAuth(key, o.str("auth"))
	if e != nil {
		return nil, protocolError()
	}
	user := asObject(o["user"]).str("user_guid")
	if !(Contract{}).ValidateUserID(user) {
		return nil, protocolError()
	}
	session := privateSession{Auth: auth, PrivateKey: private, UserID: user}
	raw, e := json.Marshal(session)
	if e != nil {
		return nil, e
	}
	result := &domains.Session{Provider: domains.ProviderRubika, Version: 1, UserID: user, Phone: c.challenge.Phone, Data: raw}
	c.mu.Lock()
	c.session = session
	c.key = key
	c.mu.Unlock()
	c.challenge = authChallenge{}
	c.setStatus("authenticated", "disconnected", "degraded", "")
	return result, nil
}
func (c *Client) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.setStatus("", "disconnected", "degraded", "")
	return nil
}
func (c *Client) Logout(ctx context.Context) error {
	if e := c.Disconnect(ctx); e != nil {
		return e
	}
	_, e := c.invoke(ctx, "logout", object{}, true, "")
	if e != nil {
		return e
	}
	c.mu.Lock()
	c.session = privateSession{}
	c.key = nil
	c.mu.Unlock()
	c.setStatus("unauthenticated", "disconnected", "degraded", "")
	return nil
}
