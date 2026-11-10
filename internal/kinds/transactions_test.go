package kinds

import (
	"testing"
)

func TestBuildTransactionSummary(t *testing.T) {
	items := []Transaction{
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
	items := []Transaction{
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

func TestRedactTransactionRead_StripsAccount(t *testing.T) {
	tx := Transaction{
		PostedAt: "2024-03-05",
		Amount:   "-45.67",
		Merchant: "Amazon",
		Account:  &TransactionAccount{Name: "Checking", Last4: "4421"},
	}
	got := redactTransactionRead(tx)
	if got.Account != nil {
		t.Fatal("account not stripped at read level")
	}
	if got.Merchant != "Amazon" {
		t.Fatalf("merchant = %q", got.Merchant)
	}
}

func TestFilterTransactions(t *testing.T) {
	items := []Transaction{
		{PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon", Category: []string{"Shopping"}, Account: &TransactionAccount{Name: "Checking", Last4: "4421"}, Currency: "USD"},
		{PostedAt: "2024-03-10", Amount: "-12.00", Merchant: "Blue Bottle", Category: []string{"Coffee"}, Account: &TransactionAccount{Name: "Credit Card", Last4: "9902"}, Currency: "USD"},
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

func TestRedactTransactionsRead(t *testing.T) {
	items := []Transaction{
		{Amount: "-1.00", Merchant: "A", Account: &TransactionAccount{Name: "X"}},
	}
	read := redactTransactionsRead(items)
	if len(read) != 1 || read[0].Account != nil || read[0].Merchant != "A" {
		t.Fatalf("read redaction = %+v", read)
	}
}
