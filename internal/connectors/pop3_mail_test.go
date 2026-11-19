package connectors

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// mockPOP3Msg is one message the mock server holds.
type mockPOP3Msg struct {
	num  int
	uidl string
	raw  string
}

// mockPOP3Server is a tiny in-process POP3 server covering the ingest
// subset: USER, PASS, UIDL, RETR, QUIT.
type mockPOP3Server struct {
	ln    net.Listener
	msgs  []mockPOP3Msg
	failR map[int]bool

	mu   sync.Mutex
	cmds []string
}

func newMockPOP3Server(t *testing.T, msgs ...mockPOP3Msg) *mockPOP3Server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock pop3 listen: %v", err)
	}
	m := &mockPOP3Server{ln: ln, msgs: msgs, failR: map[int]bool{}}
	go m.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return m
}

func (m *mockPOP3Server) endpoint() string {
	return "pop3://" + m.ln.Addr().String() + "?tls=none"
}

func (m *mockPOP3Server) commands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.cmds...)
}

func (m *mockPOP3Server) serve() {
	for {
		conn, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.handle(conn)
	}
}

func (m *mockPOP3Server) handle(conn net.Conn) {
	defer conn.Close()
	fmt.Fprintf(conn, "+OK mock POP3 ready\r\n")
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		m.mu.Lock()
		m.cmds = append(m.cmds, line)
		m.mu.Unlock()
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "USER"), strings.HasPrefix(up, "PASS"), strings.HasPrefix(up, "STLS"):
			fmt.Fprintf(conn, "+OK\r\n")
		case strings.HasPrefix(up, "UIDL"):
			fmt.Fprintf(conn, "+OK\r\n")
			for _, msg := range m.msgs {
				fmt.Fprintf(conn, "%d %s\r\n", msg.num, msg.uidl)
			}
			fmt.Fprintf(conn, ".\r\n")
		case strings.HasPrefix(up, "RETR"):
			var num int
			fmt.Sscanf(line, "RETR %d", &num)
			if m.failR[num] {
				delete(m.failR, num)
				fmt.Fprintf(conn, "-ERR no such message\r\n")
				continue
			}
			for _, msg := range m.msgs {
				if msg.num != num {
					continue
				}
				fmt.Fprintf(conn, "+OK %d octets\r\n", len(msg.raw))
				// Dot-stuff every line, as RFC 1939 requires.
				for _, l := range strings.Split(strings.TrimSuffix(msg.raw, "\r\n"), "\r\n") {
					if strings.HasPrefix(l, ".") {
						l = "." + l
					}
					fmt.Fprintf(conn, "%s\r\n", l)
				}
				fmt.Fprintf(conn, ".\r\n")
				break
			}
		case strings.HasPrefix(up, "QUIT"):
			fmt.Fprintf(conn, "+OK bye\r\n")
			return
		default:
			fmt.Fprintf(conn, "-ERR unknown command\r\n")
		}
	}
}

func TestPop3MailSource_Declaration(t *testing.T) {
	found := false
	for _, s := range MailSources() {
		if s.ServiceID != Pop3MailServiceID {
			continue
		}
		found = true
		if !s.Canonical {
			t.Fatal("pop3-mail must claim canonical (live mailbox home)")
		}
		if s.Parent != "" {
			t.Fatalf("parent = %q, want none", s.Parent)
		}
	}
	if !found {
		t.Fatal("Pop3MailSource missing from MailSources()")
	}
	if src := MailSourceFor(Pop3MailServiceID); !src.Canonical {
		t.Fatal("MailSourceFor(pop3-mail) must return the declaration")
	}
}

func TestPop3Mail_ImplementsMailIngest(t *testing.T) {
	c := NewPop3MailConnector(newTestHost(""))
	var _ MailIngest = c
	_ = c
}

