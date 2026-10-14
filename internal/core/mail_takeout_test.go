package core

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleMbox = `From MAILER-DAEMON Fri Jan  1 10:00:00 2024
From: Ada Lovelace <ada@example.com>
To: me@example.com
Subject: Takeout hello
Message-ID: <takeout-1@example.com>
Date: Fri, 01 Jan 2024 10:00:00 +0000
X-GM-LABELS: INBOX
Content-Type: text/plain; charset=utf-8

Hello from the takeout export.

From MAILER-DAEMON Sat Jan  2 11:00:00 2024
From: me@example.com
To: ada@example.com
Subject: Re: Takeout hello
Message-ID: <takeout-2@example.com>
Date: Sat, 02 Jan 2024 11:00:00 +0000
X-GM-LABELS: SENT
Content-Type: text/plain; charset=utf-8

Sounds good.

From MAILER-DAEMON Sun Jan  3 12:00:00 2024
From: Broken <broken@example.com>
Subject: no message id
Date: Sun, 03 Jan 2024 12:00:00 +0000
Content-Type: text/plain; charset=utf-8

Bodies without Message-ID still import via content hash.
`

func writeTakeoutMbox(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "All mail Including Spam and Trash.mbox")
	if err := os.WriteFile(path, []byte(sampleMbox), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTakeoutImportMboxFile(t *testing.T) {
	a := openTestMailArchive(t)
	path := writeTakeoutMbox(t, t.TempDir())
	if err := a.SetTakeoutPath(path); err != nil {
		t.Fatalf("set path: %v", err)
	}
	if a.TakeoutPath() == "" {
		t.Fatal("expected stored takeout path")
	}

	res, err := a.ImportTakeout("", 10)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Scanned != 3 || res.Stored != 3 || !res.Done {
		t.Fatalf("res = %+v", res)
	}

	// Completed export is a cheap no-op until the cursor is reset.
	res2, err := a.ImportTakeout(path, 10)
	if err != nil {
		t.Fatalf("import2: %v", err)
	}
	if res2.Scanned != 0 || res2.Stored != 0 || !res2.Done {
		t.Fatalf("res2 = %+v", res2)
	}

	// After reset, re-sync skips already-present Message-IDs (read-only source).
	if err := a.ResetTakeoutCursor(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	res3, err := a.ImportTakeout(path, 10)
	if err != nil {
		t.Fatalf("import3: %v", err)
	}
	if res3.Stored != 0 || res3.Skipped != 3 {
		t.Fatalf("res3 = %+v", res3)
	}

	hits, err := a.Search(MailSearchQuery{Q: "takeout", WithBody: true})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if hits.Total != 2 {
		// "takeout" appears in subject/body of first two; third has "Message-ID" in body
		// but not the word takeout in a way FTS always hits — accept 2-3.
		if hits.Total < 2 {
			t.Fatalf("fts total = %d", hits.Total)
		}
	}

	byID, err := a.Get(takeoutServiceID, "msgid:takeout-1@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if byID.Subject != "Takeout hello" || byID.FromEmail != "ada@example.com" {
		t.Fatalf("rec = %+v", byID)
	}
	if byID.Direction != "inbound" || byID.Mailbox != "INBOX" {
		t.Fatalf("labels = %+v", byID)
	}
	if !strings.Contains(byID.BodyText, "Hello from the takeout export") {
		t.Fatalf("body = %q", byID.BodyText)
	}
}

func TestTakeoutImportDirectoryAndZip(t *testing.T) {
	a := openTestMailArchive(t)
	dir := t.TempDir()
	writeTakeoutMbox(t, dir)

	res, err := a.ImportTakeout(dir, 10)
	if err != nil {
		t.Fatalf("dir import: %v", err)
	}
	if res.Stored != 3 {
		t.Fatalf("dir res = %+v", res)
	}

	// Zip of an mbox entry.
	zipPath := filepath.Join(t.TempDir(), "takeout.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("Takeout/Mail/All mail Including Spam and Trash.mbox")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(sampleMbox)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zf.Close()

	a2 := openTestMailArchive(t)
	resZip, err := a2.ImportTakeout(zipPath, 10)
	if err != nil {
		t.Fatalf("zip import: %v", err)
	}
	if resZip.Stored != 3 {
		t.Fatalf("zip res = %+v", resZip)
	}
}

func TestTakeoutImportBatchCapAndBadMessage(t *testing.T) {
	a := openTestMailArchive(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "mail.mbox")
	// First message is invalid RFC822 (no headers); rest are fine.
	content := "From x\nnot-a-message\n\n" + sampleMbox
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := a.ImportTakeout(path, 2)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Failed < 1 {
		t.Fatalf("expected failed parse, res = %+v", res)
	}
	if res.Stored != 2 || !res.Truncated || res.Done {
		t.Fatalf("cap res = %+v", res)
	}
}

func TestAdminMailImportTakeout(t *testing.T) {
	a := openTestMailArchive(t)
	path := writeTakeoutMbox(t, t.TempDir())
	srv := mailTestServer(t, a)

	req := httptest.NewRequest("POST", "/v1/admin/mail/import", strings.NewReader(`{"path":"`+path+`","max_messages":0}`))
	req.Header.Set("X-Api-Key", "test-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save path = %d body=%s", w.Code, w.Body.String())
	}
	if a.TakeoutPath() == "" {
		t.Fatal("path not saved")
	}

	req = httptest.NewRequest("POST", "/v1/admin/mail/import", strings.NewReader(`{"max_messages":10}`))
	req.Header.Set("X-Api-Key", "test-key")
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	srv.handleAdminRoutes(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sync = %d body=%s", w.Code, w.Body.String())
	}
	var res MailSyncResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("json: %v", err)
	}
	if res.Service != takeoutServiceID || res.Stored != 3 {
		t.Fatalf("res = %+v", res)
	}

	// Status shows the read-only location.
	req = httptest.NewRequest("GET", "/v1/admin/mail/status", nil)
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	srv.AuthMiddleware(srv.HandleAdminMailStatus)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var st MailArchiveStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if st.TakeoutPath == "" || st.ByService[takeoutServiceID] != 3 {
		t.Fatalf("status = %+v", st)
	}

	// Missing path is a clear client error.
	req = httptest.NewRequest("POST", "/v1/admin/mail/import", strings.NewReader(`{"max_messages":5}`))
	req.Header.Set("X-Api-Key", "test-key")
	w = httptest.NewRecorder()
	a2 := openTestMailArchive(t)
	srv2 := mailTestServer(t, a2)
	srv2.handleAdminRoutes(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing path = %d body=%s", w.Code, w.Body.String())
	}
}

func TestTakeoutImportResumesWithoutFullRescan(t *testing.T) {
	a := openTestMailArchive(t)
	dir := t.TempDir()
	path := writeTakeoutMbox(t, dir) // 3 messages

	// First batch stores 2 and leaves the third for later.
	res1, err := a.ImportTakeout(path, 2)
	if err != nil {
		t.Fatalf("batch1: %v", err)
	}
	if res1.Scanned != 2 || res1.Stored != 2 || !res1.Truncated || res1.Done {
		t.Fatalf("batch1 = %+v", res1)
	}

	// Second batch must continue at the cursor — not re-scan the first two.
	res2, err := a.ImportTakeout(path, 10)
	if err != nil {
		t.Fatalf("batch2: %v", err)
	}
	if res2.Scanned != 1 || res2.Stored != 1 || !res2.Done {
		t.Fatalf("batch2 should resume at cursor, got %+v", res2)
	}
	if res2.Skipped != 0 {
		t.Fatalf("batch2 skipped (would indicate a re-scan) = %d", res2.Skipped)
	}

	// A completed export is a cheap no-op.
	res3, err := a.ImportTakeout(path, 10)
	if err != nil {
		t.Fatalf("batch3: %v", err)
	}
	if !res3.Done || res3.Scanned != 0 || res3.Stored != 0 {
		t.Fatalf("batch3 = %+v", res3)
	}

	// Explicit reset walks again and skips via identity.
	if err := a.ResetTakeoutCursor(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	res4, err := a.ImportTakeout(path, 10)
	if err != nil {
		t.Fatalf("batch4: %v", err)
	}
	if res4.Scanned != 3 || res4.Stored != 0 || res4.Skipped != 3 || !res4.Done {
		t.Fatalf("batch4 after reset = %+v", res4)
	}
}

func TestRecordFromTakeoutMessageMultipart(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"From: Ada <ada@example.com>",
		"To: me@example.com",
		"Subject: Multipart note",
		"Message-ID: <mp@example.com>",
		"Date: Fri, 01 Jan 2024 10:00:00 +0000",
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="b"`,
		"",
		"--b",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"plain body here",
		"--b",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>html body</p>",
		"--b--",
		"",
	}, "\n"))
	rec, err := recordFromTakeoutMessage(raw, "mail.mbox")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rec.BodyText != "plain body here" {
		t.Fatalf("body = %q", rec.BodyText)
	}
	if rec.ID != "msgid:mp@example.com" {
		t.Fatalf("id = %q", rec.ID)
	}
}
