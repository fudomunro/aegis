package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/core/sampledata"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestNormalizeTransactionAmount(t *testing.T) {
	cases := map[string]string{
		"-45.67":  "-45.67",
		"45.67":   "45.67",
		"+45.67":  "45.67",
		"-1,234":  "-1234.00",
		"12":      "12.00",
		"-12.3":   "-12.30",
		"":        "",
		"  -1.5 ": "-1.50",
	}
	for in, want := range cases {
		if got := kinds.NormalizeTransactionAmount(in); got != want {
			t.Fatalf("kinds.NormalizeTransactionAmount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatMilliunitsAmount(t *testing.T) {
	cases := map[int64]string{
		0:       "0.00",
		45670:   "45.67",
		-45670:  "-45.67",
		1000:    "1.00",
		1:       "0.00",
		15:      "0.02", // rounds half-up at mills
		1000000: "1000.00",
	}
	for in, want := range cases {
		if got := kinds.FormatMilliunitsAmount(in); got != want {
			t.Fatalf("kinds.FormatMilliunitsAmount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestYNABCategories_FromSplits(t *testing.T) {
	split := "Split"
	groceries := "Groceries"
	household := "Household Goods"
	shopping := "Shopping"
	cats := connectors.YnabCategories(connectors.YnabTransaction{
		CategoryName: &split,
		Subtransactions: []connectors.YnabSubtransaction{
			{CategoryName: &groceries},
			{CategoryName: &household},
			{CategoryName: &groceries}, // duplicate
		},
	})
	if len(cats) != 2 || cats[0] != "Groceries" || cats[1] != "Household Goods" {
		t.Fatalf("cats = %v", cats)
	}
	// Parent-only category (no splits).
	cats2 := connectors.YnabCategories(connectors.YnabTransaction{CategoryName: &shopping})
	if len(cats2) != 1 || cats2[0] != "Shopping" {
		t.Fatalf("cats2 = %v", cats2)
	}
}

func TestValidatePermissions_SummaryOnlyOnTransactions(t *testing.T) {
	if err := policy.ValidatePermissions(policy.Permissions{"transactions": policy.AccessSummary}); err != nil {
		t.Fatalf("summary on transactions: %v", err)
	}
	if err := policy.ValidatePermissions(policy.Permissions{"tracks": policy.AccessSummary}); err == nil {
		t.Fatal("expected summary on tracks rejected")
	}
	if err := policy.ValidatePermissions(policy.Permissions{"default": policy.AccessSummary}); err == nil {
		t.Fatal("expected summary on default rejected")
	}
}

type fakeTransactionProvider struct {
	txs []kinds.Transaction
	err error
}

func (f *fakeTransactionProvider) ListUserTransactions(ctx context.Context) ([]kinds.Transaction, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	return f.txs, false, nil
}

func (f *fakeTransactionProvider) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{}
}
func (f *fakeTransactionProvider) SystemInstructions(string) string { return "" }
func (f *fakeTransactionProvider) HandleProxy(http.ResponseWriter, *http.Request, string) {
}

func TestHandleLibraryTransactions_AggregateAndSummary(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "ynab", Name: "YNAB", Action: "inject"},
			{ID: "quicken", Name: "Quicken", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"ynab": &fakeTransactionProvider{txs: sampledata.GenerateSampleLibrary().Transactions[:3]},
		"quicken": &fakeTransactionProvider{txs: []kinds.Transaction{{
			PostedAt: "2024-03-06", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Account: &kinds.TransactionAccount{Name: "Credit Card"},
			Sources: []kinds.TransactionSource{{Service: "quicken", ServiceName: "Quicken", ID: "q-coffee"}},
		}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/transactions", nil)
	w := httptest.NewRecorder()
	srv.libraryHandler("transactions")(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list kinds.TransactionList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total == 0 {
		t.Fatal("expected transactions")
	}
	if list.Summary == nil {
		t.Fatal("expected summary rollup")
	}

	// Service-level summary redaction: no line items from that service, and
	// app-level summary returns totals only.
	req2 := httptest.NewRequest("GET", "/v1/library/transactions", nil)
	w2 := httptest.NewRecorder()
	srv.libraryHandler("transactions")(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w2.Code, w2.Body.String())
	}
}

func TestHandleLibraryTransactions_Filters(t *testing.T) {
	cfg := &Config{
		Services: []Service{
			{ID: "ynab", Name: "YNAB", Action: "inject"},
		},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		"ynab": &fakeTransactionProvider{txs: []kinds.Transaction{
			{PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon", Sources: []kinds.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "1"}}},
			{PostedAt: "2024-03-10", Amount: "-12.00", Merchant: "Blue Bottle", Sources: []kinds.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "2"}}},
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/transactions?after=2024-03-08", nil)
	w := httptest.NewRecorder()
	srv.libraryHandler("transactions")(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var list kinds.TransactionList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("total = %d, want 1", list.Total)
	}
}

func TestYNABNeutralTransaction_Millunits(t *testing.T) {
	c := &connectors.YNABConnector{}
	payee := "Amazon"
	tx := c.NeutralTransactionCurrency("budget-1", []connectors.YNABAccount{{ID: "a1", Name: "Checking", Type: "checking"}}, connectors.YnabTransaction{
		ID:        "t1",
		Date:      "2024-03-05",
		Amount:    -45670,
		PayeeName: &payee,
		AccountID: "a1",
		Cleared:   "cleared",
		ImportID:  strPtr("IMP-1"),
	}, "USD")
	if tx.Amount != "-45.67" {
		t.Fatalf("amount = %q", tx.Amount)
	}
	if tx.Merchant != "Amazon" {
		t.Fatalf("merchant = %q", tx.Merchant)
	}
	if tx.EntityID != "IMP-1" {
		t.Fatalf("entity = %q", tx.EntityID)
	}
	if tx.Account == nil || tx.Account.Name != "Checking" {
		t.Fatalf("account = %+v", tx.Account)
	}
	if tx.Status != kinds.TransactionStatusPosted {
		t.Fatalf("status = %q", tx.Status)
	}
}

func strPtr(s string) *string { return &s }

func TestYNABListUserTransactions_Unauthorized(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "ynab", Name: "YNAB"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	c := connectors.NewYNABConnector(srv)
	_, _, err := c.ListUserTransactions(context.Background())
	if err != kinds.ErrUnauthorized {
		t.Fatalf("err = %v, want kinds.ErrUnauthorized", err)
	}
}

func TestYNABListUserTransactions_MockAPI(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"budgets": []map[string]any{{
					"id":              "b1",
					"name":            "My Budget",
					"currency_format": map[string]any{"iso_code": "USD"},
				}},
			},
		})
	})
	mux.HandleFunc("/v1/budgets/b1/accounts", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"accounts": []map[string]any{{"id": "a1", "name": "Checking", "type": "checking"}},
			},
		})
	})
	mux.HandleFunc("/v1/budgets/b1/transactions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"transactions": []map[string]any{
					{
						"id":           "t1",
						"date":         "2024-03-05",
						"amount":       -12345,
						"payee_name":   "Coffee Shop",
						"account_id":   "a1",
						"account_name": "Checking",
						"cleared":      "cleared",
						"import_id":    "IMP-9",
						"deleted":      false,
					},
					{
						"id":            "t2",
						"date":          "2024-03-12",
						"amount":        -200000,
						"payee_name":    "Costco",
						"account_id":    "a1",
						"account_name":  "Checking",
						"cleared":       "cleared",
						"category_name": "Split",
						"deleted":       false,
						"subtransactions": []map[string]any{
							{"id": "s1", "amount": -50000, "category_name": "Groceries"},
							{"id": "s2", "amount": -150000, "category_name": "Household Goods"},
						},
					},
				},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := &Config{
		Services: []Service{{
			ID:       "ynab",
			Name:     "YNAB",
			Endpoint: ts.URL + "/v1",
			Headers:  map[string]string{"Authorization": "Bearer test-token"},
		}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	c := connectors.NewYNABConnector(srv)
	txs, truncated, err := c.ListUserTransactions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(txs) != 2 {
		t.Fatalf("want coffee + split parent, got %d", len(txs))
	}
	// Newest first: split is 2024-03-12, coffee is 2024-03-05.
	split := txs[0]
	if split.Amount != "-200.00" {
		t.Fatalf("split amount = %q (parent total must stay intact)", split.Amount)
	}
	if len(split.Category) != 2 || split.Category[0] != "Groceries" || split.Category[1] != "Household Goods" {
		t.Fatalf("split categories = %v", split.Category)
	}
	coffee := txs[1]
	if coffee.Amount != "-12.35" {
		t.Fatalf("amount = %q", coffee.Amount)
	}
	if coffee.Merchant != "Coffee Shop" {
		t.Fatalf("merchant = %q", coffee.Merchant)
	}
}

func TestGenerateSampleLibrary_TransactionsMerge(t *testing.T) {
	set := sampledata.GenerateSampleLibrary()
	if len(set.Transactions) == 0 {
		t.Fatal("no sample transactions")
	}
	merged := kinds.MergeTransactions(set.Transactions)
	// Amazon durable-ID pair must merge; coffee adjacent-day pair must merge;
	// coffee-2 near-miss stays distinct. Split Costco stays one row.
	// Inputs: 8 → outputs: 6.
	if len(merged) != 6 {
		t.Fatalf("merged = %d, want 6", len(merged))
	}
	var multi, nearMiss int
	for _, tx := range merged {
		if len(tx.Sources) > 1 {
			multi++
		}
		if tx.EntityID == "ynab-tx-coffee-2" {
			nearMiss++
		}
	}
	if multi < 2 {
		t.Fatalf("multi-source merges = %d, want >= 2", multi)
	}
	if nearMiss != 1 {
		t.Fatalf("near-miss rows = %d", nearMiss)
	}
}

func TestLibraryKindTransactions_Registered(t *testing.T) {
	k := LibraryKindByID("transactions")
	if k == nil {
		t.Fatal("transactions kind not registered")
	}
	if !k.Mentions("did we go over on groceries?") {
		t.Fatal("keywords missing grocery question")
	}
	if !k.Mentions("what did I spend on restaurants?") {
		t.Fatal("keywords missing spending question")
	}
	if k.Mentions("play some jazz") {
		t.Fatal("false positive mention")
	}
	if !strings.Contains(k.SyncRule, "amount_date_merchant") {
		t.Fatalf("sync rule = %q", k.SyncRule)
	}
}

func TestSummaryKinds(t *testing.T) {
	p := policy.Permissions{"transactions": policy.AccessSummary, "tracks": policy.AccessRead}
	got := p.SummaryKinds()
	if len(got) != 1 || got[0] != "transactions" {
		t.Fatalf("SummaryKinds = %v", got)
	}
	if (&policy.Permissions{}).SummaryKinds() != nil {
		t.Fatal("nil map should return nil")
	}
}

func TestYNABCapabilitiesAndInstructions(t *testing.T) {
	c := connectors.NewYNABConnector(&Server{})
	caps := c.Capabilities()
	if len(caps.Kinds) != 1 || caps.Kinds[0].Name != "transactions" {
		t.Fatalf("caps = %+v", caps)
	}
	if len(caps.Kinds[0].Operations) < 3 {
		t.Fatalf("ops = %d", len(caps.Kinds[0].Operations))
	}
	s := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(s, "/v1/library/transactions") {
		t.Fatalf("instructions = %q", s)
	}
	if !strings.Contains(s, "signed decimal") {
		t.Fatalf("instructions missing money note: %q", s)
	}
}

func TestYNABHandleProxy_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	c := connectors.NewYNABConnector(srv)
	req := httptest.NewRequest("GET", "/v1/services/ynab/budgets", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/budgets")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestYNABHandleProxy_Mock(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"budgets": []any{}}})
	})
	mux.HandleFunc("/v1/budgets/b1/transactions", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"transactions": []map[string]any{{
					"id": "t1", "date": "2024-03-05", "amount": -5000,
					"payee_name": "Store", "cleared": "cleared", "deleted": false,
				}},
			},
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	cfg := &Config{
		Services: []Service{{
			ID:       "ynab",
			Name:     "YNAB",
			Endpoint: ts.URL + "/v1",
			Headers:  map[string]string{"Authorization": "Bearer tok"},
		}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	c := connectors.NewYNABConnector(srv)

	req := httptest.NewRequest("GET", "/v1/services/ynab/budgets", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/budgets")
	if w.Code != http.StatusOK {
		t.Fatalf("budgets status %d body=%s", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/v1/services/ynab/budgets/b1/transactions", nil)
	w2 := httptest.NewRecorder()
	c.HandleProxy(w2, req2, "/budgets/b1/transactions")
	if w2.Code != http.StatusOK {
		t.Fatalf("tx status %d body=%s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), "Store") {
		t.Fatalf("body = %s", w2.Body.String())
	}
}

func TestTransactionFilters_CacheKey(t *testing.T) {
	f := kinds.TransactionFilters{}
	if f.CacheKeySuffix() != "" {
		t.Fatal("empty filters should have empty suffix")
	}
	f2 := kinds.TransactionFilters{After: "2024-01-01", Q: "Coffee"}
	if !strings.Contains(f2.CacheKeySuffix(), "after=2024-01-01") {
		t.Fatalf("suffix = %q", f2.CacheKeySuffix())
	}
}

func TestYNABListUserTransactions_PrefersNewest(t *testing.T) {
	// YNAB's transactions endpoint is unpaginated and may return oldest-first.
	// The library must keep the most recent window, not the first N in API order.
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"budgets": []map[string]any{{"id": "b1", "name": "B", "currency_format": map[string]any{"iso_code": "USD"}}},
			},
		})
	})
	mux.HandleFunc("/v1/budgets/b1/accounts", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"accounts": []any{}}})
	})
	mux.HandleFunc("/v1/budgets/b1/transactions", func(w http.ResponseWriter, r *http.Request) {
		txs := []map[string]any{}
		// 50 old rows first (2025), then 5 recent (2026-10).
		for i := 0; i < 50; i++ {
			txs = append(txs, map[string]any{
				"id":   "old-" + string(rune('a'+i%26)) + strconv.Itoa(i),
				"date": "2025-11-01", "amount": -1000, "payee_name": "Old", "cleared": "cleared",
			})
		}
		for i := 0; i < 5; i++ {
			txs = append(txs, map[string]any{
				"id":   "new-" + strconv.Itoa(i),
				"date": "2026-10-12", "amount": -2000, "payee_name": "Recent", "cleared": "cleared",
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"transactions": txs}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg := &Config{Services: []Service{{
		ID: "ynab", Name: "YNAB", Endpoint: ts.URL + "/v1",
		Headers: map[string]string{"Authorization": "Bearer t"},
	}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	txs, truncated, err := connectors.NewYNABConnector(srv).ListUserTransactions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("55 rows must fit without truncation")
	}
	if len(txs) != 55 {
		t.Fatalf("len = %d", len(txs))
	}
	// Newest first.
	if txs[0].Merchant != "Recent" || txs[0].PostedAt != "2026-10-12" {
		t.Fatalf("first = %+v want recent 2026-10-12", txs[0])
	}
	// All five recent rows are present.
	var recent int
	for _, tx := range txs {
		if tx.PostedAt == "2026-10-12" {
			recent++
		}
	}
	if recent != 5 {
		t.Fatalf("recent count = %d", recent)
	}
}
