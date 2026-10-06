package rest

import (
	"context"
	"testing"

	"github.com/mimalef70/gobale/src/domains"
	"github.com/stretchr/testify/require"
)

type revokedBrowserAccount struct{ testClient }

func (f *revokedBrowserAccount) Connect(context.Context, *domains.Session, domains.Sink) error {
	return domains.E("AUTH_REQUIRED", "synthetic provider session revoked", 401)
}

// A provider 401 and a browser-session 401 are deliberately distinct contracts.
// A frontend must not discard the administrator's valid cookie when one Bale
// account needs another login (native reconnect has this exact failure mode).
func TestAdminReviewProvider401KeepsValidAdministrativeSession(t *testing.T) {
	srv, svc, st := integrationBrowserServer(t, "", true, func(domains.Device) domains.Client { return &revokedBrowserAccount{} })
	ctx := context.Background()
	d, err := svc.CreateDevice(ctx, "revoked")
	require.NoError(t, err)
	require.NoError(t, st.SaveSession(ctx, d.ConnectionID, &domains.Session{UserID: "1001", Token: "synthetic-revoked-session"}))
	session := integrationLogin(t, srv)
	res, out := browserIntegrationRequest(t, srv, session, "POST", "/ui/api/devices/revoked/reconnect", nil, browserInstance(d), false)
	require.Equal(t, 401, res.StatusCode, out)
	require.Equal(t, "AUTH_REQUIRED", out["code"])
	res, out = browserIntegrationRequest(t, srv, session, "GET", "/ui/auth/session", nil, nil, false)
	require.Equal(t, 200, res.StatusCode, out)
	require.Equal(t, session.csrf, out["results"].(map[string]any)["csrf_token"])
	res, out = browserIntegrationRequest(t, srv, browserIntegrationSession{}, "GET", "/ui/api/devices", nil, nil, false)
	require.Equal(t, 401, res.StatusCode, out)
	require.Equal(t, "UI_UNAUTHORIZED", out["code"])
}
