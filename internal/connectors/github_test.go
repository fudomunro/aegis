package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
)

// githubMockAPI serves the GitHub REST endpoints the connector calls.
type githubMockAPI struct {
	orgs       []string
	members    map[string][]githubUser // org -> members
	following  []githubUser
	profiles   map[string]githubUser // login -> full profile
	failOrgs   bool
	failFollow bool
}

func (m *githubMockAPI) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "github") {
			t := "missing Accept header"
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"message": t})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path
		switch {
		case p == "/user/orgs":
			if m.failOrgs {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			// Return all orgs in one page (no Link header).
			out := make([]map[string]string, 0, len(m.orgs))
			for _, o := range m.orgs {
				out = append(out, map[string]string{"login": o})
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(p, "/orgs/") && strings.HasSuffix(p, "/members"):
			org := strings.TrimSuffix(strings.TrimPrefix(p, "/orgs/"), "/members")
			members := m.members[org]
			out := make([]map[string]any, 0, len(members))
			for _, u := range members {
				out = append(out, map[string]any{"login": u.Login, "id": u.ID})
			}
			json.NewEncoder(w).Encode(out)
		case p == "/user/following":
			if m.failFollow {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			out := make([]map[string]any, 0, len(m.following))
			for _, u := range m.following {
				out = append(out, map[string]any{"login": u.Login, "id": u.ID})
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(p, "/users/"):
			login := strings.TrimPrefix(p, "/users/")
			u, ok := m.profiles[login]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(u)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newGitHubTestHost(endpoint string) *testHost {
	host := newTestHostWithService(GitHubServiceID, endpoint, "https://aegis.example.com")
	host.setToken(GitHubServiceID, "test-token")
	return host
}

func TestGitHubConnector_ListUserContacts_OrgMembersAndFollowing(t *testing.T) {
	mock := &githubMockAPI{
		orgs: []string{"acme", "widgets"},
		members: map[string][]githubUser{
			"acme":   {{Login: "ada", ID: 1}, {Login: "grace", ID: 2}},
			"widgets": {{Login: "grace", ID: 2}, {Login: "alan", ID: 3}},
		},
		following: []githubUser{{Login: "linus", ID: 4}, {Login: "ada", ID: 1}},
		profiles: map[string]githubUser{
			"ada":   {Login: "ada", ID: 1, Name: "Ada Lovelace", Email: "ada@example.com", Company: "Analytical Engines", HTMLURL: "https://github.com/ada"},
			"grace": {Login: "grace", ID: 2, Name: "Grace Hopper", Company: "Navy", HTMLURL: "https://github.com/grace"},
			"alan":  {Login: "alan", ID: 3, HTMLURL: "https://github.com/alan"},
			"linus": {Login: "linus", ID: 4, Name: "Linus Torvalds", Bio: "Creator of Linux", HTMLURL: "https://github.com/linus"},
		},
	}
	upstream := httptest.NewServer(mock.handler())
	defer upstream.Close()

	conn := NewGitHubConnector(newGitHubTestHost(upstream.URL))
	contacts, truncated, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if truncated {
		t.Fatal("unexpected truncated")
	}
	if len(contacts) != 4 {
		t.Fatalf("got %d contacts, want 4: %+v", len(contacts), contacts)
	}

	byName := map[string]kinds.Contact{}
	for _, c := range contacts {
		byName[c.Name] = c
	}

	ada, ok := byName["Ada Lovelace"]
	if !ok {
		t.Fatalf("no Ada Lovelace in %+v", byName)
	}
	if len(ada.Emails) != 1 || ada.Emails[0] != "ada@example.com" {
		t.Errorf("ada emails = %v", ada.Emails)
	}
	if ada.Organization != "Analytical Engines" {
		t.Errorf("ada organization = %q", ada.Organization)
	}
	if len(ada.Handles) != 1 || ada.Handles[0] != "github:ada" {
		t.Errorf("ada handles = %v", ada.Handles)
	}
	if len(ada.Sources) != 1 || ada.Sources[0].Service != GitHubServiceID {
		t.Errorf("ada sources = %+v", ada.Sources)
	}
	if ada.Sources[0].ID != "ada" {
		t.Errorf("ada source id = %q", ada.Sources[0].ID)
	}

	// Grace is in two orgs; company wins over org fallback.
	grace := byName["Grace Hopper"]
	if grace.Organization != "Navy" {
		t.Errorf("grace organization = %q", grace.Organization)
	}

	// Alan has no profile name — falls back to login, org from "widgets".
	alan, ok := byName["alan"]
	if !ok {
		t.Fatalf("no alan in %+v", byName)
	}
	if alan.Organization != "widgets" {
		t.Errorf("alan organization = %q", alan.Organization)
	}
	if len(alan.Handles) != 1 || alan.Handles[0] != "github:alan" {
		t.Errorf("alan handles = %v", alan.Handles)
	}

	linus := byName["Linus Torvalds"]
	if linus.Organization != "" {
		t.Errorf("linus organization = %q", linus.Organization)
	}
	if linus.Extra["bio"] != "Creator of Linux" {
		t.Errorf("linus bio = %v", linus.Extra["bio"])
	}
}

func TestGitHubConnector_ListUserContacts_DedupesAcrossSources(t *testing.T) {
	mock := &githubMockAPI{
		orgs:    []string{"acme"},
		members: map[string][]githubUser{"acme": {{Login: "ada", ID: 1}}},
		following: []githubUser{{Login: "ada", ID: 1}},
		profiles: map[string]githubUser{
			"ada": {Login: "ada", ID: 1, Name: "Ada Lovelace"},
		},
	}
	upstream := httptest.NewServer(mock.handler())
	defer upstream.Close()

	conn := NewGitHubConnector(newGitHubTestHost(upstream.URL))
	contacts, _, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1 (deduped)", len(contacts))
	}
	if contacts[0].Name != "Ada Lovelace" {
		t.Errorf("name = %q", contacts[0].Name)
	}
}

func TestGitHubConnector_ListUserContacts_FailOpen(t *testing.T) {
	mock := &githubMockAPI{
		orgs:      []string{"acme"},
		members:   map[string][]githubUser{"acme": {{Login: "ada", ID: 1}}},
		following: []githubUser{{Login: "linus", ID: 4}},
		profiles: map[string]githubUser{
			"ada":   {Login: "ada", ID: 1, Name: "Ada Lovelace"},
			"linus": {Login: "linus", ID: 4, Name: "Linus Torvalds"},
		},
		failOrgs: true,
	}
	upstream := httptest.NewServer(mock.handler())
	defer upstream.Close()

	conn := NewGitHubConnector(newGitHubTestHost(upstream.URL))
	contacts, _, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	// Orgs failed but following still yields a contact.
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1", len(contacts))
	}
	if contacts[0].Name != "Linus Torvalds" {
		t.Errorf("name = %q", contacts[0].Name)
	}
}

func TestGitHubConnector_ListUserContacts_NoToken(t *testing.T) {
	host := newTestHostWithService(GitHubServiceID, "http://unused", "https://aegis.example.com")
	conn := NewGitHubConnector(host)
	_, _, err := conn.ListUserContacts(context.Background())
	if err != kinds.ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestGitHubConnector_ListUserContacts_EnrichFallback(t *testing.T) {
	// Profile fetch 404s — contact still appears with login as name.
	mock := &githubMockAPI{
		orgs:     []string{"acme"},
		members:  map[string][]githubUser{"acme": {{Login: "ghost", ID: 9}}},
		profiles: map[string]githubUser{}, // /users/ghost → 404
	}
	upstream := httptest.NewServer(mock.handler())
	defer upstream.Close()

	conn := NewGitHubConnector(newGitHubTestHost(upstream.URL))
	contacts, _, err := conn.ListUserContacts(context.Background())
	if err != nil {
		t.Fatalf("ListUserContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1", len(contacts))
	}
	if contacts[0].Name != "ghost" {
		t.Errorf("name = %q, want login fallback", contacts[0].Name)
	}
	if contacts[0].Organization != "acme" {
		t.Errorf("organization = %q, want acme", contacts[0].Organization)
	}
}

func TestGitHubConnector_HandleProxy_List(t *testing.T) {
	mock := &githubMockAPI{
		orgs:     []string{"acme"},
		members:  map[string][]githubUser{"acme": {{Login: "ada", ID: 1}}},
		profiles: map[string]githubUser{"ada": {Login: "ada", ID: 1, Name: "Ada Lovelace"}},
	}
	upstream := httptest.NewServer(mock.handler())
	defer upstream.Close()

	conn := NewGitHubConnector(newGitHubTestHost(upstream.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/me/contacts", nil)
	conn.HandleProxy(w, req, "/me/contacts")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var list kinds.ContactList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if list.Total != 1 {
		t.Fatalf("total = %d, want 1", list.Total)
	}
	if list.Contacts[0].Name != "Ada Lovelace" {
		t.Errorf("name = %q", list.Contacts[0].Name)
	}
}

func TestGitHubConnector_HandleProxy_GetByID(t *testing.T) {
	mock := &githubMockAPI{
		profiles: map[string]githubUser{
			"ada": {Login: "ada", ID: 1, Name: "Ada Lovelace", Email: "ada@example.com"},
		},
	}
	upstream := httptest.NewServer(mock.handler())
	defer upstream.Close()

	conn := NewGitHubConnector(newGitHubTestHost(upstream.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/contacts/ada", nil)
	conn.HandleProxy(w, req, "/contacts/ada")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var contact kinds.Contact
	if err := json.Unmarshal(w.Body.Bytes(), &contact); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if contact.Name != "Ada Lovelace" {
		t.Errorf("name = %q", contact.Name)
	}
	if contact.Handles[0] != "github:ada" {
		t.Errorf("handles = %v", contact.Handles)
	}
}

func TestGitHubConnector_HandleProxy_NoToken(t *testing.T) {
	host := newTestHostWithService(GitHubServiceID, "http://unused", "https://aegis.example.com")
	conn := NewGitHubConnector(host)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/me/contacts", nil)
	conn.HandleProxy(w, req, "/me/contacts")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestGitHubConnector_HandleProxy_NotFound(t *testing.T) {
	mock := &githubMockAPI{}
	upstream := httptest.NewServer(mock.handler())
	defer upstream.Close()

	conn := NewGitHubConnector(newGitHubTestHost(upstream.URL))
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/repos/x", nil)
	conn.HandleProxy(w, req, "/repos/x")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestMapGitHubUser(t *testing.T) {
	c := MapGitHubUser(githubUser{
		Login:   "octocat",
		Name:    "The Octocat",
		Email:   "octo@example.com",
		Company: "GitHub",
		HTMLURL: "https://github.com/octocat",
		Bio:     "Mascot",
	}, "")
	if c.Name != "The Octocat" {
		t.Errorf("name = %q", c.Name)
	}
	if c.Organization != "GitHub" {
		t.Errorf("organization = %q", c.Organization)
	}
	if c.Handles[0] != "github:octocat" {
		t.Errorf("handles = %v", c.Handles)
	}
	if c.Extra["html_url"] != "https://github.com/octocat" {
		t.Errorf("extra = %v", c.Extra)
	}

	// No name, no company — login + org fallback.
	c2 := MapGitHubUser(githubUser{Login: "ghost"}, "acme")
	if c2.Name != "ghost" {
		t.Errorf("name = %q", c2.Name)
	}
	if c2.Organization != "acme" {
		t.Errorf("organization = %q", c2.Organization)
	}
}

func TestLinkHasNext(t *testing.T) {
	cases := []struct {
		link string
		want bool
	}{
		{"", false},
		{`<https://api.github.com/user/orgs?page=2>; rel="next"`, true},
		{`<https://api.github.com/user/orgs?page=2>; rel="next", <https://api.github.com/user/orgs?page=5>; rel="last"`, true},
		{`<https://api.github.com/user/orgs?page=5>; rel="last"`, false},
	}
	for _, tc := range cases {
		if got := linkHasNext(tc.link); got != tc.want {
			t.Errorf("linkHasNext(%q) = %v, want %v", tc.link, got, tc.want)
		}
	}
}

func TestGitHubConnector_CapabilitiesAndInstructions(t *testing.T) {
	conn := NewGitHubConnector(newGitHubTestHost("http://unused"))
	caps := conn.Capabilities()
	if len(caps.Kinds) != 1 || caps.Kinds[0].Name != "contacts" {
		t.Fatalf("capabilities = %+v", caps)
	}
	inst := conn.SystemInstructions("https://aegis.example.com")
	if !strings.Contains(inst, "/v1/library/contacts") {
		t.Errorf("instructions missing library path: %s", inst)
	}
	if !strings.Contains(inst, "GitHub") {
		t.Errorf("instructions missing GitHub: %s", inst)
	}
}
