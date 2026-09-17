package main

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
)

// HandleSetup serves the setup wizard page.
// GET /v1/setup
func (s *Server) HandleSetup(w http.ResponseWriter, r *http.Request) {
	tokens := s.tokens.Status()

	var serviceCards string
	for _, svc := range s.config.Services {
		status := tokens[s.config.connectionIDForService(svc.ID)]
		card := s.renderServiceCard(svc, status)
		serviceCards += card
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, setupHTML, serviceCards)
}

func (s *Server) renderServiceCard(svc Service, status TokenStatus) string {
	var statusBadge, actionBtn string

	name := html.EscapeString(svc.Name)
	idPath := url.PathEscape(svc.ID)

	hasOAuth := svc.Connection != ""
	if !hasOAuth {
		statusBadge = `<span class="badge badge-gray">No OAuth</span>`
		actionBtn = `<p class="dim">Add a <code>connection</code> reference to this service's config to enable authorization.</p>`
	} else if status.HasToken && !status.Expired {
		statusBadge = fmt.Sprintf(`<span class="badge badge-green">Connected</span> <span class="dim">expires in %s</span>`, html.EscapeString(status.ExpiresIn))
		actionBtn = fmt.Sprintf(`<a class="btn btn-outline" href="/v1/auth/%s/login">Reconnect</a>`, idPath)
	} else if status.HasToken && status.Expired {
		if status.HasRefresh {
			statusBadge = `<span class="badge badge-orange">Expired (will auto-refresh)</span>`
		} else {
			statusBadge = `<span class="badge badge-red">Expired</span>`
		}
		actionBtn = fmt.Sprintf(`<a class="btn btn-primary" href="/v1/auth/%s/login">Reconnect</a>`, idPath)
	} else {
		statusBadge = `<span class="badge badge-orange">Not Connected</span>`
		actionBtn = fmt.Sprintf(`<a class="btn btn-primary" href="/v1/auth/%s/login">Connect</a>`, idPath)
	}

	scopes := s.config.requiredScopes(svc.ID)
	var scopesInfo string
	if len(scopes) > 0 {
		escaped := make([]string, len(scopes))
		for i, sc := range scopes {
			escaped[i] = html.EscapeString(sc)
		}
		scopesInfo = fmt.Sprintf(`<p class="dim">Scopes: <code>%s</code></p>`, strings.Join(escaped, "</code>, <code>"))
	}
	if svc.Connection != "" {
		scopesInfo += fmt.Sprintf(`<p class="dim">Connection: <code>%s</code></p>`, html.EscapeString(svc.Connection))
	}

	return fmt.Sprintf(`
<div class="card">
  <div class="card-header">
    <h3>%s</h3>
    %s
  </div>
  <p class="dim">%s</p>
  %s
  <div class="card-actions">%s</div>
</div>`, name, statusBadge, html.EscapeString(svc.Description), scopesInfo, actionBtn)
}

const setupHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Guardian — Setup</title>
<style>
  :root {
    --bg: #0d1117; --surface: #161b22; --border: #30363d;
    --text: #e6edf3; --dim: #8b949e; --accent: #58a6ff;
    --green: #3fb950; --orange: #d29922; --red: #f85149;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body { font-family: system-ui, sans-serif; background: var(--bg); color: var(--text); max-width: 700px; margin: 0 auto; padding: 2rem 1.5rem; }
  h1 { font-size: 2rem; margin-bottom: 0.5rem; }
  h2 { font-size: 1.3rem; margin: 2rem 0 1rem; color: var(--accent); }
  h3 { font-size: 1.1rem; margin: 0; }
  .dim { color: var(--dim); font-size: 0.9rem; margin: 0.3rem 0; }
  code { background: var(--surface); padding: 0.1rem 0.4rem; border-radius: 3px; font-size: 0.85rem; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: 8px; padding: 1.25rem; margin: 1rem 0; }
  .card-header { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 0.5rem; }
  .card-actions { margin-top: 1rem; }
  .badge { padding: 0.15rem 0.6rem; border-radius: 12px; font-size: 0.75rem; font-weight: 600; }
  .badge-green { background: rgba(63,185,80,0.15); color: var(--green); }
  .badge-orange { background: rgba(210,153,34,0.15); color: var(--orange); }
  .badge-red { background: rgba(248,81,73,0.15); color: var(--red); }
  .badge-gray { background: var(--border); color: var(--dim); }
  .btn { display: inline-block; padding: 0.5rem 1.25rem; border-radius: 6px; font-weight: 600; text-decoration: none; font-size: 0.9rem; }
  .btn-primary { background: var(--accent); color: #000; }
  .btn-primary:hover { opacity: 0.9; }
  .btn-outline { border: 1px solid var(--border); color: var(--text); }
  .btn-outline:hover { border-color: var(--accent); }
  .intro { color: var(--dim); margin-bottom: 1.5rem; line-height: 1.6; }
</style>
</head>
<body>
<h1>Guardian Setup</h1>
<p class="intro">Connect your music services so Guardian can mediate agent access. Each service uses OAuth — you'll authorize Guardian, and agents will access data through Guardian's proxy without ever seeing your credentials.</p>

<h2>Services</h2>
%s

<h2>How it works</h2>
<div class="card">
  <p class="dim">1. Click <strong>Connect</strong> to authorize Guardian with a service.</p>
  <p class="dim">2. You'll be redirected to the service to approve access.</p>
  <p class="dim">3. Guardian stores the token securely (encrypted on disk).</p>
  <p class="dim">4. When agents reference the service, Guardian injects proxy instructions into the LLM request.</p>
  <p class="dim">5. Agents call Guardian's proxy, which adds your OAuth token and forwards to the real API.</p>
  <p class="dim">6. Responses are cached for faster follow-up requests.</p>
</div>
</body>
</html>`
