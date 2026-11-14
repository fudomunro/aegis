package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// GitHubConnector lists organization members and followed users as neutral
// Contact values (issue #289). Auth is a GitHub OAuth app (read:user,
// read:org); tokens stay in Aegis's encrypted store. Profile metadata only —
// Aegis does not store avatars or other media. Repo collaborators, issues/PRs
// participants, and activity are out of scope (the maturity catalog declares
// those gaps).
type GitHubConnector struct {
	host Host
}

// NewGitHubConnector creates a connector wired to the given Host.
func NewGitHubConnector(host Host) *GitHubConnector {
	return &GitHubConnector{host: host}
}

const (
	GitHubServiceID   = "github"
	GitHubServiceName = "GitHub"
	// githubPageSize is the per-request page size for list endpoints.
	githubPageSize = 100
	// githubEnrichWorkers bounds parallel profile-enrichment fetches.
	githubEnrichWorkers = 8
	// githubEnrichCap caps unique profile fetches so a huge social graph
	// cannot exhaust the rate limit; beyond it contacts carry login only.
	githubEnrichCap = 2000
)

// GitHubUpstreamBase returns the configured GitHub API base.
func (c *GitHubConnector) GitHubUpstreamBase() string {
	return c.host.UpstreamBaseFor(GitHubServiceID, "https://api.github.com")
}

// githubUser is the subset of a GitHub user object we use. The simple user
// shape (list endpoints) carries login/ID only; the full shape (GET
// /users/{login}) adds name/company/email/bio.
type githubUser struct {
	Login   string `json:"login"`
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Company string `json:"company"`
	Email   string `json:"email"`
	Bio     string `json:"bio"`
	HTMLURL string `json:"html_url"`
}

// Capabilities returns what GitHub kinds and operations this connector supports.
func (c *GitHubConnector) Capabilities() shared.ServiceCapabilities {
	return shared.ServiceCapabilities{
		Kinds: []shared.KindCapability{
			{
				Name:        "contacts",
				Description: "Organization members and followed users — list contacts (metadata only)",
				Operations: []shared.Operation{
					{
						Name:        "read_list",
						Description: "List GitHub contacts as neutral Contact values (org members + following). Permission key: contacts.",
						Method:      "GET",
						Path:        "/me/contacts",
						Response: shared.ResponseSpec{
							Description: "Neutral contacts: name, emails, handles, organization, sources[] (service, id)",
							Fields:      []string{"contacts", "total"},
						},
					},
					{
						Name:        "get_by_id",
						Description: "Get a single contact by GitHub login",
						Method:      "GET",
						Path:        "/contacts/{id}",
						Parameters: []shared.Parameter{
							{Name: "id", In: "path", Type: "string", Description: "GitHub login", Required: true},
						},
						Response: shared.ResponseSpec{
							Description: "Neutral contact with service-local id and identifiers",
							Fields:      []string{"name", "emails", "handles", "organization", "sources"},
						},
					},
				},
			},
		},
	}
}

// SystemInstructions returns the system message injected when GitHub is referenced.
func (c *GitHubConnector) SystemInstructions(baseURL string) string {
	root := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf(`When the user's request involves GitHub people, organization members, or who they know on GitHub, use Aegis's unified data API. Do not call the GitHub API directly.

GET %s/v1/library/contacts — contacts across every connected contact service (name, emails, handles, organization, sources[])

GitHub contacts cover organization members and users the account follows. sources[] records where each contact lives for sync. Contacts use neutral identity matching (email → phone → handle → name) so people can be matched to photos, messages, and events.`, root)
}

// HandleProxy processes a GitHub API request through the connector.
// Agent-facing neutral paths are /me/contacts and /contacts/{id}; GitHub
// exposes no content bytes for contacts, so those are the only surfaces.
func (c *GitHubConnector) HandleProxy(w http.ResponseWriter, r *http.Request, remainingPath string) {
	logger := slog.With("service", GitHubServiceID, "method", r.Method, "path", remainingPath)

	token := c.host.GetValidToken(GitHubServiceID, logger)
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid GitHub token — authorize at /v1/auth/"+GitHubServiceID+"/login")
		return
	}

	p := strings.Trim(remainingPath, "/")
	switch {
	case r.Method == http.MethodGet && (p == "me/contacts" || p == "contacts"):
		c.handleListContacts(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "contacts/"):
		id := strings.TrimPrefix(p, "contacts/")
		if id == "" {
			shared.WriteJSONError(w, http.StatusBadRequest, "contact id is required")
			return
		}
		c.handleGetContact(w, r, id)
	default:
		shared.WriteJSONError(w, http.StatusNotFound, "not a GitHub contacts content path")
	}
}

