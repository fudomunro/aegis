package kinds

import "testing"

func TestMergeTransactions_ProviderID(t *testing.T) {
	in := []Transaction{
		{
			PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon", EntityID: "AQB-1",
			Sources: []TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y1"}},
		},
		{
			PostedAt: "2024-03-05", Amount: "-45.67", Merchant: "Amazon.com", EntityID: "AQB-1",
			Sources: []TransactionSource{{Service: "quicken", ServiceName: "Quicken", ID: "q1"}},
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
	in := []Transaction{
		{
			PostedAt: "2024-03-06", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Sources: []TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y1"}},
		},
		{
			PostedAt: "2024-03-07", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Sources: []TransactionSource{{Service: "quicken", ServiceName: "Quicken", ID: "q1"}},
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
	in := []Transaction{
		{
			PostedAt: "2024-03-06", Amount: "-12.00", Merchant: "Blue Bottle Coffee",
			Sources: []TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y1"}},
		},
		{
			PostedAt: "2024-03-06", Amount: "-24.00", Merchant: "Blue Bottle Coffee",
			Sources: []TransactionSource{{Service: "ynab", ServiceName: "YNAB", ID: "y2"}},
		},
	}
	merged := MergeTransactions(in)
	if len(merged) != 2 {
		t.Fatalf("len = %d, want 2 (different amounts must not merge)", len(merged))
	}
}

func TestUnionTransactionCategories(t *testing.T) {
	got := unionTransactionCategories([]string{"Coffee"}, []string{"Dining", "coffee", "Travel"})
	if len(got) != 3 || got[0] != "Coffee" || got[1] != "Dining" || got[2] != "Travel" {
		t.Fatalf("got %v", got)
	}
}
