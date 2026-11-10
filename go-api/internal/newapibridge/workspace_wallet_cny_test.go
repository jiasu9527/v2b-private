package newapibridge

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func fixtureCNYQuote(fen int64) workspaceCNYQuote {
	return workspaceCNYQuote{
		AmountFen: fen, CreditedQuota: (fen*500000 + 365) / 730,
		QuotaPerUnit: 500000, Currency: "CNY", DisplayRate: "7.3",
		QuoteHash: fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("cny:%d:7.3", fen)))),
	}
}

func fixtureWorkspaceCNYQuote(q workspaceCNYQuote) workspaceQuote {
	return workspaceQuote{AmountFen: q.AmountFen, CreditedQuota: q.CreditedQuota,
		Unit: q.QuotaPerUnit, Currency: q.Currency, DisplayRate: q.DisplayRate,
		DisplayCurrency: "CNY", DisplayCredit: formatMoneyFen(q.AmountFen), RemoteQuoteHash: q.QuoteHash}
}

func fixtureCNYTopUp(q workspaceCNYQuote) remoteTopUp {
	return remoteTopUp{AmountFen: q.AmountFen, CreditedQuota: q.CreditedQuota,
		QuotaPerUnit: q.QuotaPerUnit, Currency: q.Currency, DisplayRate: q.DisplayRate, QuoteHash: q.QuoteHash}
}

func TestWorkspaceCNYQuoteValidatesCentAmountsWithoutUnitMultiples(t *testing.T) {
	for fen := int64(100); fen <= 10000; fen++ {
		q := fixtureCNYQuote(fen)
		if !validWorkspaceCNYQuote(q) {
			t.Fatalf("valid cent amount rejected: fen=%d quota=%d", fen, q.CreditedQuota)
		}
	}
	for _, fen := range []int64{100, 123, 1000, 99999, 100000000} {
		q := fixtureCNYQuote(fen)
		if !workspaceCNYQuoteMatches(q, fixtureWorkspaceCNYQuote(q)) {
			t.Fatalf("quote does not match itself: %+v", q)
		}
	}
}

func TestWorkspaceCNYQuoteRejectsInvalidOrDishonestSnapshots(t *testing.T) {
	mutations := map[string]func(*workspaceCNYQuote){
		"under_minimum": func(q *workspaceCNYQuote) { q.AmountFen = 99 },
		"zero":          func(q *workspaceCNYQuote) { q.AmountFen = 0 },
		"negative":      func(q *workspaceCNYQuote) { q.AmountFen = -100 },
		"over_maximum":  func(q *workspaceCNYQuote) { q.AmountFen = 100000001 },
		"missing_quota": func(q *workspaceCNYQuote) { q.CreditedQuota = 0 },
		"inflated_quota": func(q *workspaceCNYQuote) {
			q.CreditedQuota++ // Still rounds to the same cent; exact quota conversion must reject it.
		},
		"missing_unit":  func(q *workspaceCNYQuote) { q.QuotaPerUnit = 0 },
		"currency":      func(q *workspaceCNYQuote) { q.Currency = "USD" },
		"missing_rate":  func(q *workspaceCNYQuote) { q.DisplayRate = "" },
		"zero_rate":     func(q *workspaceCNYQuote) { q.DisplayRate = "0" },
		"bad_rate":      func(q *workspaceCNYQuote) { q.DisplayRate = "NaN" },
		"exponent_rate": func(q *workspaceCNYQuote) { q.DisplayRate = "73e-1" },
		"fraction_rate": func(q *workspaceCNYQuote) { q.DisplayRate = "73/10" },
		"long_rate":     func(q *workspaceCNYQuote) { q.DisplayRate = strings.Repeat("1", 65) },
		"missing_hash":  func(q *workspaceCNYQuote) { q.QuoteHash = "" },
		"bad_hash":      func(q *workspaceCNYQuote) { q.QuoteHash = strings.Repeat("g", 64) },
		"short_hash":    func(q *workspaceCNYQuote) { q.QuoteHash = strings.Repeat("a", 63) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			q := fixtureCNYQuote(123)
			mutate(&q)
			if validWorkspaceCNYQuote(q) {
				t.Fatalf("invalid quote accepted: %+v", q)
			}
		})
	}
}

func TestWorkspaceCNYQuoteRoundsExactHalfQuotaUp(t *testing.T) {
	q := fixtureCNYQuote(100)
	q.QuotaPerUnit, q.DisplayRate, q.CreditedQuota = 2001, "2", 1001
	if !validWorkspaceCNYQuote(q) {
		t.Fatal("exact half quota must round up")
	}
	q.CreditedQuota = 1000
	if validWorkspaceCNYQuote(q) {
		t.Fatal("rounded-down half quota accepted")
	}
}

func TestWorkspaceCNYOrderUsesSnapshotAndNeverFallsBackToLegacy(t *testing.T) {
	q := fixtureCNYQuote(123)
	local := fixtureWorkspaceCNYQuote(q)
	order := fixtureCNYTopUp(q)
	if !workspaceOrderQuotaMatches(order, 0, local) {
		t.Fatal("CNY order should match its saved quote")
	}
	if workspaceOrderQuotaMatches(order, 1, local) {
		t.Fatal("CNY snapshot must not also use legacy amount")
	}
	order.CreditedQuota = 0
	if workspaceOrderQuotaMatches(order, 0, local) {
		t.Fatal("partial snapshot accepted")
	}
	legacy := workspaceQuote{Unit: 500000, CreditedQuota: 1000000}
	if !workspaceOrderQuotaMatches(remoteTopUp{}, 2, legacy) {
		t.Fatal("legacy order compatibility lost")
	}
	for _, partial := range []remoteTopUp{{AmountFen: 100}, {CreditedQuota: 1}, {QuotaPerUnit: 500000}, {Currency: "CNY"}, {DisplayRate: "7.3"}, {QuoteHash: q.QuoteHash}} {
		if workspaceOrderQuotaMatches(partial, 2, legacy) {
			t.Fatalf("partial new order fell back to legacy: %+v", partial)
		}
	}
	if workspaceOrderQuotaMatches(remoteTopUp{}, 9223372036854775807, legacy) {
		t.Fatal("legacy multiplication overflow accepted")
	}
}
