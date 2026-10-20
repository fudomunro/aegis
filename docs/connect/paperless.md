# Paperless-ngx

Self-hosted document store that already OCRs, tags, and classifies — a natural
fit for the Immich audience. Configure a service named `paperless` with the
API base (`…/api`) and a token:

```json
{
 "id": "paperless",
 "name": "Paperless-ngx",
 "endpoint": "http://192.168.1.10:8000/api",
 "headers": {"Authorization": "Token …"},
 "canonical_for": ["documents"],
 "grant_limits": {"documents": "read"}
}
```

Use `documents: "metadata"` in a data grant (or `grant_limits`) to let agents
browse titles/tags without excerpts or full text.

