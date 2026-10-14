package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// gmailArchiveSyncer implements MailArchiveSyncer on top of the Gmail
// connector (issue #15: connector lives in internal/connectors; the local
// mail archive store stays in core).
type gmailArchiveSyncer struct {
	*connectors.GoogleGmailConnector
	host connectors.Host
}

// MailSyncOptions bounds one ingest run so a huge mailbox cannot stall a request.
type MailSyncOptions struct {
	// MaxMessages is the hydrate/store cap for this call (default 200).
	MaxMessages int
	// Query is the provider list query. Empty uses mailSyncDefaultQuery.
	Query string
	// ResetCursor discards the saved page token and starts over.
	ResetCursor bool
}

// MailSyncResult reports one bounded archive ingest run. Callers repeat
// POST /v1/mail/sync until Done is true.
type MailSyncResult struct {
	Service    string `json:"service"`
	Listed     int    `json:"listed"`
	Stored     int    `json:"stored"`
	Skipped    int    `json:"skipped,omitempty"`
	Failed     int    `json:"failed"`
	Truncated  bool   `json:"truncated"`
	Done       bool   `json:"done"`
	Query      string `json:"query,omitempty"`
	LastStored string `json:"last_stored,omitempty"`
	Error      string `json:"error,omitempty"`
}

// MailArchiveSyncer is implemented by connectors that can page their message
// list into the local archive (compressed payload + FTS index).
type MailArchiveSyncer interface {
	SyncMailArchive(ctx context.Context, archive *MailArchive, opts MailSyncOptions) (MailSyncResult, error)
}

// mailSyncDefaultQuery pulls every mailbox the user can see, including
// spam/trash, so "all of my email" is actually complete.
const mailSyncDefaultQuery = "in:anywhere"

// mailSyncListPage is the provider ID page size during archive ingest.
const mailSyncListPage = 100

// gmailQuotaMarker identifies Google's per-user rate-quota body text.
const gmailQuotaMarker = "Quota exceeded"

// gmailArchiveGetInterval paces full-message gets. Gmail's "Units per minute
// per user" budget is easy to exhaust when hydrating a whole page of IDs;
// without pacing, mid-page 403s used to skip those messages forever.
var gmailArchiveGetInterval = 1200 * time.Millisecond

func isGmailQuotaError(err error) bool {
	return err != nil && strings.Contains(err.Error(), gmailQuotaMarker)
}

