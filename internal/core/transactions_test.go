package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
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
		if got := normalizeTransactionAmount(in); got != want {
			t.Fatalf("normalizeTransactionAmount(%q) = %q, want %q", in, got, want)
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
		if got := types.FormatMilliunitsAmount(in); got != want {
			t.Fatalf("types.FormatMilliunitsAmount(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeTransactions_ProviderID(t *testing.T) {
	in := []types.Transaction{
		{
			PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon", EntityID: "AQB-1",
			Sources: []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y1"}},
		},
		{
			PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon.com", EntityID: "AQB-1",
			Sources: []types.TransactionSource{{Service: "quicken", ServiceName: "Quicken", ID: "q1"}},
		},
	}
	merged := MergeTransactions(in)
	if len(merged) != 1 {
		t.Fatalf("len = %d, want 1", len(merged))
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(merged[0].Sources))
	}
	if merged[0].Match != TransactionMatchProviderID {
		t.Fatalf("match = %q, want %q", merged[0].Match, TransactionMatchProviderID)
	}
}

func TestMergeTransactions_AmountDateMerchant(t *testing.T) {
	in := []types.Transaction{
		{
			PostedAt: "2024-03-06", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Sources: []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y1"}},
		},
		{
			PostedAt: "2024-03-07", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Sources: []types.TransactionSource{{Service: "quicken", ServiceName: "Quicken", ID: "q1"}},
		},
	}
	merged := MergeTransactions(in)
	if len(merged) != 1 {
		t.Fatalf("len = %d, want 1 (adjacent-day merge)", len(merged))
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(merged[0].Sources))
	}
	if merged[0].Match != TransactionMatchAmountDateMerchant {
		t.Fatalf("match = %q", merged[0].Match)
	}
}

func TestMergeTransactions_NearMissDoesNotMerge(t *testing.T) {
	in := []types.Transaction{
		{
			PostedAt: "2024-03-06", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Sources: []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y1"}},
		},
		{
			PostedAt: "2024-03-06", Amount: "-24.00", Merchant: "Blue Bottle Coffee",
			Sources: []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y2"}},
		},
	}
	merged := MergeTransactions(in)
	if len(merged) != 2 {
		t.Fatalf("len = %d, want 2 (different amounts must not merge)", len(merged))
	}
}

func TestBuildTransactionSummary(t *testing.T) {
	items := []types.Transaction{
		{PostedAt: "2024-03-05", Amount: "-45.67", Category: []string{"Shopping"}, Currency: "USD"},
		{PostedAt: "2024-03-06", Amount: "-12.00", Category: []string{"Coffee"}, Currency: "USD"},
		{PostedAt: "2024-03-01", Amount: "3200.00", Category: []string{"Income"}, Currency: "USD"},
	}
	sum := buildTransactionSummary(items)
	if sum == nil {
		t.Fatal("summary nil")
	}
	if sum.Count != 3 {
		t.Fatalf("count = %d", sum.Count)
	}
	if sum.TotalAmount != "3142.33" {
		t.Fatalf("total = %q", sum.TotalAmount)
	}
	if sum.ByCategory["Shopping"] != "-45.67" {
		t.Fatalf("shopping = %q", sum.ByCategory["Shopping"])
	}
	if sum.ByMonth["2024-03"] != "3142.33" {
		t.Fatalf("month = %q", sum.ByMonth["2024-03"])
	}
	if sum.Currency != "USD" {
		t.Fatalf("currency = %q", sum.Currency)
	}
}

func TestBuildTransactionSummary_MultiCategorySplit(t *testing.T) {
	// YNAB split: categories without amounts. Full total credits each category
	// so the line item "shows up" in grocery/household views.
	items := []types.Transaction{
		{PostedAt: "2024-03-12", Amount: "-200.00", Category: []string{"Groceries", "Household Goods"}, Currency: "USD"},
	}
	sum := buildTransactionSummary(items)
	if sum.ByCategory["Groceries"] != "-200.00" {
		t.Fatalf("groceries = %q", sum.ByCategory["Groceries"])
	}
	if sum.ByCategory["Household Goods"] != "-200.00" {
		t.Fatalf("household = %q", sum.ByCategory["Household Goods"])
	}
	if sum.TotalAmount != "-200.00" {
		t.Fatalf("total = %q (line-item total must stay intact)", sum.TotalAmount)
	}
}

