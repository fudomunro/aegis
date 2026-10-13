package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// YNABConnector lists budget transactions from the YNAB API and implements
// TransactionProvider (issue #141). Auth is a Personal Access Token in service
// headers (Authorization: Bearer …) or a stored OAuth token. Amounts arrive as
// YNAB milliunits and are emitted as signed decimal strings — never floats.
// Read-only: no budget mutations are advertised.
type YNABConnector struct {
	server *Server
}

// NewYNABConnector creates a connector wired to the given server.
func NewYNABConnector(srv *Server) *YNABConnector {
	return &YNABConnector{server: srv}
}

const (
	ynabServiceID   = "ynab"
	ynabServiceName = "YNAB"
	// ynabBudgetLimit bounds how many budgets contribute transactions.
	ynabBudgetLimit = 5
	// ynabLibraryCap is the safety bound on mapped line items. YNAB's
	// transactions endpoint returns the full history in one response (no
	// page size), so we map everything then keep the newest when over cap.
	ynabLibraryCap = 5000
)

// ynabUpstreamBase returns the configured YNAB API base.
func (c *YNABConnector) ynabUpstreamBase() string {
	return c.server.upstreamBaseFor(ynabServiceID, "https://api.ynab.com/v1")
}

// Capabilities returns YNAB transaction operations reachable through the proxy.
func (c *YNABConnector) Capabilities() ServiceCapabilities {
	return ServiceCapabilities{
		DataTypes: []DataTypeCapability{
			{
				Name:        "transactions",
				Description: "YNAB budget transactions — line items with amount, payee, category, account ref (no full account numbers)",
				Operations: []Operation{
					{
						Name:        "list_budgets",
						Description: "List the user's budgets (permission key: transactions)",
						Method:      "GET",
						Path:        "/budgets",
						Response: ResponseSpec{
							Description: "Budget list",
							Fields:      []string{"data.budgets"},
						},
					},
					{
						Name:        "list_accounts",
						Description: "List accounts for one budget (display name, type, last4 — never full numbers)",
						Method:      "GET",
						Path:        "/budgets/{budget_id}/accounts",
						Parameters: []Parameter{
							{Name: "budget_id", In: "path", Type: "string", Description: "YNAB budget UUID or last-used", Required: true},
						},
						Response: ResponseSpec{
							Description: "Account list",
							Fields:      []string{"data.accounts"},
						},
					},
					{
						Name:        "list_transactions",
						Description: "List budget transactions (permission key: transactions)",
						Method:      "GET",
						Path:        "/budgets/{budget_id}/transactions",
						Parameters: []Parameter{
							{Name: "budget_id", In: "path", Type: "string", Description: "YNAB budget UUID or last-used", Required: true},
							{Name: "since_date", In: "query", Type: "string", Description: "Only transactions on/after this date (YYYY-MM-DD)"},
							{Name: "type", In: "query", Type: "string", Description: "unapproved or categorized"},
						},
						Response: ResponseSpec{
							Description: "Raw YNAB transaction list; prefer GET /v1/library/transactions for neutral values",
							Fields:      []string{"data.transactions"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the agent hint when YNAB is referenced.
func (c *YNABConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	proxy := root + "/v1/services/" + ynabServiceID
	return fmt.Sprintf(`When the user's request involves YNAB (budgets, categorized spending), you MUST use the Aegis proxy at %s instead of calling api.ynab.com directly. Aegis adds the YNAB token — never ask the user for a Personal Access Token.

For unified money questions ("did we go over on groceries?", "what did we spend on restaurants?"), prefer GET %s/v1/library/transactions which merges YNAB and any imported Quicken files. Amounts are signed decimal strings (negative = outflow). Filter with after/before/account/q. When permission is "summary", only category/month totals are available — do not request line items.
`, proxy, root)
}

// HandleProxy forwards YNAB API paths through the shared connector pipeline.
func (c *YNABConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", ynabServiceID, "method", r.Method, "path", remainingPath)
	token := c.ynabToken(logger)
	if token == "" {
		writeJSONError(w, http.StatusUnauthorized,
			"no valid YNAB token — set Authorization header on the ynab service or authorize at /v1/auth/"+ynabServiceID+"/login")
		return
	}
	upstreamBase := c.ynabUpstreamBase()
	upstreamURL := upstreamBase + remainingPath
	if encoded := r.URL.Query().Encode(); encoded != "" {
		upstreamURL += "?" + encoded
	}
	cacheKey := ynabServiceID + ":" + r.URL.RequestURI()
	if r.Method == http.MethodGet {
		if cached, ok := c.server.cache.Get(cacheKey); ok {
			logger.Debug("cache hit")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Aegis-Cache", "hit")
			io.WriteString(w, cached)
			return
		}
	}
	data, status, err := doUpstreamRequest(r.Context(), r.Method, upstreamURL, r.Body, token)
	if err != nil {
		logger.Error("ynab request failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, ynabServiceName+" API request failed")
		return
	}
	if status == http.StatusUnauthorized {
		writeJSONError(w, http.StatusUnauthorized,
			ynabServiceName+" token is invalid or expired — re-authorize at /v1/auth/"+ynabServiceID+"/login")
		return
	}
	if status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "30")
		writeJSONError(w, http.StatusTooManyRequests, ynabServiceName+" rate limit exceeded — retry later")
		return
	}
	// Prefer neutral library shape for transaction listings.
	if strings.HasSuffix(strings.TrimRight(remainingPath, "/"), "/transactions") && r.Method == http.MethodGet {
		var raw ynabTransactionsResponse
		if err := json.Unmarshal(data, &raw); err == nil {
			var accounts []ynabAccount
			// Account names may already be embedded on each row.
			list := make([]Transaction, 0, len(raw.Data.Transactions))
			for _, t := range raw.Data.Transactions {
				if t.Deleted {
					continue
				}
				list = append(list, c.neutralTransaction("", accounts, t))
			}
			result, err := json.Marshal(TransactionList{Transactions: list, Total: len(list), Synced: true})
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "response transformation failed")
				return
			}
			if status == http.StatusOK {
				c.server.cache.Set(cacheKey, string(result), 60*time.Second)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			w.Write(result)
			return
		}
	}
	if r.Method == http.MethodGet && status == http.StatusOK {
		c.server.cache.Set(cacheKey, string(data), 60*time.Second)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}

// ynabToken resolves the Authorization token from static headers or OAuth.
func (c *YNABConnector) ynabToken(logger *slog.Logger) string {
	if svc := c.server.config.GetService(ynabServiceID); svc != nil {
		for k, v := range svc.Headers {
			if strings.EqualFold(k, "Authorization") && strings.TrimSpace(v) != "" {
				v = strings.TrimSpace(v)
				if strings.HasPrefix(strings.ToLower(v), "bearer ") {
					return strings.TrimSpace(v[len("bearer "):])
				}
				return v
			}
			if strings.EqualFold(k, "X-YNAB-Token") && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return c.server.getValidToken(ynabServiceID, logger)
}

// ynabAPIGet performs a GET against the YNAB API and decodes into out.
func (c *YNABConnector) ynabAPIGet(ctx context.Context, logger *slog.Logger, path string, query url.Values, out any) error {
	token := c.ynabToken(logger)
	if token == "" {
		return ErrUnauthorized
	}
	u := c.ynabUpstreamBase() + path
	if encoded := query.Encode(); encoded != "" {
		u += "?" + encoded
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ynab %s returned %d", path, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// ynabBudget is the subset of a YNAB budget we need.
type ynabBudget struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	CurrencyFormat *struct {
		ISOCode string `json:"iso_code"`
	} `json:"currency_format"`
}

// ynabAccount is the subset of a YNAB account we need. Never store or emit
// full account numbers — YNAB gives display names / last4 style fields.
type ynabAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	// Last4 is not part of the public YNAB account shape; some imports carry
	// it in note/memo fields. Left empty unless a provider field supplies it.
	Last4     string `json:"-"`
	Closed    bool   `json:"closed"`
	OnBudget  bool   `json:"on_budget"`
	Debt      bool   `json:"debt"`
}

// ynabTransaction is the subset of a YNAB transaction we map to neutral form.
type ynabTransaction struct {
	ID                    string  `json:"id"`
	Date                  string  `json:"date"`
	Amount                int64   `json:"amount"`
	Memo                  *string `json:"memo"`
	PayeeID               *string `json:"payee_id"`
	PayeeName             *string `json:"payee_name"`
	CategoryID            *string `json:"category_id"`
	CategoryName          *string `json:"category_name"`
	AccountID             string  `json:"account_id"`
	AccountName           string  `json:"account_name"`
	Cleared               string  `json:"cleared"`
	Approved              bool    `json:"approved"`
	Deleted               bool    `json:"deleted"`
	ImportID              *string `json:"import_id"`
	TransferAccountID     *string `json:"transfer_account_id"`
	TransferTransactionID *string `json:"transfer_transaction_id"`
	MatchedTransactionID  *string `json:"matched_transaction_id"`
	// Subtransactions are YNAB splits. We take their categories but not their
	// amounts — the parent total stays intact and agents dig into YNAB for
	// exact per-category splits.
	Subtransactions []ynabSubtransaction `json:"subtransactions"`
}

// ynabSubtransaction is one YNAB split line (categories only in Aegis).
type ynabSubtransaction struct {
	ID           string  `json:"id"`
	Amount       int64   `json:"amount"`
	CategoryID   *string `json:"category_id"`
	CategoryName *string `json:"category_name"`
	PayeeName    *string `json:"payee_name"`
	Memo         *string `json:"memo"`
}

// ynabCategories collects parent + split categories, skipping YNAB's
// placeholder "Split" label. Order follows parent then splits.
func ynabCategories(t ynabTransaction) []string {
	seen := map[string]bool{}
	var out []string
	add := func(raw *string) {
		if raw == nil {
			return
		}
		s := strings.TrimSpace(*raw)
		if s == "" || strings.EqualFold(s, "Split") {
			return
		}
		k := strings.ToLower(s)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, s)
	}
	add(t.CategoryName)
	for _, sub := range t.Subtransactions {
		add(sub.CategoryName)
	}
	return out
}

type ynabBudgetsResponse struct {
	Data struct {
		Budgets []ynabBudget `json:"budgets"`
	} `json:"data"`
}

type ynabAccountsResponse struct {
	Data struct {
		Accounts []ynabAccount `json:"accounts"`
	} `json:"data"`
}

type ynabTransactionsResponse struct {
	Data struct {
		Transactions []ynabTransaction `json:"transactions"`
		ServerKnowledge int64          `json:"server_knowledge"`
	} `json:"data"`
}

// ynabAccountByName maps account id → neutral account ref.
func ynabAccountRef(accounts []ynabAccount, id, fallbackName string) *TransactionAccount {
	for _, a := range accounts {
		if a.ID == id {
			return &TransactionAccount{
				Name:  a.Name,
				Type:  a.Type,
				Last4: a.Last4,
			}
		}
	}
	name := strings.TrimSpace(fallbackName)
	if name == "" {
		return nil
	}
	return &TransactionAccount{Name: name}
}

// neutralTransaction maps one YNAB row to the neutral model.
func (c *YNABConnector) neutralTransaction(budgetID string, accounts []ynabAccount, t ynabTransaction) Transaction {
	return c.neutralTransactionCurrency(budgetID, accounts, t, "USD")
}

// neutralTransactionCurrency maps one YNAB row using a budget currency code.
func (c *YNABConnector) neutralTransactionCurrency(budgetID string, accounts []ynabAccount, t ynabTransaction, currency string) Transaction {
	amount := FormatMilliunitsAmount(t.Amount)
	status := TransactionStatusPosted
	if strings.EqualFold(t.Cleared, "uncleared") {
		status = TransactionStatusPending
	}
	merchant := ""
	if t.PayeeName != nil {
		merchant = strings.TrimSpace(*t.PayeeName)
	}
	// Splits contribute categories only — parent amount stays intact.
	categories := ynabCategories(t)
	memo := ""
	if t.Memo != nil {
		memo = strings.TrimSpace(*t.Memo)
	}
	entityID := ""
	if t.ImportID != nil {
		entityID = strings.TrimSpace(*t.ImportID)
	}
	if entityID == "" {
		entityID = strings.TrimSpace(t.ID)
	}
	posted := t.Date
	if day := normalizeTransactionDate(posted); day != "" {
		posted = day
	}
	if currency == "" {
		currency = "USD"
	}
	extra := map[string]any{
		"budget_id": budgetID,
		"cleared":   t.Cleared,
		"approved":  t.Approved,
	}
	if t.ImportID != nil && strings.TrimSpace(*t.ImportID) != "" {
		extra["import_id"] = strings.TrimSpace(*t.ImportID)
	}
	if t.TransferAccountID != nil && strings.TrimSpace(*t.TransferAccountID) != "" {
		extra["transfer_account_id"] = strings.TrimSpace(*t.TransferAccountID)
	}
	description := merchant
	if description == "" {
		description = memo
	}
	deep := fmt.Sprintf("https://app.ynab.com/budgets/%s/transactions/%s", url.PathEscape(budgetID), url.PathEscape(t.ID))
	out := newTransaction(ynabServiceID, ynabServiceName, t.ID, Transaction{
		PostedAt:    posted,
		Amount:      amount,
		Currency:    currency,
		Description: description,
		Merchant:    merchant,
		Category:    categories,
		Memo:        memo,
		Account:     ynabAccountRef(accounts, t.AccountID, t.AccountName),
		Status:      status,
		EntityID:    entityID,
		Extra:       extra,
	})
	if len(out.Sources) == 1 {
		out.Sources[0].URL = deep
		out.Sources[0].AccountID = t.AccountID
		out.Sources[0].Extra = extra
	}
	return out
}

// ListUserTransactions fetches YNAB transactions across the user's budgets.
func (c *YNABConnector) ListUserTransactions(ctx context.Context) (transactions []Transaction, truncated bool, err error) {
	logger := slog.With("service", ynabServiceID)
	token := c.ynabToken(logger)
	if token == "" {
		return nil, false, ErrUnauthorized
	}

	var budgetsResp ynabBudgetsResponse
	if err := c.ynabAPIGet(ctx, logger, "/budgets", nil, &budgetsResp); err != nil {
		if err == ErrUnauthorized {
			return nil, false, err
		}
		return nil, false, err
	}
	budgets := budgetsResp.Data.Budgets
	if len(budgets) == 0 {
		return nil, false, nil
	}
	if len(budgets) > ynabBudgetLimit {
		truncated = true
		budgets = budgets[:ynabBudgetLimit]
	}

	// Collect every mapped row from every budget first. The YNAB
	// transactions endpoint is unpaginated — filtering to "first N" in API
	// order dropped recent charges whenever the payload was oldest-first.
	var all []Transaction
	for _, b := range budgets {
		currency := "USD"
		if b.CurrencyFormat != nil && strings.TrimSpace(b.CurrencyFormat.ISOCode) != "" {
			currency = strings.TrimSpace(b.CurrencyFormat.ISOCode)
		}
		var accountsResp ynabAccountsResponse
		accounts := []ynabAccount{}
		if err := c.ynabAPIGet(ctx, logger, "/budgets/"+url.PathEscape(b.ID)+"/accounts", nil, &accountsResp); err != nil && err != ErrUnauthorized {
			logger.Warn("ynab accounts fetch failed", "budget", b.ID, "error", err)
		} else if err == ErrUnauthorized {
			return nil, false, err
		} else {
			accounts = accountsResp.Data.Accounts
		}

		var txResp ynabTransactionsResponse
		if err := c.ynabAPIGet(ctx, logger, "/budgets/"+url.PathEscape(b.ID)+"/transactions", nil, &txResp); err != nil {
			if err == ErrUnauthorized {
				return nil, false, err
			}
			logger.Warn("ynab transactions fetch failed", "budget", b.ID, "error", err)
			continue
		}
		for _, t := range txResp.Data.Transactions {
			if t.Deleted {
				continue
			}
			all = append(all, c.neutralTransactionCurrency(b.ID, accounts, t, currency))
		}
	}

	// Newest first, then keep the most recent window.
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].PostedAt > all[j].PostedAt
	})
	if len(all) > ynabLibraryCap {
		truncated = true
		all = all[:ynabLibraryCap]
	}
	return all, truncated, nil
}

// Ensure the connector implements the required interfaces at compile time.
var (
	_ ServiceConnector   = (*YNABConnector)(nil)
	_ TransactionProvider = (*YNABConnector)(nil)
)
