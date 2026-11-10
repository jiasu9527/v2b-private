package newapibridge

import (
	"context"
	"encoding/hex"
	"math/big"
	"net/http"
	"strings"
)

// CNY quotes preserve the gateway's quota scale and model prices. Only the
// payment input changes from integer gateway units to integer renminbi fen.
type workspaceCNYQuote struct {
	AmountFen     int64  `json:"amount_fen"`
	CreditedQuota int64  `json:"credited_quota"`
	QuotaPerUnit  int64  `json:"quota_per_unit"`
	Currency      string `json:"currency"`
	DisplayRate   string `json:"display_rate"`
	QuoteHash     string `json:"quote_hash"`
}

func validWorkspaceCNYQuote(q workspaceCNYQuote) bool {
	if q.Currency != "CNY" || q.AmountFen < 100 || q.AmountFen > 100000000 || q.CreditedQuota <= 0 || q.QuotaPerUnit <= 0 || len(q.DisplayRate) == 0 || len(q.DisplayRate) > 64 || strings.Count(q.DisplayRate, ".") > 1 {
		return false
	}
	for _, c := range q.DisplayRate {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	hash, err := hex.DecodeString(q.QuoteHash)
	if err != nil || len(hash) != 32 {
		return false
	}
	rate, ok := new(big.Rat).SetString(q.DisplayRate)
	if !ok || rate.Sign() <= 0 {
		return false
	}
	// Match the server's nearest-whole-quota, half-away-from-zero rule exactly.
	// Never accept a quote solely because its displayed money matches.
	exact := new(big.Rat).Mul(new(big.Rat).SetInt64(q.AmountFen), new(big.Rat).SetInt64(q.QuotaPerUnit))
	exact.Quo(exact, new(big.Rat).Mul(rate, new(big.Rat).SetInt64(100)))
	remainder := new(big.Int)
	rounded, _ := new(big.Int).QuoRem(exact.Num(), exact.Denom(), remainder)
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(exact.Denom()) >= 0 {
		rounded.Add(rounded, big.NewInt(1))
	}
	if rounded.Cmp(big.NewInt(q.CreditedQuota)) != 0 {
		return false
	}
	credited := new(big.Rat).Mul(new(big.Rat).SetInt64(q.CreditedQuota), rate)
	credited.Quo(credited, new(big.Rat).SetInt64(q.QuotaPerUnit))
	return credited.FloatString(2) == formatMoneyFen(q.AmountFen)
}

func (s *Service) workspaceCNYQuote(ctx context.Context, cfg Settings, c workspaceSession, fen int64) (workspaceCNYQuote, error) {
	env, _, err := s.workspaceRequest(ctx, cfg, &c, http.MethodPost, "/api/user/amount", map[string]any{"amount_fen": fen}, "")
	var quote workspaceCNYQuote
	if err != nil {
		return quote, err
	}
	if workspaceDecode(env.Data, &quote) != nil || quote.AmountFen != fen || !validWorkspaceCNYQuote(quote) {
		return quote, ErrRemoteUnavailable
	}
	return quote, nil
}

func workspaceCNYQuoteMatches(c workspaceCNYQuote, q workspaceQuote) bool {
	return validWorkspaceCNYQuote(c) && c.AmountFen == q.AmountFen && c.CreditedQuota == q.CreditedQuota && c.QuotaPerUnit == q.Unit && c.Currency == q.Currency && c.DisplayRate == q.DisplayRate && c.QuoteHash == q.RemoteQuoteHash
}

func workspaceOrderQuotaMatches(order remoteTopUp, amount int64, q workspaceQuote) bool {
	if q.RemoteQuoteHash != "" {
		return amount == 0 && workspaceCNYQuoteMatches(workspaceCNYQuote{AmountFen: order.AmountFen, CreditedQuota: order.CreditedQuota, QuotaPerUnit: order.QuotaPerUnit, Currency: order.Currency, DisplayRate: order.DisplayRate, QuoteHash: order.QuoteHash}, q)
	}
	// Partial new-format orders must not silently fall back to legacy math.
	if order.AmountFen != 0 || order.CreditedQuota != 0 || order.QuotaPerUnit != 0 || order.Currency != "" || order.DisplayRate != "" || order.QuoteHash != "" {
		return false
	}
	return amount > 0 && q.Unit > 0 && amount <= 9223372036854775807/q.Unit && amount*q.Unit == q.CreditedQuota
}
