# GitHub

1. Go to [GitHub Developer Settings → OAuth Apps](https://github.com/settings/developers)
2. Click **New OAuth App** (or register a GitHub App with user authorization)
3. Set the authorization callback URL to `https://your-aegis/v1/auth/github/callback`
4. Copy the Client ID into `aegis.json` and the Client Secret into
   `GITHUB_CLIENT_SECRET` (see `aegis.example.json`)
5. Authorize at `/v1/auth/github/login`

Scopes: `read:user` (profile names, company, public email) and `read:org`
(organization membership). Tokens live in Aegis's encrypted store; agents never
see them.

The connector lists **organization members** (from orgs the account belongs to)
and **followed users** via the GitHub REST API, and maps them to neutral
`Contact` values. `login` is the service-local ID and appears as
`handles: ["github:login"]`. `name` falls back to the login when the profile
has no display name; `company` becomes `organization` (falling back to the
first org the user was seen in). Public profile email becomes `emails`.

GitHub exposes no phone numbers, so `phones` is always empty. Avatars and
other media are never stored — contact metadata only.

Repo collaborators, issues/PRs participants, and activity are out of scope
for this connection; the maturity catalog declares those gaps.

Contacts feed the unified `GET /v1/library/contacts` alongside Google
Contacts, CardDAV, and LinkedIn imports, and participate in cross-type
identity matching (email → handle → name) so GitHub people can be matched to
photos, messages, and events.