func pop3TestHost(t *testing.T, srv *mockPOP3Server) *testHost {
	t.Helper()
	th := newTestHostWithService(Pop3MailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(Pop3MailServiceID, map[string]string{"username": "u", "password": "p"})
	return th
}

func TestPop3Ingest_WalksAndStores(t *testing.T) {
	srv := newMockPOP3Server(t,
		mockPOP3Msg{num: 1, uidl: "aaa", raw: mockRaw(1)},
		mockPOP3Msg{num: 2, uidl: "bbb", raw: mockRaw(2)},
	)
	sink := newMockSink()

	res, err := NewPop3MailConnector(pop3TestHost(t, srv)).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 2 || res.Failed != 0 {
		t.Fatalf("stored=%d failed=%d, want 2/0", res.Stored, res.Failed)
	}
	if !res.Done {
		t.Fatal("expected Done on a complete walk")
	}
	if sink.cursor.PageToken != "bbb" {
		t.Fatalf("cursor = %q, want bbb", sink.cursor.PageToken)
	}
	msg := sink.stored[0]
	if msg.EntityID != "msg-1@example.com" {
		t.Fatalf("entity id = %q", msg.EntityID)
	}
	if !msg.Read {
		t.Fatal("POP3 rows are read (nothing marks them unread)")
	}
	if msg.Sources[0].ID != "aaa" {
		t.Fatalf("source id = %q, want the UIDL", msg.Sources[0].ID)
	}
	if msg.Sources[0].Mailbox != "POP3" {
		t.Fatalf("mailbox = %q, want POP3", msg.Sources[0].Mailbox)
	}
	if msg.Direction != "inbound" {
		t.Fatalf("direction = %q, want inbound", msg.Direction)
	}
}

func TestPop3Ingest_ProbeSkipsDownloaded(t *testing.T) {
	srv := newMockPOP3Server(t,
		mockPOP3Msg{num: 1, uidl: "aaa", raw: mockRaw(1)},
		mockPOP3Msg{num: 2, uidl: "bbb", raw: mockRaw(2)},
	)
	sink := newMockSink()
	// bbb is already in the archive under its UIDL.
	sink.seed(kinds.Message{
		Sources: []kinds.MessageSource{{Service: Pop3MailServiceID, ID: "bbb"}},
	})

	res, err := NewPop3MailConnector(pop3TestHost(t, srv)).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 1 || res.Skipped != 1 {
		t.Fatalf("stored=%d skipped=%d, want 1/1", res.Stored, res.Skipped)
	}
	// The probe means the known UIDL is never RETR'd.
	for _, cmd := range srv.commands() {
		if strings.HasPrefix(cmd, "RETR 2") {
			t.Fatal("already-stored UIDL must not be re-downloaded")
		}
	}
}

func TestPop3Ingest_ResumesAfterCursorUIDL(t *testing.T) {
	srv := newMockPOP3Server(t,
		mockPOP3Msg{num: 1, uidl: "aaa", raw: mockRaw(1)},
		mockPOP3Msg{num: 2, uidl: "bbb", raw: mockRaw(2)},
		mockPOP3Msg{num: 3, uidl: "ccc", raw: mockRaw(3)},
	)
	sink := newMockSink()
	sink.cursor = shared.MailIngestCursor{PageToken: "bbb"}

	res, err := NewPop3MailConnector(pop3TestHost(t, srv)).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 1 || res.Listed != 1 {
		t.Fatalf("stored=%d listed=%d, want 1/1 (only ccc is new)", res.Stored, res.Listed)
	}
	if sink.stored[0].Sources[0].ID != "ccc" {
		t.Fatalf("stored %q, want ccc", sink.stored[0].Sources[0].ID)
	}
}

func TestPop3Ingest_MissingCursorHintWalksFromTop(t *testing.T) {
	srv := newMockPOP3Server(t,
		mockPOP3Msg{num: 1, uidl: "aaa", raw: mockRaw(1)},
		mockPOP3Msg{num: 2, uidl: "bbb", raw: mockRaw(2)},
	)
	sink := newMockSink()
	// The cursor's UIDL is gone from the server (deleted): the walk must
	// restart from the top rather than skip the mailbox.
	sink.cursor = shared.MailIngestCursor{PageToken: "vanished"}

	res, err := NewPop3MailConnector(pop3TestHost(t, srv)).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 2 {
		t.Fatalf("stored=%d, want 2 (full walk)", res.Stored)
	}
}

func TestPop3Ingest_DotUnstuffing(t *testing.T) {
	// A body line beginning with "." is sent stuffed ("..") and must come
	// back single-dotted.
	raw := "Message-ID: <dot@example.com>\r\nSubject: dots\r\n\r\n.hidden\r\n.tail\r\n"
	srv := newMockPOP3Server(t, mockPOP3Msg{num: 1, uidl: "dots", raw: raw})
	sink := newMockSink()

	_, err := NewPop3MailConnector(pop3TestHost(t, srv)).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if len(sink.payloads) != 1 {
		t.Fatalf("payloads = %d, want 1", len(sink.payloads))
	}
	if string(sink.payloads[0]) != raw {
		t.Fatalf("payload = %q, want the un-stuffed message", sink.payloads[0])
	}
}

func TestPop3Ingest_TransportErrorStopsWithoutSkipping(t *testing.T) {
	srv := newMockPOP3Server(t,
		mockPOP3Msg{num: 1, uidl: "aaa", raw: mockRaw(1)},
		mockPOP3Msg{num: 2, uidl: "bbb", raw: mockRaw(2)},
	)
	srv.failR[2] = true
	sink := newMockSink()

	res, err := NewPop3MailConnector(pop3TestHost(t, srv)).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err == nil {
		t.Fatal("expected the RETR failure to surface")
	}
	if res.Stored != 1 {
		t.Fatalf("stored=%d, want 1", res.Stored)
	}
	if sink.cursor.PageToken != "aaa" {
		t.Fatalf("cursor = %q, want aaa so bbb is retried", sink.cursor.PageToken)
	}
}

func TestPop3Ingest_MissingConfig(t *testing.T) {
	sink := newMockSink()
	_, err := NewPop3MailConnector(newTestHost("")).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err == nil || !strings.Contains(err.Error(), "endpoint not set") {
		t.Fatalf("err = %v, want endpoint-not-set config error", err)
	}
	th := newTestHostWithService(Pop3MailServiceID, "pop3://pop.example.com:995", "")
	_, err = NewPop3MailConnector(th).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err = %v, want missing-credentials config error", err)
	}
}