func (c *GitHubConnector) handleListContacts(w http.ResponseWriter, r *http.Request) {
	contacts, truncated, err := c.ListUserContacts(r.Context())
	if err != nil {
		if err == kinds.ErrUnauthorized {
			shared.WriteJSONError(w, http.StatusUnauthorized,
				"no valid GitHub token — authorize at /v1/auth/"+GitHubServiceID+"/login")
			return
		}
		slog.Warn("github contacts list failed", "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch contacts from GitHub")
		return
	}
	if contacts == nil {
		contacts = []kinds.Contact{}
	}
	shared.WriteJSON(w, kinds.ContactList{
		Contacts: contacts,
		LibraryListMeta: kinds.LibraryListMeta{
			Total:     len(contacts),
			Synced:    true,
			Truncated: truncated,
			ByService: map[string]kinds.LibraryServiceStatus{
				GitHubServiceID: {
					Status:    kinds.LibraryStatusOK,
					Count:     len(contacts),
					Truncated: truncated,
				},
			},
		},
	})
}

func (c *GitHubConnector) handleGetContact(w http.ResponseWriter, r *http.Request, login string) {
	token := c.host.GetValidToken(GitHubServiceID, slog.Default())
	if token == "" {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			"no valid GitHub token — authorize at /v1/auth/"+GitHubServiceID+"/login")
		return
	}
	user, err := c.fetchUser(r.Context(), token, login)
	if err != nil {
		slog.Warn("github user fetch failed", "login", login, "error", err)
		shared.WriteJSONError(w, http.StatusBadGateway, "failed to fetch user from GitHub")
		return
	}
	shared.WriteJSON(w, MapGitHubUser(user, ""))
}

// ListUserContacts implements ContactProvider. Fetches organization members
// and followed users, enriches each with profile detail, and maps them into
// neutral Contact values for the unified library and cross-type matching.
//
// Fail-open: a failed org or following page is logged and skipped, never
// fatal. Enrichment failures fall back to login-only contacts.
func (c *GitHubConnector) ListUserContacts(ctx context.Context) ([]kinds.Contact, bool, error) {
	token := c.host.GetValidToken(GitHubServiceID, slog.Default())
	if token == "" {
		return nil, false, kinds.ErrUnauthorized
	}
	logger := slog.With("service", GitHubServiceID)
	truncated := false

	// login -> first org name (fallback organization when profile has none).
	orgOf := map[string]string{}
	// Ordered unique logins, org members first then following.
	var logins []string
	seen := map[string]bool{}
	add := func(login string, org string) {
		if login == "" || seen[login] {
			return
		}
		seen[login] = true
		logins = append(logins, login)
		if org != "" {
			if _, ok := orgOf[login]; !ok {
				orgOf[login] = org
			}
		}
	}

	// 1. Organization members.
	orgs, orgsTrunc, err := c.listOrgs(ctx, token)
	if err != nil {
		logger.Warn("github orgs list failed; continuing with following only", "error", err)
	}
	for _, org := range orgs {
		members, mTrunc, mErr := c.listOrgMembers(ctx, token, org)
		if mErr != nil {
			logger.Warn("github org members list failed", "org", org, "error", mErr)
			continue
		}
		for _, m := range members {
			add(m.Login, org)
		}
		if mTrunc {
			truncated = true
		}
	}
	if orgsTrunc {
		truncated = true
	}

	// 2. Followed users.
	following, fTrunc, err := c.listFollowing(ctx, token)
	if err != nil {
		logger.Warn("github following list failed", "error", err)
	}
	for _, u := range following {
		add(u.Login, "")
	}
	if fTrunc {
		truncated = true
	}

	if len(logins) == 0 {
		return nil, truncated, nil
	}

	// 3. Enrich with profile detail (name, company, email). Bounded and
	// parallel; failures leave the simple login-only contact in place.
	enriched := make(map[string]githubUser, len(logins))
	enrichCount := 0
	var mu sync.Mutex
	sem := make(chan struct{}, githubEnrichWorkers)
	var wg sync.WaitGroup
	for _, login := range logins {
		if enrichCount >= githubEnrichCap {
			truncated = true
			break
		}
		enrichCount++
		wg.Add(1)
		sem <- struct{}{}
		go func(login string) {
			defer wg.Done()
			defer func() { <-sem }()
			u, uErr := c.fetchUser(ctx, token, login)
			mu.Lock()
			defer mu.Unlock()
			if uErr != nil {
				logger.Debug("github user enrich failed", "login", login, "error", uErr)
				enriched[login] = githubUser{Login: login}
				return
			}
			enriched[login] = u
		}(login)
	}
	wg.Wait()

	contacts := make([]kinds.Contact, 0, len(logins))
	for _, login := range logins {
		u, ok := enriched[login]
		if !ok {
			// Past the enrich cap — login-only contact.
			u = githubUser{Login: login}
		}
		contact := MapGitHubUser(u, orgOf[login])
		if kinds.PrimaryContactKey(contact) != "" || len(contact.Sources) > 0 {
			contacts = append(contacts, contact)
		}
	}
	return contacts, truncated, nil
}

