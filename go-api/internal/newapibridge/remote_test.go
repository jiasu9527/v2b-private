package newapibridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestWalletMoneyIsIntegerFen(t *testing.T) {
	for value, want := range map[string]int64{"1": 100, "1.2": 120, "1.23": 123, "0.01": 1, "999999.99": 99999999} {
		got, err := parseMoneyFen(value)
		if err != nil || got != want {
			t.Fatalf("parseMoneyFen(%q)=%d,%v want %d", value, got, err, want)
		}
	}
	for _, value := range []string{"", "0", "0.00", "-1", "+1", "1e2", ".1", "1.", "1.001", "NaN", " 1", "1 ", "92233720368547758.08", "1,00"} {
		if got, err := parseMoneyFen(value); err == nil {
			t.Fatalf("accepted %q as %d", value, got)
		}
	}
	if got, err := remoteMoneyFen(json.Number("12.340000000000002")); err != nil || got != 1234 {
		t.Fatal(got, err)
	}
	if got := formatMoneyFen(101); got != "1.01" {
		t.Fatal(got)
	}
}

func TestEpaySignatureBindsFieldsAndRejectsAmbiguity(t *testing.T) {
	values := url.Values{"pid": {"forest"}, "money": {"12.30"}, "out_trade_no": {"test-order"}, "type": {"wallet"}, "sign_type": {"MD5"}}
	values.Set("sign", epaySignature(values, "fixture-key"))
	if !validEpaySignature(values, "fixture-key") {
		t.Fatal("valid signature rejected")
	}
	values.Set("money", "12.31")
	if validEpaySignature(values, "fixture-key") {
		t.Fatal("tampered amount accepted")
	}
	values.Set("money", "12.30")
	values.Add("money", "12.30")
	if validEpaySignature(values, "fixture-key") {
		t.Fatal("duplicate amount accepted")
	}
	values["money"] = []string{"12.30"}
	values.Set("sign_type", "SHA256")
	if validEpaySignature(values, "fixture-key") {
		t.Fatal("wrong sign protocol accepted")
	}
}

func TestRemoteTopupUsesExactOrderNotFuzzySearchResult(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-secret" || r.Header.Get("New-Api-User") != "1" {
			t.Error("missing scoped remote credentials")
		}
		if r.URL.Query().Get("keyword") != "wanted" {
			t.Error("incorrect keyword")
		}
		items := make([]remoteTopUp, 100)
		for i := range items {
			items[i] = remoteTopUp{ID: int64(i + 1), UserID: 4, TradeNo: "wanted-fuzzy-" + strconv.Itoa(i), Money: json.Number("1.00")}
		}
		if r.URL.Query().Get("p") == "2" {
			items = []remoteTopUp{{ID: 101, UserID: 4, TradeNo: "wanted", Money: json.Number("2.00"), Status: "pending", PaymentProvider: "epay", PaymentMethod: "wallet"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"total": 101, "items": items}})
	}))
	defer server.Close()
	s := &Service{client: server.Client()}
	topup, err := s.readRemoteTopUp(context.Background(), remotePaymentConfig{APIURL: server.URL, AdminToken: "test-secret", AdminUserID: 1}, "wanted")
	if err != nil || topup.ID != 101 || calls.Load() != 2 {
		t.Fatal(topup, err, calls.Load())
	}
}

func TestRemoteRedirectDoesNotForwardSecrets(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true); w.WriteHeader(http.StatusOK) }))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	s := &Service{client: origin.Client()}
	_, err := s.readRemoteUser(context.Background(), remotePaymentConfig{APIURL: origin.URL, AdminToken: "do-not-forward", AdminUserID: 1}, 9)
	if !errors.Is(err, ErrRemoteUnavailable) || leaked.Load() {
		t.Fatal("redirect followed or unexpected error", err)
	}
}

func TestWalletRemoteOrderAndIdentityMustAgree(t *testing.T) {
	c := Checkout{RemoteTradeNo: "A", RemoteUserID: 9, AmountFen: 1234, paymentMethod: "wallet"}
	valid := remoteTopUp{TradeNo: "A", UserID: 9, Money: json.Number("12.34"), PaymentProvider: "epay", PaymentMethod: "wallet"}
	if err := verifyTopUpMatchesCheckout(valid, c); err != nil {
		t.Fatal(err)
	}
	bad := valid
	bad.Money = json.Number("12.35")
	if err := verifyTopUpMatchesCheckout(bad, c); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad = valid
	bad.UserID = 10
	if err := verifyTopUpMatchesCheckout(bad, c); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad = valid
	bad.PaymentProvider = "stripe"
	if err := verifyTopUpMatchesCheckout(bad, c); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	for _, subject := range []string{"", "1", "forest:01", "forest:-1", "forest:1 ", "other:1"} {
		if _, err := localSubjectID(subject); err == nil {
			t.Fatal("accepted subject", subject)
		}
	}
	if got, err := localSubjectID("forest:1"); err != nil || got != 1 {
		t.Fatal(got, err)
	}
}
