package balemeow

import (
	"context"
	"github.com/mimalef70/goomni/src/domains"
)

// The native adapter alone identifies reviewed Bale RID echoes. Storage still
// verifies the accepted event's immutable account, peer and original message.
func (c *Client) prepareBatchSink(sink domains.BatchSink) domains.BatchSink {
	if sink == nil {
		return nil
	}
	return func(ctx context.Context, batch domains.EventBatch) error {
		batch.Proofs = nil
		for i := range batch.Events {
			event := c.prepareEvent(ctx, batch.Events[i])
			batch.Events[i] = event
			if event.Type == "message" && event.Direction == "outgoing" && event.SenderID == event.AccountID && event.MessageID != "" {
				batch.Proofs = append(batch.Proofs, domains.AcceptanceProof{Provider: domains.ProviderBale, Kind: domains.BaleMessageEchoProof, EventID: event.ID, RequestID: event.MessageID})
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		commitCtx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
		defer cancel()
		return sink(commitCtx, batch)
	}
}
