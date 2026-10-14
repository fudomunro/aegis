package core

import (
	"bytes"
	"strings"
	"testing"
)

// Fuzz targets for hostile/irregular Takeout + mbox input (issue #107).
// These parsers run on user-supplied archives; they must never panic.

func FuzzRecordFromTakeoutMessage(f *testing.F) {
	f.Add([]byte("From: a@b.c\nSubject: hi\nMessage-ID: <x@y>\nDate: Mon, 02 Jan 2006 15:04:05 -0700\n\nbody\n"))
	f.Add([]byte("From: a@b.c\nSubject: =?utf-8?B?5pel5pys6Kqe?=\n\nmultipart-ish"))
	f.Add([]byte("\nFrom: x\nSubject: x\n\n"))
	f.Add([]byte("not an email"))
	f.Add([]byte{})
	f.Add([]byte("From: a\nSubject: \x00\xff\nDate: not-a-date\nMessage-ID: <>\n\n\x00\x01\x02"))
	f.Add([]byte("From: a\nX-GM-LABELS: \\Inbox \\Sent UNREAD\nContent-Type: text/plain; charset=utf-8\n\nhello"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		rec, err := recordFromTakeoutMessage(raw, "mbox/test")
		if err != nil {
			return
		}
		if rec.ID == "" {
			t.Fatalf("successful parse produced empty ID for %q", raw)
		}
		if rec.Service == "" {
			t.Fatalf("successful parse produced empty service")
		}
		// Snippet must stay a prefix-safe truncation of BodyText or empty.
		if rec.Snippet != "" && !strings.HasPrefix(rec.BodyText, rec.Snippet) && rec.Snippet != rec.BodyText {
			// Snippet is body[:200]; if body shorter they match, else prefix.
			if !strings.HasPrefix(rec.BodyText, rec.Snippet) {
				t.Fatalf("snippet is not a prefix of body")
			}
		}
	})
}

func FuzzWalkMboxFrom(f *testing.F) {
	f.Add([]byte("From a@b Mon Jan  2 00:00:00 2006\nFrom: x\nSubject: one\n\nbody1\nFrom a@b Mon Jan  2 00:00:00 2006\nFrom: y\nSubject: two\n\nbody2\n"))
	f.Add([]byte("From local\n"))
	f.Add([]byte("From "))
	f.Add([]byte(""))
	f.Add([]byte("not mbox at all\nFrom: header\n"))
	f.Add([]byte("From x\n\nFrom x\n\nFrom x\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Collect messages; callback must never see empty payloads and
		// walk must not panic on any byte sequence.
		var msgs [][]byte
		err := walkMboxFrom(bytes.NewReader(data), 0, "fuzz", func(raw []byte, source string, msgStart, msgNext int64) error {
			if len(bytes.TrimSpace(raw)) == 0 {
				t.Fatalf("walkMbox emitted empty message")
			}
			if msgNext < msgStart {
				t.Fatalf("msgNext %d < msgStart %d", msgNext, msgStart)
			}
			msgs = append(msgs, append([]byte(nil), raw...))
			return nil
		})
		if err != nil {
			t.Fatalf("walkMboxFrom error: %v", err)
		}
		// Nested parse of each extracted blob must also be panic-free.
		for _, raw := range msgs {
			_, _ = recordFromTakeoutMessage(raw, "fuzz")
		}
		// isMboxFromLine is total.
		_ = isMboxFromLine(data)
	})
}

func FuzzIsMboxFromLine(f *testing.F) {
	f.Add([]byte("From "))
	f.Add([]byte("From: x\n"))
	f.Add([]byte("FROM "))
	f.Add([]byte(" From "))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, line []byte) {
		got := isMboxFromLine(line)
		want := bytes.HasPrefix(line, []byte("From "))
		if got != want {
			t.Fatalf("isMboxFromLine(%q) = %v, want %v", line, got, want)
		}
	})
}

func FuzzTakeoutMessageID(f *testing.F) {
	f.Add("", []byte("raw"))
	f.Add("<abc@x>", []byte("raw"))
	f.Add("  <>  ", []byte{})
	f.Add("weird id", []byte("payload"))
	f.Fuzz(func(t *testing.T, messageID string, raw []byte) {
		id := takeoutMessageID(messageID, raw)
		if id == "" {
			t.Fatal("empty takeout id")
		}
		if !strings.HasPrefix(id, "msgid:") && !strings.HasPrefix(id, "blob:") {
			t.Fatalf("unexpected id prefix: %q", id)
		}
		// Same inputs → same id (stable hashing).
		if again := takeoutMessageID(messageID, raw); again != id {
			t.Fatalf("unstable id: %q vs %q", id, again)
		}
	})
}
