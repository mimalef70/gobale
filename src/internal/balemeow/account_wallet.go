package balemeow

import (
	"context"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/mimalef70/goomni/src/domains"
	"github.com/mimalef70/goomni/src/internal/balemeow/wire"
)

// accountWalletCall is strictly read-only. Payment/charge tokens, bank account
// details and authenticated wallet links are not part of this API projection.
func (c *Client) accountWalletCall(ctx context.Context, op string, raw json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	var p struct {
		PocketType *int32 `json:"pocket_type"`
	}
	if len(raw) > 4096 || decodeAccountBody(raw, &p) != nil {
		return nil, boundedError("INVALID_REQUEST", "invalid wallet read body", 400)
	}
	switch op {
	case "wallet.list":
		if p.PocketType != nil {
			return nil, boundedError("INVALID_REQUEST", "wallet.list does not accept pocket_type", 400)
		}
		data, err := c.readRPC(ctx, "bale.wallet.v1.Wallet", "GetMyWallets", &wire.Empty{})
		if err != nil {
			return nil, err
		}
		r := &wire.AccountWalletsResponse{}
		if decode(data, r) != nil || len(r.Wallets) > 100 {
			return nil, protocolError()
		}
		wallets := []map[string]any{}
		for _, w := range r.Wallets {
			if w == nil || w.Id == "" || len(w.Id) > 256 || !utf8.ValidString(w.Id) || len(w.Balances) > 16 {
				return nil, protocolError()
			}
			balances := []map[string]any{}
			for _, b := range w.Balances {
				if b == nil || b.Currency < 0 {
					return nil, protocolError()
				}
				balances = append(balances, map[string]any{"currency": b.Currency, "amount": strconv.FormatInt(b.Amount, 10)})
			}
			v := map[string]any{"id": w.Id, "balances": balances}
			if w.IsActive != nil {
				v["active"] = w.IsActive.Value
			}
			wallets = append(wallets, v)
		}
		return json.Marshal(map[string]any{"wallets": wallets})
	case "wallet.kifpools":
		pocket := int32(0)
		if p.PocketType != nil {
			pocket = *p.PocketType
		}
		switch pocket {
		case 0, 2200, 3300, 6680, 9999:
		default:
			return nil, boundedError("INVALID_REQUEST", "unsupported pocket_type", 400)
		}
		data, err := c.readRPC(ctx, "bale.kifpool.v1.Kifpool", "GetMyKifpools", &wire.AccountKifpoolsRequest{PocketType: pocket})
		if err != nil {
			return nil, err
		}
		r := &wire.AccountKifpoolsResponse{}
		if decode(data, r) != nil || len(r.Wallets) > 100 {
			return nil, protocolError()
		}
		wallets := []map[string]any{}
		for _, w := range r.Wallets {
			if w == nil || len(w.App) > 256 || !utf8.ValidString(w.App) || w.Level < 0 {
				return nil, protocolError()
			}
			wallets = append(wallets, map[string]any{"merchant": w.IsMerchant, "app": w.App, "balance": strconv.FormatInt(w.Balance, 10), "level": w.Level})
		}
		return json.Marshal(map[string]any{"wallets": wallets, "pocket_type": pocket})
	default:
		return nil, domains.Unsupported(op)
	}
}
