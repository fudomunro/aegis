(function () {
  var cfg = window.AEGIS_DEMO || { baseUrl: "", personas: {} };
  var current = "shire";

  function $(id) { return document.getElementById(id); }

  function persona() {
    return cfg.personas[current] || { label: current, key: "", blurb: "" };
  }

  function baseUrl() {
    var v = ($("base-url") && $("base-url").value || cfg.baseUrl || "").trim().replace(/\/+$/, "");
    return v || "https://YOUR_DEMO_HOST";
  }

  function apiKey() {
    var v = ($("api-key") && $("api-key").value || "").trim();
    return v || persona().key || "YOUR_API_KEY";
  }

  function briefText() {
    var p = persona();
    var base = baseUrl();
    var key = apiKey();
    return [
      "You are helping with a personal-data API named Aegis.",
      "",
      "BASE_URL = " + base,
      "API_KEY  = " + key,
      "",
      "You MUST call the API with Bash (curl). Do not answer from your own",
      "knowledge of the story — only from what the API returns.",
      "",
      "First command to run now (copy exactly):",
      "",
      "  curl -sS -H \"X-Api-Key: " + key + "\" \\",
      "    \"" + base + "/v1/mail/search?sort=asc&limit=3\"",
      "",
      "Account: " + p.label + ". " + (p.blurb || ""),
      "Synthetic demo data. Read-only.",
      "",
      "Endpoints (send X-Api-Key on every request):",
      "  GET /v1/mail/search?q=&from=&to=&after=&before=&sort=asc|desc&limit=",
      "  GET /v1/library/messages",
      "  GET /v1/library/contacts",
      "  GET /v1/library/events",
      "  GET /v1/library/places",
      "  GET /v1/library/photos",
      "  GET /v1/library/tracks, /playlists, /albums, /artists",
      "  GET /v1/library/transactions",
      "  GET /v1/library/documents",
      "  GET /v1/library/documents/{service}/{id}/text",
      "",
      "Rules:",
      "- Only call this Aegis API — no other data sources.",
      "- Never invent messages or people; if the API returns empty, say so.",
      "- Read-only: do not POST/PUT/DELETE.",
      "- Cite subjects and dates when you answer.",
      "- If Bash or network is unavailable, say that plainly and stop.",
      "- If curl fails, report the status code and body exactly.",
    ].join("\n");
  }

  function curlSmoke() {
    return "curl -sS -H \"X-Api-Key: " + apiKey() + "\" \\\n  \"" + baseUrl() + "/v1/mail/search?sort=asc&limit=1\"";
  }

  function render() {
    $("persona-blurb").textContent = persona().blurb || "";
    $("brief").value = briefText();
    $("smoke").textContent = curlSmoke();
    document.querySelectorAll(".persona").forEach(function (btn) {
      btn.setAttribute("aria-pressed", btn.dataset.id === current ? "true" : "false");
    });
  }

  function copy(text, btn) {
    function done() {
      var old = btn.textContent;
      btn.textContent = "Copied";
      setTimeout(function () { btn.textContent = old; }, 1200);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () {
        window.prompt("Copy:", text);
      });
    } else {
      window.prompt("Copy:", text);
    }
  }

  document.addEventListener("DOMContentLoaded", function () {
    if ($("base-url")) $("base-url").value = cfg.baseUrl || "";
    if ($("api-key")) $("api-key").value = (cfg.personas.shire && cfg.personas.shire.key) || "";

    document.querySelectorAll(".persona").forEach(function (btn) {
      btn.addEventListener("click", function () {
        current = btn.dataset.id;
        if ($("api-key") && cfg.personas[current]) {
          $("api-key").value = cfg.personas[current].key || "";
        }
        render();
      });
    });

    if ($("base-url")) $("base-url").addEventListener("input", render);
    if ($("api-key")) $("api-key").addEventListener("input", render);

    var copyBrief = $("copy-brief");
    if (copyBrief) copyBrief.addEventListener("click", function () { copy(briefText(), copyBrief); });

    var copySmoke = $("copy-smoke");
    if (copySmoke) copySmoke.addEventListener("click", function () { copy(curlSmoke(), copySmoke); });

    render();
  });
})();