func TestUnionTransactionCategories(t *testing.T) {
	got := unionTransactionCategories([]string{"Coffee"}, []string{"Dining", "coffee", "Travel"})
	if len(got) != 3 || got[0] != "Coffee" || got[1] != "Dining" || got[2] != "Travel" {
		t.Fatalf("got %v", got)
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

func TestRedactTransactionRead_StripsAccount(t *testing.T) {
	tx := types.Transaction{
		PostedAt: "2024-03-05",
		Amount:   "-45.67",
		Merchant: "Amazon",
		Account:  &types.TransactionAccount{Name: "Checking", Last4: "4421"},
	}
	got := RedactTransactionRead(tx)
	if got.Account != nil {
		t.Fatal("account not stripped at read level")
	}
	if got.Merchant != "Amazon" {
		t.Fatalf("merchant = %q", got.Merchant)
	}
}

func TestValidatePermissions_SummaryOnlyOnTransactions(t *testing.T) {
	if err := types.ValidatePermissions(types.Permissions{"transactions": types.AccessSummary}); err != nil {
		t.Fatalf("summary on transactions: %v", err)
	}
	if err := types.ValidatePermissions(types.Permissions{"tracks": types.AccessSummary}); err == nil {
		t.Fatal("expected summary on tracks rejected")
	}
	if err := types.ValidatePermissions(types.Permissions{"default": types.AccessSummary}); err == nil {
		t.Fatal("expected summary on default rejected")
	}
}

func TestParseQIF(t *testing.T) {
	qif := `!Type:Bank
D03/05/2024
T-45.67
PAmazon.com
LGroceries
MMilk and snacks
N1234
^
D03/06/2024
T1200.00
PPayroll
LIncome:Salary
^
`
	txs, err := ParseQIF("sample.qif", []byte(qif))
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Fatalf("len = %d, want 2", len(txs))
	}
	if txs[0].Amount != "-45.67" || txs[0].Merchant != "Amazon.com" {
		t.Fatalf("tx0 = %+v", txs[0])
	}
	if txs[0].PostedAt != "2024-03-05" {
		t.Fatalf("posted = %q", txs[0].PostedAt)
	}
	if txs[1].Amount != "1200.00" {
		t.Fatalf("tx1 amount = %q", txs[1].Amount)
	}
	if txs[0].Sources[0].Service != quickenServiceID {
		t.Fatalf("service = %q", txs[0].Sources[0].Service)
	}
}

func TestParseOFX(t *testing.T) {
	ofx := `OFXHEADER:100
DATA:OFXSGML
VERSION:102
<OFX>
<BANKMSGSRSV1>
<STMTTRNRS>
<STMTRS>
<BANKTRANLIST>
<STMTTRN>
<TRNTYPE>POS
<DTPOSTED>20240305120000
<TRNAMT>-45.67
<FITID>2024030501
<NAME>AMAZON.COM
<MEMO>Shopping
</STMTTRN>
<STMTTRN>
<TRNTYPE>DIRECTDEP
<DTPOSTED>20240301
<TRNAMT>3200.00
<FITID>2024030101
<NAME>ACME PAYROLL
</STMTTRN>
</BANKTRANLIST>
</STMTRS>
</STMTTRNRS>
</BANKMSGSRSV1>
</OFX>
`
	txs, err := ParseOFX("export.qfx", []byte(ofx))
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 2 {
		t.Fatalf("len = %d, want 2", len(txs))
	}
	if txs[0].Amount != "-45.67" {
		t.Fatalf("tx0 amount = %q", txs[0].Amount)
	}
	if txs[0].EntityID != "2024030501" {
		t.Fatalf("tx0 entity = %q", txs[0].EntityID)
	}
	if txs[0].PostedAt != "2024-03-05" {
		t.Fatalf("tx0 posted = %q", txs[0].PostedAt)
	}
}

func TestParseOFX_SGMLWithoutClose(t *testing.T) {
	ofx := `OFXHEADER:100
<OFX>
<STMTTRN>
<TRNTYPE>POS
<DTPOSTED>20240305
<TRNAMT>-9.99
<FITID>SGML-1
<NAME>COFFEE
</OFX>
`
	txs, err := ParseOFX("s.qfx", []byte(ofx))
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) == 0 {
		t.Fatal("expected SGML parse")
	}
	if txs[0].Amount != "-9.99" {
		t.Fatalf("amount = %q", txs[0].Amount)
	}
}