// listOrgs returns the organizations the authenticated user belongs to.
func (c *GitHubConnector) listOrgs(ctx context.Context, token string) ([]string, bool, error) {
	var out []string
	truncated := false
	for page := 1; page <= shared.LibraryPageCap; page++ {
		var batch []struct {
			Login string `json:"login"`
		}
		next, err := c.getJSON(ctx, token, "/user/orgs?per_page="+strconv.Itoa(githubPageSize)+"&page="+strconv.Itoa(page), &batch)
		if err != nil {
			return out, truncated, err
		}
		for _, o := range batch {
			if o.Login != "" {
				out = append(out, o.Login)
			}
		}
		if !next {
			return out, truncated, nil
		}
		if page == shared.LibraryPageCap {
			truncated = true
		}
	}
	return out, truncated, nil
}

// listOrgMembers returns the members of one organization.
func (c *GitHubConnector) listOrgMembers(ctx context.Context, token, org string) ([]githubUser, bool, error) {
	var out []githubUser
	truncated := false
	for page := 1; page <= shared.LibraryPageCap; page++ {
		var batch []githubUser
		next, err := c.getJSON(ctx, token,
			"/orgs/"+url.PathEscape(org)+"/members?per_page="+strconv.Itoa(githubPageSize)+"&page="+strconv.Itoa(page),
			&batch)
		if err != nil {
			return out, truncated, err
		}
		out = append(out, batch...)
		if !next {
			return out, truncated, nil
		}
		if page == shared.LibraryPageCap {
			truncated = true
		}
	}
	return out, truncated, nil
}

// listFollowing returns the users the authenticated user follows.
func (c *GitHubConnector) listFollowing(ctx context.Context, token string) ([]githubUser, bool, error) {
	var out []githubUser
	truncated := false
	for page := 1; page <= shared.LibraryPageCap; page++ {
		var batch []githubUser
		next, err := c.getJSON(ctx, token,
			"/user/following?per_page="+strconv.Itoa(githubPageSize)+"&page="+strconv.Itoa(page),
			&batch)
		if err != nil {
			return out, truncated, err
		}
		out = append(out, batch...)
		if !next {
			return out, truncated, nil
		}
		if page == shared.LibraryPageCap {
			truncated = true
		}
	}
	return out, truncated, nil
}

// fetchUser returns the full profile for one login.
func (c *GitHubConnector) fetchUser(ctx context.Context, token, login string) (githubUser, error) {
	var u githubUser
	_, err := c.getJSON(ctx, token, "/users/"+url.PathEscape(login), &u)
	return u, err
}

// getJSON GETs a GitHub API path and decodes the JSON body into out. It
// returns hasMore=true when a Link header advertises a next page.
func (c *GitHubConnector) getJSON(ctx context.Context, token, path string, out any) (hasMore bool, err error) {
	u := strings.TrimRight(c.GitHubUpstreamBase(), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := shared.HTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("github %s returned %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, err
	}
	return linkHasNext(resp.Header.Get("Link")), nil
}

// linkHasNext reports whether a GitHub Link header advertises rel="next".
func linkHasNext(link string) bool {
	for _, part := range strings.Split(link, ",") {
		if strings.Contains(part, `rel="next"`) {
			return true
		}
	}
	return false
}

// MapGitHubUser maps a GitHub user object to a neutral Contact. orgName is
// the first organization the user was seen in, used as the Organization
// fallback when the profile has no company.
func MapGitHubUser(u githubUser, orgName string) kinds.Contact {
	name := strings.TrimSpace(u.Name)
	if name == "" {
		name = u.Login
	}
	var emails []string
	if strings.TrimSpace(u.Email) != "" {
		emails = []string{u.Email}
	}
	contact := kinds.NewContact(
		GitHubServiceID,
		GitHubServiceName,
		u.Login,
		name,
		emails,
		nil,
	)
	contact.Handles = []string{"github:" + u.Login}
	if company := strings.TrimSpace(u.Company); company != "" {
		contact.Organization = company
	} else if orgName != "" {
		contact.Organization = orgName
	}
	if u.HTMLURL != "" || u.Bio != "" {
		if contact.Extra == nil {
			contact.Extra = map[string]any{}
		}
		if u.HTMLURL != "" {
			contact.Extra["html_url"] = u.HTMLURL
		}
		if u.Bio != "" {
			contact.Extra["bio"] = u.Bio
		}
	}
	return contact
}

var (
	_ ServiceConnector      = (*GitHubConnector)(nil)
	_ kinds.ContactProvider = (*GitHubConnector)(nil)
)
