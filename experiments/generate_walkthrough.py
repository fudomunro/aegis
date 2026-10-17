#!/usr/bin/env python3
"""Regenerate experiments/index.html from walkthrough.template.html + live tree facts.

Usage (from repo root or experiments/):
    python3 experiments/generate_walkthrough.py          # rewrite experiments/index.html
    python3 experiments/generate_walkthrough.py --check  # optional local staleness check

The walkthrough lives under experiments/ so the CI docs-freshness job does not
treat it as a gated product doc (issue #160 follow-up).
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
DOCS = REPO / "experiments"
TEMPLATE = DOCS / "walkthrough.template.html"
OUTPUT = DOCS / "index.html"

# Human labels for files the file-map section should explain.
FILE_ROLES = {
    "main.go": "Thin entry: core.Run()",
    "internal/core/run.go": "Flags, env, Server wiring, connector registration, HTTP routes",
    "handler.go": "Server, AuthMiddleware, ServiceConnector, pre-request hook",
    "config.go": "Services + shared OAuth connections, matching, reload",
    "identity.go": "Tailscale Serve headers, WhoIs, MultiResolver, PeerIdentity",
    "oauth.go": "Login, callback, code exchange, token refresh, revoke",
    "tokens.go": "AES-256-GCM token store (PBKDF2), keyed by connection ID",
    "statestore.go": "CSRF OAuth state TTL map",
    "connections.go": "Connection ↔ service surface resolution helpers",
    "host.go": "Connector Host seam (tokens, upstream base, cache) — issue #15",
    "permissions.go": "Server-side permission clamp + per-request checks",
    "proxy.go": "Generic service proxy + connector delegation + cache",
    "connector_proxy.go": "Shared OAuth connector pipeline (token→cache→upstream→transform)",
    "httpclient.go": "Shared outbound HTTP client + configured upstream base",
    "library.go": "Thin per-kind library handlers + aggregate wiring",
    "librarykind.go": "Library kind registry + kindRuntime predicates + aggregate pipeline",
    "internal/types/models.go": "Neutral Playlist / Track / Artist / Album / Contact / Photo",
    "internal/types/merge.go": "Cross-service identity keys and merge algorithms",
    "internal/types/kind.go": "LibraryKind data (instructions, keywords, cache key)",
    "internal/types/policy.go": "Access levels, Permissions map, TypePolicy (#148)",
    "internal/types/pathclass.go": "Proxy path → data-type classification table",
    "internal/types/capabilities.go": "ServiceCapabilities / Operation shapes + filtering",
    "internal/types/providers.go": "Provider interfaces + ErrUnauthorized + service-name helpers",
    "internal/types/udt.go": "User-defined type schema, neutral item, merge (#15)",
    "internal/types/optin.go": "Opt-in data-type marks for UDTs / places (#15, #142)",
    "internal/core/udt.go": "UDT pack loader, field map, file_json/http_json fetch (#15)",
    "internal/core/udt_handlers.go": "UDT library aggregate handler + type-policy gate (#15)",
    "internal/core/udt_overlay.go": "user_types.json overlay: admin local-file selection (#15)",
    "internal/core/udt_admin.go": "Admin UDT status/browse/file-select API (#15)",
    "musicbrainz.go": "Optional durable IDs (MBID) for artists/tracks/albums",
    "entity_links.go": "User-confirmed cross-service entity links",
    "entity_links_handlers.go": "Admin/API handlers for links and merge review",
    "canonical.go": "Canonical-source stamping and write-policy notices",
    "mirror.go": "Playlist / liked-tracks backup copy orchestration",
    "mirror_spotify.go": "Spotify side of mirror reads/writes",
    "mirror_youtube.go": "YouTube Music side of mirror reads/writes",
    "contacts.go": "Contact identity + thin library handler",
    "photos.go": "Photo identity/merge; bytes stay on the source service",
    "metrics.go": "In-memory access ring buffer for the admin map",
    "flow.go": "Admin data-flow topology + recent access stats",
    "cache.go": "In-memory TTL cache (GET 200 only)",
    "flight.go": "Singleflight for concurrent token refresh",
    "setup.go": "Web setup wizard",
    "admin.go": "Admin dashboard shell + JSON summary endpoints",
    "internal/core/service_connections.go": "service_connections.json overlay (Immich/YNAB/Tailscale admin secrets)",
    "internal/core/tailscale_admin.go": "Tailscale Admin API client: Immich VIP step check/fix (issue #124)",
    "internal/core/immich_setup.go": "Immich setup wizard API + VIP Service guide (issues #39, #121, #124)",
    "internal/core/ynab_setup.go": "YNAB / Quicken admin setup (issue #141)",
    "web/admin.html": "Admin SPA markup (//go:embed)",
    "spotify.go": "Spotify connector (proxy, providers, transforms)",
    "youtube.go": "YouTube Music connector",
    "amazonmusic.go": "Amazon Music connector",
    "google-photos.go": "Google Photos connector (metadata only)",
    "google-contacts.go": "Google Contacts connector (service id: google)",
    "immich.go": "Immich local/foreign canonical photo connector",
}

# Preferred reading order for the file-map table (unknown files append after).
READING_ORDER = [
    "main.go",
    "internal/core/run.go",
    "internal/core/handler.go",
    "internal/core/config.go",
    "handler.go",
    "config.go",
    "identity.go",
    "host.go",
    "internal/types/models.go",
    "internal/types/merge.go",
    "internal/types/kind.go",
    "internal/types/policy.go",
    "internal/types/pathclass.go",
    "internal/types/capabilities.go",
    "internal/types/providers.go",
    "internal/types/udt.go",
    "internal/types/optin.go",
    "permissions.go",
    "proxy.go",
    "connector_proxy.go",
    "library.go",
    "librarykind.go",
    "internal/core/udt.go",
    "internal/core/udt_handlers.go",
    "internal/core/udt_overlay.go",
    "internal/core/udt_admin.go",
    "musicbrainz.go",
    "entity_links.go",
    "entity_links_handlers.go",
    "canonical.go",
    "mirror.go",
    "mirror_spotify.go",
    "mirror_youtube.go",
    "contacts.go",
    "photos.go",
    "oauth.go",
    "tokens.go",
    "connections.go",
    "statestore.go",
    "flight.go",
    "cache.go",
    "httpclient.go",
    "metrics.go",
    "flow.go",
    "setup.go",
    "admin.go",
    "internal/core/service_connections.go",
    "internal/core/tailscale_admin.go",
    "internal/core/immich_setup.go",
    "internal/core/ynab_setup.go",
    "spotify.go",
    "youtube.go",
    "amazonmusic.go",
    "google-photos.go",
    "google-contacts.go",
    "immich.go",
]


def run_git(*args: str) -> str:
    out = subprocess.check_output(["git", *args], cwd=REPO, text=True)
    return out.strip()


def run_git_quiet(*args: str) -> bool:
    try:
        subprocess.check_output(["git", *args], cwd=REPO, text=True, stderr=subprocess.DEVNULL)
    except subprocess.CalledProcessError:
        return False
    return True


def go_module() -> str:
    for line in (REPO / "go.mod").read_text(encoding="utf-8").splitlines():
        if line.startswith("module "):
            return line.split("module ", 1)[1].strip()
    return "(unknown)"


def go_sources() -> list[Path]:
    files = sorted(REPO.glob("*.go"))
    files.extend(sorted((REPO / "internal").rglob("*.go")))
    return [p for p in files if not p.name.endswith("_test.go")]


def loc_of(path: Path) -> int:
    return sum(1 for line in path.read_text(encoding="utf-8", errors="replace").splitlines() if line.strip())


def extract_connectors() -> list[str]:
    main = (REPO / "internal/core/run.go").read_text(encoding="utf-8")
    ids: list[str] = []
    # Literal keys: srv.connectors["spotify"] = ...
    ids.extend(re.findall(r'srv\.connectors\["([^"]+)"\]', main))
    # connectors.PackageID constants referenced in main registration.
    const_map = {
        "GooglePhotosServiceID": "google-photos",
        "GoogleContactsServiceID": "google",
        "ImmichServiceID": "immich",
        "GoogleCalendarServiceID": "google-calendar",
        "GoogleGmailServiceID": "google-gmail",
        "YNABServiceID": "ynab",
        "OwnTracksServiceID": "owntracks",
        "BlueskyServiceID": "bluesky",
        "SampleCalendarServiceID": "sample-calendar",
    }
    for const, value in const_map.items():
        if re.search(rf"connectors\.{const}", main) or re.search(rf"srv\.connectors\[{const}\]", main):
            ids.append(value)
    # Preserve registration order, drop dupes.
    seen: set[str] = set()
    ordered: list[str] = []
    for i in ids:
        if i not in seen:
            seen.add(i)
            ordered.append(i)
    return ordered


def extract_library_kinds() -> list[str]:
    text = (REPO / "internal/core/librarykind.go").read_text(encoding="utf-8")
    # Only the registry block IDs (first assignments after libraryKinds =).
    block = text.split("var libraryKinds", 1)[-1].split("func ", 1)[0]
    return re.findall(r'ID:\s*"([^"]+)"', block)


def file_map_rows() -> str:
    entries: list[tuple[str, int, str]] = []
    for path in go_sources():
        name = path.relative_to(REPO).as_posix()
        entries.append((name, loc_of(path), FILE_ROLES.get(name, FILE_ROLES.get(path.name, "Supporting code"))))

    admin_html = REPO / "web" / "admin.html"
    if admin_html.exists():
        entries.append(
            (
                "web/admin.html",
                loc_of(admin_html),
                FILE_ROLES["web/admin.html"],
            )
        )

    order_index = {name: i for i, name in enumerate(READING_ORDER)}
    entries.sort(key=lambda e: (order_index.get(e[0], 999), e[0]))

    rows = []
    for name, loc, role in entries:
        rows.append(
            f"      <tr><td><code>{name}</code></td>"
            f"<td class=\"mono\">{loc}</td><td>{role}</td></tr>"
        )
    return "\n".join(rows)


def render() -> tuple[str, dict[str, str]]:
    """Build the stamped HTML and the replacement map used to produce it."""
    commit = run_git("rev-parse", "--short", "HEAD")
    commit_full = run_git("rev-parse", "HEAD")
    try:
        subject = run_git("log", "-1", "--format=%s")
        commit_date = run_git("log", "-1", "--format=%cs")
        branch = run_git("rev-parse", "--abbrev-ref", "HEAD")
    except subprocess.CalledProcessError:
        subject = "(unknown)"
        commit_date = "(unknown)"
        branch = "(unknown)"

    sources = go_sources()
    source_files = len(sources)
    source_loc = sum(loc_of(p) for p in sources)
    connectors = extract_connectors()
    kinds = extract_library_kinds()
    generated_at = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")

    replacements = {
        "COMMIT": commit,
        "COMMIT_FULL": commit_full,
        "COMMIT_DATE": commit_date,
        "COMMIT_SUBJECT": subject,
        "BRANCH": branch,
        "GENERATED_AT": generated_at,
        "SOURCE_FILES": str(source_files),
        "SOURCE_LOC": f"{source_loc:,}",
        "CONNECTOR_COUNT": str(len(connectors)),
        "CONNECTOR_LIST": ", ".join(connectors),
        "CONNECTOR_LIST_HTML": ", ".join(f"<code>{c}</code>" for c in connectors),
        "KIND_COUNT": str(len(kinds)),
        "KIND_LIST": ", ".join(kinds),
        "KIND_LIST_HTML": ", ".join(f"<code>{k}</code>" for k in kinds),
        "FILE_MAP_ROWS": file_map_rows(),
        "GO_MODULE": go_module(),
    }

    html = TEMPLATE.read_text(encoding="utf-8")
    for key, value in replacements.items():
        html = html.replace("{{" + key + "}}", value)

    leftover = re.findall(r"\{\{[A-Z0-9_]+\}\}", html)
    if leftover:
        raise RuntimeError(f"unreplaced placeholders: {sorted(set(leftover))}")
    return html, replacements


def normalize_for_compare(html: str) -> str:
    """Blank version metadata so tree-fact drift is what --check reports."""
    html = re.sub(
        r'<title>Aegis — Code Walkthrough @ [^<]+</title>',
        "<title>Aegis — Code Walkthrough @ STAMP</title>",
        html,
    )
    html = re.sub(
        r'<span class="version-badge"[^>]*><span class="dot"></span>[^<]*</span>',
        '<span class="version-badge">STAMP</span>',
        html,
        count=1,
    )
    html = re.sub(
        r'<span class="version-meta">[^<]*</span>',
        '<span class="version-meta">META</span>',
        html,
        count=1,
    )
    html = re.sub(r"File map @ [0-9a-f]+", "File map @ STAMP", html)
    html = re.sub(r"<code>[0-9a-f]{7,40}</code>", "<code>STAMP</code>", html)
    html = re.sub(r"<strong>[0-9a-f]{7,40}</strong>", "<strong>STAMP</strong>", html)
    # Snapshot caption after the stamp: (branch · date · subject).
    html = re.sub(
        r'(<code>STAMP</code></strong>\s*\n\s*)\([^\n]+\)\.',
        r"\1(META).",
        html,
        count=1,
    )
    html = re.sub(r"· \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC", "· STAMP_TIME", html)
    return html


def extract_stamp(html: str) -> str:
    m = re.search(
        r'class="version-badge"[^>]*>.*?([0-9a-f]{7,40})',
        html,
    )
    if not m:
        return ""
    return m.group(1)


def is_ancestor_or_equal(stamp: str, head: str) -> bool:
    if not stamp:
        return False
    if not run_git_quiet("rev-parse", "--verify", f"{stamp}^{{commit}}"):
        return False
    return run_git_quiet("merge-base", "--is-ancestor", stamp, head)


def check() -> int:
    """Fail when experiments/index.html does not match the current tree / HEAD history."""
    if not OUTPUT.exists():
        print(f"missing {OUTPUT.relative_to(REPO)}; run generate first", file=sys.stderr)
        return 1

    try:
        expected, _ = render()
    except RuntimeError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    actual = OUTPUT.read_text(encoding="utf-8")
    if normalize_for_compare(expected) != normalize_for_compare(actual):
        print(
            "experiments/index.html is stale relative to the current tree.\n"
            "Run: python3 experiments/generate_walkthrough.py",
            file=sys.stderr,
        )
        return 1

    stamp = extract_stamp(actual)
    head = run_git("rev-parse", "HEAD")
    head_short = run_git("rev-parse", "--short", "HEAD")
    if stamp == head_short or stamp == head:
        print(f"experiments/index.html is current (stamped {stamp})")
        return 0
    if is_ancestor_or_equal(stamp, "HEAD"):
        # Merge commits move HEAD without changing tree facts; the stamp may
        # name a parent that still describes this exact source tree.
        print(
            f"experiments/index.html tree facts match HEAD; "
            f"stamped {stamp} is an ancestor of {head_short}"
        )
        return 0

    print(
        f"experiments/index.html is stamped {stamp!r}, which is not HEAD ({head_short}) "
        f"or an ancestor of HEAD.\n"
        "Run: python3 experiments/generate_walkthrough.py",
        file=sys.stderr,
    )
    return 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--check",
        action="store_true",
        help="verify experiments/index.html is current; do not write",
    )
    args = parser.parse_args(argv)

    if not TEMPLATE.exists():
        print(f"missing template: {TEMPLATE}", file=sys.stderr)
        return 1

    if args.check:
        return check()

    try:
        html, replacements = render()
    except RuntimeError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    OUTPUT.write_text(html, encoding="utf-8")
    print(f"wrote {OUTPUT.relative_to(REPO)}")
    print(f"  version  {replacements['COMMIT']} ({replacements['BRANCH']}) — {replacements['COMMIT_DATE']}")
    print(f"  subject  {replacements['COMMIT_SUBJECT']}")
    print(f"  sources  {replacements['SOURCE_FILES']} files · {replacements['SOURCE_LOC']} LOC")
    print(f"  connectors ({replacements['CONNECTOR_COUNT']}): {replacements['CONNECTOR_LIST']}")
    print(f"  library kinds ({replacements['KIND_COUNT']}): {replacements['KIND_LIST']}")
    print(f"  generated {replacements['GENERATED_AT']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
