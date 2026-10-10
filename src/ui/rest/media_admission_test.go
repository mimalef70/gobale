package rest

import (
	"context"
	"testing"
	"time"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/infrastructure/workpool"
	"github.com/stretchr/testify/require"
)

func TestMediaAdmissionDeadlineReleasesWaiterWithoutReleasingActiveStream(t *testing.T) {
	srv, svc := setupAPI(t, "")
	d, err := svc.CreateDevice(context.Background(), "media-wait", domains.ProviderBale)
	require.NoError(t, err)
	srv.mediaPool = workpool.New(1)
	srv.mediaPool.SetProviders([]domains.Provider{domains.ProviderBale})
	release, ok := srv.mediaPool.TryAcquireFor(domains.ProviderBale, d.ConnectionID)
	require.True(t, ok)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	acquired, err := srv.acquireMedia(ctx, d)
	require.Nil(t, acquired)
	var de *domains.Error
	require.ErrorAs(t, err, &de)
	require.Equal(t, "REQUEST_TIMEOUT", de.Code)
	require.Equal(t, 504, de.HTTP)
	require.Equal(t, 1, srv.mediaPool.Active())
	release()
	require.Zero(t, srv.mediaPool.Active())
	next, err := srv.acquireMedia(context.Background(), d)
	require.NoError(t, err)
	next()
	require.Zero(t, srv.mediaPool.Active())
}