// SyncMailArchive implements MailArchiveSyncer. Each call lists up to
// MaxMessages IDs (continuing the stored page token) and stores full payloads.
func (c *gmailArchiveSyncer) SyncMailArchive(ctx context.Context, archive *MailArchive, opts MailSyncOptions) (MailSyncResult, error) {
	res := MailSyncResult{Service: connectors.GoogleGmailServiceID}
	if archive == nil {
		res.Error = "mail archive disabled"
		return res, fmt.Errorf("mail archive disabled")
	}
	maxMsgs := opts.MaxMessages
	if maxMsgs <= 0 {
		maxMsgs = 200
	}
	query := strings.TrimSpace(opts.Query)
	if query == "" {
		query = mailSyncDefaultQuery
	}
	res.Query = query

	logger := slog.With("service", connectors.GoogleGmailServiceID, "handler", "mail_archive_sync")
	token := c.host.GetValidToken(connectors.GoogleGmailServiceID, logger)
	if token == "" {
		res.Error = "no valid Gmail token"
		_ = archive.SaveSyncCursor(connectors.GoogleGmailServiceID, MailSyncCursor{Error: res.Error})
		return res, types.ErrUnauthorized
	}

	cursor, err := archive.LoadSyncCursor(connectors.GoogleGmailServiceID)
	if err != nil {
		cursor = MailSyncCursor{}
	}
	if opts.ResetCursor {
		cursor = MailSyncCursor{}
	}
	pageToken := cursor.PageToken

	for res.Stored+res.Failed < maxMsgs {
		pageSize := mailSyncListPage
		if remaining := maxMsgs - res.Stored - res.Failed; remaining > 0 && remaining < pageSize {
			pageSize = remaining
		}
		values := url.Values{}
		values.Set("maxResults", fmt.Sprint(pageSize))
		if query != "" {
			values.Set("q", query)
		}
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		}
		listURL := c.GoogleGmailUpstreamBase() + "/users/me/messages?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
		data, err := readMailBody(resp)
		if err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
		if resp.StatusCode != http.StatusOK {
			listErr := connectors.GmailListHTTPError("messages list", resp.StatusCode, data)
			// Keep the fetch page token so a quota blip retries this page.
			return c.finishMailSync(archive, res, cursor, listErr)
		}
		var raw connectors.GoogleGmailMessageList
		if err := json.Unmarshal(data, &raw); err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
		// pageFetchToken is the token that produced this page. Only advance the
		// durable cursor after the whole page is stored (or permanently skipped).
		pageFetchToken := pageToken
		for _, item := range raw.Messages {
			if res.Stored+res.Failed >= maxMsgs {
				res.Truncated = true
				break
			}
			if item.ID == "" {
				continue
			}
			res.Listed++
			if err := c.storeGmailMessageArchive(ctx, archive, token, item.ID); err != nil {
				if isGmailQuotaError(err) || ctx.Err() != nil {
					// Rate quota: stop now and leave the cursor on this page so
					// the remaining IDs are retried after the window resets.
					logger.Warn("mail archive sync paused", "id", item.ID, "error", err)
					res.Truncated = true
					retry := cursor
					retry.PageToken = pageFetchToken
					retry.Done = false
					return c.finishMailSync(archive, res, retry, err)
				}
				// Fail-open per message: one bad item must not drop the batch.
				logger.Warn("mail archive store failed", "id", item.ID, "error", err)
				res.Failed++
				continue
			}
			res.Stored++
			res.LastStored = item.ID
			if err := paceGmailArchiveGet(ctx); err != nil {
				retry := cursor
				retry.PageToken = pageFetchToken
				retry.Done = false
				return c.finishMailSync(archive, res, retry, err)
			}
		}
		pageToken = raw.NextPageToken
		if pageToken == "" {
			res.Done = true
			break
		}
		if res.Stored+res.Failed >= maxMsgs {
			res.Truncated = true
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	cursor.PageToken = pageToken
	cursor.Done = res.Done
	cursor.LastSyncAt = now
	cursor.Error = res.Error
	if err := archive.SaveSyncCursor(connectors.GoogleGmailServiceID, cursor); err != nil {
		logger.Warn("mail archive cursor save failed", "error", err)
	}
	return res, nil
}

func (c *gmailArchiveSyncer) finishMailSync(archive *MailArchive, res MailSyncResult, cursor MailSyncCursor, err error) (MailSyncResult, error) {
	res.Error = err.Error()
	now := time.Now().UTC().Format(time.RFC3339)
	cursor.LastSyncAt = now
	cursor.Error = res.Error
	_ = archive.SaveSyncCursor(connectors.GoogleGmailServiceID, cursor)
	return res, err
}

// paceGmailArchiveGet sleeps between full-message hydrates so a large page
// cannot exhaust the Gmail per-minute unit quota (issue: archive sync).
func paceGmailArchiveGet(ctx context.Context) error {
	if gmailArchiveGetInterval <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(gmailArchiveGetInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *gmailArchiveSyncer) storeGmailMessageArchive(ctx context.Context, archive *MailArchive, token, id string) error {
	raw, err := c.FetchGmailMessage(ctx, token, id, "full")
	if err != nil {
		return err
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	gz, err := GzipPayload(payload)
	if err != nil {
		return err
	}
	msg := connectors.MapGoogleGmailMessage(raw, "full")
	rec := RecordFromMessage(connectors.GoogleGmailServiceID, connectors.GoogleGmailServiceName, msg, gz)
	return archive.Upsert(rec)
}

func readMailBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
