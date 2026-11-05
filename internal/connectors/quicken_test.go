package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

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
	if txs[0].Sources[0].Service != QuickenServiceID {
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
	list, err := store.List(types.TransactionFilters{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %d", len(list))
	}
	// Filter by merchant text.
	list2, err := store.List(types.TransactionFilters{Q: "amazon"}, 100)
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
	c := NewQuickenConnector(store)
	txs, _, err := c.ListUserTransactions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 1 {
		t.Fatalf("len = %d", len(txs))
	}
	if txs[0].Sources[0].Service != QuickenServiceID {
		t.Fatalf("service = %q", txs[0].Sources[0].Service)
	}
}

func TestQuickenCapabilitiesAndInstructions(t *testing.T) {
	c := NewQuickenConnector(nil)
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
	c := NewQuickenConnector(store)

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