func TestQuickenStore_ImportAndList(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenQuickenStore(filepath.Join(dir, "quicken.db"))
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("store nil")
	}
	defer store.Close()

	qifPath := filepath.Join(dir, "export.qif")
	qif := `!Type:Bank
D03/05/2024
T-45.67
PAmazon.com
LShopping
^
D03/06/2024
T-12.00
PBlue Bottle
LCoffee
^
`
	if err := os.WriteFile(qifPath, []byte(qif), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportQuickenPath(store, qifPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 2 || res.Scanned != 2 {
		t.Fatalf("import = %+v", res)
	}
	// Re-import skips duplicates.
	res2, err := ImportQuickenPath(store, qifPath)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Skipped != 2 {
		t.Fatalf("reimport = %+v", res2)
	}
	list, err := store.List(TransactionFilters{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}
	// Filter by merchant text.
	list2, err := store.List(TransactionFilters{Q: "amazon"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list2) != 1 {
		t.Fatalf("filtered list = %d, want 1", len(list2))
	}
	n, err := store.Count()
	if err != nil || n != 2 {
		t.Fatalf("count = %d err=%v", n, err)
	}
}

func TestTransactionFilters(t *testing.T) {
	items := []types.Transaction{
		{PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon", Category: []string{"Shopping"}, Account: &types.TransactionAccount{Name: "Checking", Last4: "4421"}, Currency: "USD"},
		{PostedAt: "2024-03-10", Amount: "-12.00", Merchant: "Blue Bottle", Category: []string{"Coffee"}, Account: &types.TransactionAccount{Name: "Credit Card", Last4: "9902"}, Currency: "USD"},
	}
	if got := filterTransactions(items, TransactionFilters{After: "2024-03-06"}); len(got) != 1 {
		t.Fatalf("after filter = %d", len(got))
	}
	if got := filterTransactions(items, TransactionFilters{Before: "2024-03-06"}); len(got) != 1 {
		t.Fatalf("before filter = %d", len(got))
	}
	if got := filterTransactions(items, TransactionFilters{Account: "9902"}); len(got) != 1 {
		t.Fatalf("account filter = %d", len(got))
	}
	if got := filterTransactions(items, TransactionFilters{Q: "coffee"}); len(got) != 1 {
		t.Fatalf("q filter = %d", len(got))
	}
	if got := filterTransactions(items, TransactionFilters{Currency: "EUR"}); len(got) != 0 {
		t.Fatalf("currency filter = %d", len(got))
	}
}

type fakeTransactionProvider struct {
	txs []types.Transaction
	err error
}

func (f *fakeTransactionProvider) ListUserTransactions(ctx context.Context) ([]types.Transaction, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	return f.txs, false, nil
}

func (f *fakeTransactionProvider) Capabilities() types.ServiceCapabilities {
	return types.ServiceCapabilities{}
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
		"ynab": &fakeTransactionProvider{txs: GenerateSampleLibrary().Transactions[:3]},
		"quicken": &fakeTransactionProvider{txs: []types.Transaction{{
			PostedAt: "2024-03-06", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Account: &types.TransactionAccount{Name: "Credit Card"},
			Sources: []types.TransactionSource{{Service: "quicken", ServiceName: "Quicken", ID: "q-coffee"}},
		}}},
	}

	req := httptest.NewRequest("GET", "/v1/library/transactions", nil)
	w := httptest.NewRecorder()
	srv.HandleLibraryTransactions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var list types.TransactionList
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
	cfg.Services[0].GrantLimits = types.Permissions{"transactions": types.AccessSummary}
	req2 := httptest.NewRequest("GET", "/v1/library/transactions", nil)
	w2 := httptest.NewRecorder()
	srv.HandleLibraryTransactions(w2, req2)
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
		"ynab": &fakeTransactionProvider{txs: []types.Transaction{
			{PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon", Sources: []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "1"}}},
			{PostedAt: "2024-03-10", Amount: "-12.00", Merchant: "Blue Bottle", Sources: []types.TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "2"}}},
		}},
	}
	req := httptest.NewRequest("GET", "/v1/library/transactions?after=2024-03-08", nil)
	w := httptest.NewRecorder()
	srv.HandleLibraryTransactions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var list types.TransactionList
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
	if tx.Status != types.TransactionStatusPosted {
		t.Fatalf("status = %q", tx.Status)
	}
}

func strPtr(s string) *string { return &s }

func TestYNABListUserTransactions_Unauthorized(t *testing.T) {
	cfg := &Config{Services: []Service{{ID: "ynab", Name: "YNAB"}}}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	c := connectors.NewYNABConnector(srv)
	_, _, err := c.ListUserTransactions(context.Background())
	if err != types.ErrUnauthorized {
		t.Fatalf("err = %v, want types.ErrUnauthorized", err)
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
	set := GenerateSampleLibrary()
	if len(set.Transactions) == 0 {
		t.Fatal("no sample transactions")
	}
	merged := MergeTransactions(set.Transactions)
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

func TestDataTypeForPath_Transactions(t *testing.T) {
	for _, p := range []string{
		"/budgets", "/budgets/x/transactions", "/accounts", "/transactions",
		"/me/transactions", "/categories", "/payees", "/quicken/import",
	} {
		if got := types.DataTypeForPath(p); got != "transactions" {
			t.Fatalf("types.DataTypeForPath(%q) = %q", p, got)
		}
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

func TestQuickenConnector_ListUserTransactions(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenQuickenStore(filepath.Join(dir, "quicken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	qifPath := filepath.Join(dir, "a.qif")
	if err := os.WriteFile(qifPath, []byte("!Type:Bank\nD03/05/2024\nT-1.00\nPTest\n^\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportQuickenPath(store, qifPath); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.quicken = store
	c := NewQuickenConnector(srv, store)
	txs, _, err := c.ListUserTransactions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 {
		t.Fatalf("len = %d", len(txs))
	}
	if txs[0].Sources[0].Service != quickenServiceID {
		t.Fatalf("service = %q", txs[0].Sources[0].Service)
	}
}

func TestSummaryDataTypes(t *testing.T) {
	p := types.Permissions{"transactions": types.AccessSummary, "tracks": types.AccessRead}
	got := p.SummaryDataTypes()
	if len(got) != 1 || got[0] != "transactions" {
		t.Fatalf("SummaryDataTypes = %v", got)
	}
	if (&types.Permissions{}).SummaryDataTypes() != nil {
		t.Fatal("nil map should return nil")
	}
}

func TestRedactTransactionsForLevel(t *testing.T) {
	items := []types.Transaction{
		{Amount: "-1.00", Merchant: "A", Account: &types.TransactionAccount{Name: "X"}},
	}
	if got := redactTransactionsForLevel(items, types.AccessSummary); got != nil {
		t.Fatalf("summary should drop line items, got %v", got)
	}
	read := redactTransactionsForLevel(items, types.AccessRead)
	if len(read) != 1 || read[0].Account != nil || read[0].Merchant != "A" {
		t.Fatalf("read redaction = %+v", read)
	}
	full := redactTransactionsForLevel(items, types.AccessFull)
	if full[0].Account == nil {
		t.Fatal("full should keep account")
	}
}

func TestYNABCapabilitiesAndInstructions(t *testing.T) {
	c := connectors.NewYNABConnector(&Server{})
	caps := c.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "transactions" {
		t.Fatalf("caps = %+v", caps)
	}
	if len(caps.DataTypes[0].Operations) < 3 {
		t.Fatalf("ops = %d", len(caps.DataTypes[0].Operations))
	}
	s := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(s, "/v1/library/transactions") {
		t.Fatalf("instructions = %q", s)
	}
	if !strings.Contains(s, "signed decimal") {
		t.Fatalf("instructions missing money note: %q", s)
	}
}

func TestQuickenCapabilitiesAndInstructions(t *testing.T) {
	c := NewQuickenConnector(&Server{}, nil)
	caps := c.Capabilities()
	if len(caps.DataTypes) != 1 || caps.DataTypes[0].Name != "transactions" {
		t.Fatalf("caps = %+v", caps)
	}
	s := c.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(s, "Quicken") {
		t.Fatalf("instructions = %q", s)
	}
}

func TestQuickenHandleProxy(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenQuickenStore(filepath.Join(dir, "quicken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	qifPath := filepath.Join(dir, "a.qif")
	if err := os.WriteFile(qifPath, []byte("!Type:Bank\nD03/05/2024\nT-1.00\nPTest Store\n^\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportQuickenPath(store, qifPath); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.quicken = store
	c := NewQuickenConnector(srv, store)

	req := httptest.NewRequest("GET", "/v1/services/quicken/import/status", nil)
	w := httptest.NewRecorder()
	c.HandleProxy(w, req, "/import/status")
	if w.Code != http.StatusOK {
		t.Fatalf("status code %d body=%s", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/v1/services/quicken/transactions?q=Test", nil)
	w2 := httptest.NewRecorder()
	c.HandleProxy(w2, req2, "/transactions")
	if w2.Code != http.StatusOK {
		t.Fatalf("status code %d body=%s", w2.Code, w2.Body.String())
	}
	var list types.TransactionList
	if err := json.Unmarshal(w2.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 {
		t.Fatalf("total = %d", list.Total)
	}

	req3 := httptest.NewRequest("POST", "/v1/services/quicken/import", strings.NewReader(`{"path":"`+qifPath+`"}`))
	w3 := httptest.NewRecorder()
	c.HandleProxy(w3, req3, "/import")
	if w3.Code != http.StatusOK {
		t.Fatalf("import status %d body=%s", w3.Code, w3.Body.String())
	}

	req4 := httptest.NewRequest("GET", "/v1/services/quicken/nope", nil)
	w4 := httptest.NewRecorder()
	c.HandleProxy(w4, req4, "/nope")
	if w4.Code != http.StatusMethodNotAllowed {
		t.Fatalf("nope status %d", w4.Code)
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

func TestParseQuickenBytes_Unrecognized(t *testing.T) {
	if _, err := ParseQuickenBytes("x.bin", []byte("hello world")); err == nil {
		t.Fatal("expected unrecognized format error")
	}
}

func TestQIFDateFormats(t *testing.T) {
	if got := qifDate("03/05/2024"); got != "2024-03-05" {
		t.Fatalf("got %q", got)
	}
	if got := qifDate("3/5/24"); got != "2024-03-05" {
		t.Fatalf("got %q", got)
	}
	if got := qifDate("2024-03-05"); got != "2024-03-05" {
		t.Fatalf("got %q", got)
	}
}

func TestStrictest_Summary(t *testing.T) {
	p := types.Permissions{"transactions": types.AccessSummary}
	if got := p.Strictest(); got != types.AccessNone {
		t.Fatalf("Strictest = %q, want none when summary is tightest", got)
	}
	p2 := types.Permissions{"transactions": types.AccessSummary, "tracks": types.AccessRead}
	if got := p2.Strictest(); got != types.AccessRead {
		t.Fatalf("Strictest = %q", got)
	}
}

func TestTransactionFilters_CacheKey(t *testing.T) {
	f := TransactionFilters{}
	if f.cacheKeySuffix() != "" {
		t.Fatal("empty filters should have empty suffix")
	}
	f2 := TransactionFilters{After: "2024-01-01", Q: "Coffee"}
	if !strings.Contains(f2.cacheKeySuffix(), "after=2024-01-01") {
		t.Fatalf("suffix = %q", f2.cacheKeySuffix())
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
