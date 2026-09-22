package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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

// SyncMailArchive implements MailArchiveSyncer. Each call lists up to
// MaxMessages IDs (continuing the stored page token) and stores full payloads.
func (c *GoogleGmailConnector) SyncMailArchive(ctx context.Context, archive *MailArchive, opts MailSyncOptions) (MailSyncResult, error) {
	res := MailSyncResult{Service: googleGmailServiceID}
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

	logger := slog.With("service", googleGmailServiceID, "handler", "mail_archive_sync")
	token := c.server.getValidToken(googleGmailServiceID, logger)
	if token == "" {
		res.Error = "no valid Gmail token"
		_ = archive.SaveSyncCursor(googleGmailServiceID, MailSyncCursor{Error: res.Error})
		return res, ErrUnauthorized
	}

	cursor, err := archive.LoadSyncCursor(googleGmailServiceID)
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
		listURL := c.googleGmailUpstreamBase() + "/users/me/messages?" + values.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := guardianHTTPClient.Do(req)
		if err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
		data, err := readMailBody(resp)
		if err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
		if resp.StatusCode != http.StatusOK {
			return c.finishMailSync(archive, res, cursor, fmt.Errorf("gmail messages list returned %d", resp.StatusCode))
		}
		var raw googleGmailMessageList
		if err := json.Unmarshal(data, &raw); err != nil {
			return c.finishMailSync(archive, res, cursor, err)
		}
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
				// Fail-open per message: one bad item must not drop the batch.
				logger.Warn("mail archive store failed", "id", item.ID, "error", err)
				res.Failed++
				continue
			}
			res.Stored++
			res.LastStored = item.ID
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
	if err := archive.SaveSyncCursor(googleGmailServiceID, cursor); err != nil {
		logger.Warn("mail archive cursor save failed", "error", err)
	}
	return res, nil
}

func (c *GoogleGmailConnector) finishMailSync(archive *MailArchive, res MailSyncResult, cursor MailSyncCursor, err error) (MailSyncResult, error) {
	res.Error = err.Error()
	now := time.Now().UTC().Format(time.RFC3339)
	cursor.LastSyncAt = now
	cursor.Error = res.Error
	_ = archive.SaveSyncCursor(googleGmailServiceID, cursor)
	return res, err
}

func (c *GoogleGmailConnector) storeGmailMessageArchive(ctx context.Context, archive *MailArchive, token, id string) error {
	raw, err := c.fetchGmailMessage(ctx, token, id, "full")
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
	msg := mapGoogleGmailMessage(raw, "full")
	rec := RecordFromMessage(googleGmailServiceID, googleGmailServiceName, msg, gz)
	return archive.Upsert(rec)
}

func readMailBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
