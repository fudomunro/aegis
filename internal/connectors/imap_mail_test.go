package connectors

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// mockIMAPMsg is one message the mock server holds.
type mockIMAPMsg struct {
	uid   uint32
	flags []string
	raw   string
}

// mockIMAPServer is a tiny in-process IMAP server covering exactly the
// subset the ingest speaks: CAPABILITY, AUTHENTICATE PLAIN / LOGIN, SELECT,
// UID SEARCH, UID FETCH (FLAGS + BODY.PEEK[]), LOGOUT.
type mockIMAPServer struct {
	ln       net.Listener
	uidValid uint32
	msgs     []mockIMAPMsg
	// failFetch makes UID FETCH return NO for this uid (one-shot).
	failFetch map[uint32]bool

	mu   sync.Mutex
	cmds []string
}

func newMockIMAPServer(t *testing.T, uidValid uint32, msgs ...mockIMAPMsg) *mockIMAPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock imap listen: %v", err)
	}
	m := &mockIMAPServer{ln: ln, uidValid: uidValid, msgs: msgs, failFetch: map[uint32]bool{}}
	go m.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return m
}

func (m *mockIMAPServer) addr() string { return m.ln.Addr().String() }

// endpoint is the service endpoint a test points the connector at.
func (m *mockIMAPServer) endpoint() string {
	// tls=none: the mock speaks plaintext so tests need no certificates.
	return "imap://" + m.addr() + "?tls=none&folder=INBOX"
}

func (m *mockIMAPServer) commands() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.cmds...)
}

func (m *mockIMAPServer) serve() {
	for {
		conn, err := m.ln.Accept()
		if err != nil {
			return
		}
		go m.handle(conn)
	}
}

func (m *mockIMAPServer) handle(conn net.Conn) {
	defer conn.Close()
	fmt.Fprintf(conn, "* OK mock IMAP ready\r\n")
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		sp := strings.SplitN(line, " ", 2)
		tag := sp[0]
		cmd := ""
		if len(sp) > 1 {
			cmd = sp[1]
		}
		m.mu.Lock()
		m.cmds = append(m.cmds, cmd)
		m.mu.Unlock()
		up := strings.ToUpper(cmd)
		switch {
		case strings.HasPrefix(up, "CAPABILITY"):
			fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 AUTH=PLAIN\r\n%s OK capability done\r\n", tag)
		case strings.HasPrefix(up, "AUTHENTICATE PLAIN"), strings.HasPrefix(up, "LOGIN"):
			fmt.Fprintf(conn, "%s OK authenticated\r\n", tag)
		case strings.HasPrefix(up, "SELECT"):
			fmt.Fprintf(conn, "* %d EXISTS\r\n* OK [UIDVALIDITY %d] mock\r\n%s OK select done\r\n",
				len(m.msgs), m.uidValid, tag)
		case strings.HasPrefix(up, "UID SEARCH"):
			uids := m.search(cmd)
			var b strings.Builder
			b.WriteString("* SEARCH")
			for _, u := range uids {
				fmt.Fprintf(&b, " %d", u)
			}
			b.WriteString("\r\n")
			fmt.Fprintf(conn, "%s%s OK search done\r\n", b.String(), tag)
		case strings.HasPrefix(up, "UID FETCH"):
			m.fetch(conn, tag, cmd)
		case strings.HasPrefix(up, "LOGOUT"):
			fmt.Fprintf(conn, "* BYE\r\n%s OK logout\r\n", tag)
			return
		default:
			fmt.Fprintf(conn, "%s BAD unknown command\r\n", tag)
		}
	}
}

// search answers "UID SEARCH ALL" and "UID SEARCH UID <n>:*".
func (m *mockIMAPServer) search(cmd string) []uint32 {
	var out []uint32
	up := strings.ToUpper(cmd)
	i := strings.Index(up, "SEARCH ")
	if i < 0 {
		return nil
	}
	criteria := strings.TrimSpace(cmd[i+len("SEARCH "):])
	switch {
	case strings.EqualFold(criteria, "ALL"):
		for _, msg := range m.msgs {
			out = append(out, msg.uid)
		}
	case strings.HasPrefix(strings.ToUpper(criteria), "UID "):
		spec := strings.TrimSpace(criteria[len("UID "):])
		parts := strings.SplitN(spec, ":", 2)
		from, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil {
			return nil
		}
		for _, msg := range m.msgs {
			if msg.uid >= uint32(from) {
				out = append(out, msg.uid)
			}
		}
	}
	return out
}

// fetch answers "UID FETCH <uid> (… BODY.PEEK[])" with one literal body.
func (m *mockIMAPServer) fetch(conn net.Conn, tag, cmd string) {
	fields := strings.Fields(cmd)
	if len(fields) < 3 {
		fmt.Fprintf(conn, "%s BAD fetch\r\n", tag)
		return
	}
	uid, err := strconv.ParseUint(fields[2], 10, 32)
	if err != nil {
		fmt.Fprintf(conn, "%s BAD fetch uid\r\n", tag)
		return
	}
	if m.failFetch[uint32(uid)] {
		delete(m.failFetch, uint32(uid))
		fmt.Fprintf(conn, "%s NO fetch failed\r\n", tag)
		return
	}
	for i, msg := range m.msgs {
		if msg.uid != uint32(uid) {
			continue
		}
		flags := "\\Seen"
		if len(msg.flags) > 0 {
			flags = strings.Join(msg.flags, " ")
		}
		// Literal form: marker {n} + CRLF + n bytes + the rest of the line.
		fmt.Fprintf(conn, "* %d FETCH (UID %d FLAGS (%s) BODY[] {%d}\r\n%s)\r\n",
			i+1, msg.uid, flags, len(msg.raw), msg.raw)
		fmt.Fprintf(conn, "%s OK fetch done\r\n", tag)
		return
	}
	fmt.Fprintf(conn, "%s NO no such message\r\n", tag)
}

// mockSink is a MailSink fake that records what an ingest wrote.
type mockSink struct {
	mu       sync.Mutex
	stored   []kinds.Message
	payloads [][]byte
	skipped  map[string]bool // source ids already present
	cursor   shared.MailIngestCursor
	meta     map[string]string
	failStore bool
}

func newMockSink() *mockSink {
	return &mockSink{skipped: map[string]bool{}, meta: map[string]string{}}
}

// seed marks a source id as already stored so HasMessage reports it.
func (s *mockSink) seed(msg kinds.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stored = append(s.stored, msg)
}

func (s *mockSink) HasMessage(service string, msg kinds.Message) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, have := range s.stored {
		if kinds.NormalizeMessageID(have.EntityID) != "" &&
			kinds.NormalizeMessageID(have.EntityID) == kinds.NormalizeMessageID(msg.EntityID) {
			return true, nil
		}
		for _, src := range have.Sources {
			for _, want := range msg.Sources {
				if src.Service == want.Service && src.ID == want.ID && want.ID != "" {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (s *mockSink) Store(service string, msg kinds.Message, rawPayload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failStore {
		return fmt.Errorf("mock store failure")
	}
	s.stored = append(s.stored, msg)
	s.payloads = append(s.payloads, rawPayload)
	return nil
}

func (s *mockSink) LoadCursor(service string) (shared.MailIngestCursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, nil
}

func (s *mockSink) SaveCursor(service string, cur shared.MailIngestCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor = cur
	return nil
}

func (s *mockSink) MetaGet(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta[key], nil
}

func (s *mockSink) MetaSet(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta[key] = value
	return nil
}

const mockRFC822 = "Message-ID: <msg-%d@example.com>\r\n" +
	"From: Ada Lovelace <ada@example.com>\r\n" +
	"To: grace@example.com\r\n" +
	"Subject: Note %d\r\n" +
	"Date: Mon, 2 Jan 2006 15:04:05 -0700\r\n" +
	"\r\n" +
	"body of message %d\r\n"

func mockRaw(n int) string { return fmt.Sprintf(mockRFC822, n, n, n) }

func TestImapMailSource_Declaration(t *testing.T) {
	found := false
	for _, s := range MailSources() {
		if s.ServiceID != ImapMailServiceID {
			continue
		}
		found = true
		if !s.Canonical {
			t.Fatal("imap-mail must claim canonical (live mailbox home)")
		}
		if s.Parent != "" {
			t.Fatalf("parent = %q, want none", s.Parent)
		}
	}
	if !found {
		t.Fatal("ImapMailSource missing from MailSources()")
	}
	if src := MailSourceFor(ImapMailServiceID); !src.Canonical {
		t.Fatal("MailSourceFor(imap-mail) must return the declaration")
	}
}

func TestImapMail_ImplementsMailIngest(t *testing.T) {
	c := NewImapMailConnector(newTestHost(""))
	var _ MailIngest = c
	_ = c
}

func TestImapIngest_WalksUIDsAndStores(t *testing.T) {
	srv := newMockIMAPServer(t, 42,
		mockIMAPMsg{uid: 1, flags: []string{"\\Seen"}, raw: mockRaw(1)},
		mockIMAPMsg{uid: 2, flags: []string{"\\Answered"}, raw: mockRaw(2)},
	)
	th := newTestHostWithService(ImapMailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(ImapMailServiceID, map[string]string{"username": "u", "password": "p"})
	sink := newMockSink()

	res, err := NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 2 || res.Failed != 0 {
		t.Fatalf("stored=%d failed=%d, want 2/0", res.Stored, res.Failed)
	}
	if !res.Done {
		t.Fatal("expected Done on a complete walk")
	}
	if sink.cursor.PageToken != "2" {
		t.Fatalf("cursor = %q, want 2", sink.cursor.PageToken)
	}
	if len(sink.stored) != 2 {
		t.Fatalf("sink rows = %d, want 2", len(sink.stored))
	}
	msg := sink.stored[0]
	if msg.EntityID != "msg-1@example.com" {
		t.Fatalf("entity id = %q", msg.EntityID)
	}
	if !msg.Read {
		t.Fatal("uid 1 carries \\Seen; Read must be true")
	}
	if got := msg.Labels; len(got) != 1 || got[0] != "SEEN" {
		t.Fatalf("labels = %v, want [SEEN]", got)
	}
	if msg.Sources[0].ID != "42:1" {
		t.Fatalf("source id = %q, want 42:1 (uidvalidity:uid)", msg.Sources[0].ID)
	}
	if msg.Sources[0].Mailbox != "INBOX" {
		t.Fatalf("mailbox = %q, want INBOX", msg.Sources[0].Mailbox)
	}
	if msg.Direction != "inbound" {
		t.Fatalf("direction = %q, want inbound", msg.Direction)
	}
	// The walk must never mark mail read on the server: FETCH uses the
	// non-mutating BODY.PEEK[] form, not BODY[].
	for _, cmd := range srv.commands() {
		if strings.Contains(cmd, "BODY[]") {
			t.Fatalf("fetch %q must use BODY.PEEK[], never BODY[]", cmd)
		}
	}
}

func TestImapIngest_ResumesFromCursor(t *testing.T) {
	srv := newMockIMAPServer(t, 7,
		mockIMAPMsg{uid: 1, raw: mockRaw(1)},
		mockIMAPMsg{uid: 2, raw: mockRaw(2)},
		mockIMAPMsg{uid: 3, raw: mockRaw(3)},
	)
	th := newTestHostWithService(ImapMailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(ImapMailServiceID, map[string]string{"username": "u", "password": "p"})
	sink := newMockSink()
	sink.cursor = shared.MailIngestCursor{PageToken: "2"}

	res, err := NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 1 || res.Listed != 1 {
		t.Fatalf("stored=%d listed=%d, want 1/1 (only uid 3 is new)", res.Stored, res.Listed)
	}
	if sink.stored[0].Sources[0].ID != "7:3" {
		t.Fatalf("stored %q, want 7:3", sink.stored[0].Sources[0].ID)
	}
}

func TestImapIngest_UIDValidityChangeResetsCursor(t *testing.T) {
	srv := newMockIMAPServer(t, 99,
		mockIMAPMsg{uid: 1, raw: mockRaw(1)},
	)
	th := newTestHostWithService(ImapMailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(ImapMailServiceID, map[string]string{"username": "u", "password": "p"})
	sink := newMockSink()
	sink.cursor = shared.MailIngestCursor{PageToken: "500"}
	sink.meta[imapUIDValidityMetaKey+":INBOX"] = "42" // different from the server's 99

	res, err := NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	// The stale cursor must not hide uid 1 behind position 500.
	if res.Stored != 1 {
		t.Fatalf("stored=%d, want 1 after UIDVALIDITY reset", res.Stored)
	}
	if sink.meta[imapUIDValidityMetaKey+":INBOX"] != "99" {
		t.Fatalf("uidvalidity meta = %q, want 99", sink.meta[imapUIDValidityMetaKey+":INBOX"])
	}
}

func TestImapIngest_DedupesByID(t *testing.T) {
	srv := newMockIMAPServer(t, 1,
		mockIMAPMsg{uid: 5, raw: mockRaw(5)},
	)
	th := newTestHostWithService(ImapMailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(ImapMailServiceID, map[string]string{"username": "u", "password": "p"})
	sink := newMockSink()
	sink.seed(kinds.Message{
		EntityID: "msg-5@example.com",
		Sources:  []kinds.MessageSource{{Service: ImapMailServiceID, ID: "1:5"}},
	})

	res, err := NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Skipped != 1 || res.Stored != 0 {
		t.Fatalf("skipped=%d stored=%d, want 1/0", res.Skipped, res.Stored)
	}
}

func TestImapIngest_FailOpenOnUnparseableMessage(t *testing.T) {
	srv := newMockIMAPServer(t, 3,
		mockIMAPMsg{uid: 1, raw: "not an rfc822 message without headers"},
		mockIMAPMsg{uid: 2, raw: mockRaw(2)},
	)
	th := newTestHostWithService(ImapMailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(ImapMailServiceID, map[string]string{"username": "u", "password": "p"})
	sink := newMockSink()

	res, err := NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Failed != 1 || res.Stored != 1 {
		t.Fatalf("failed=%d stored=%d, want 1/1", res.Failed, res.Stored)
	}
	// The parse failure must not block the next message or the cursor.
	if sink.cursor.PageToken != "2" {
		t.Fatalf("cursor = %q, want 2", sink.cursor.PageToken)
	}
}

func TestImapIngest_TransportErrorStopsWithoutSkipping(t *testing.T) {
	srv := newMockIMAPServer(t, 3,
		mockIMAPMsg{uid: 1, raw: mockRaw(1)},
		mockIMAPMsg{uid: 2, raw: mockRaw(2)},
		mockIMAPMsg{uid: 3, raw: mockRaw(3)},
	)
	srv.failFetch[2] = true
	th := newTestHostWithService(ImapMailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(ImapMailServiceID, map[string]string{"username": "u", "password": "p"})
	sink := newMockSink()

	res, err := NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err == nil {
		t.Fatal("expected the fetch failure to surface")
	}
	if res.Stored != 1 {
		t.Fatalf("stored=%d, want 1 (uid 1 only)", res.Stored)
	}
	// Cursor must stay on the last success so uid 2 is retried next run.
	if sink.cursor.PageToken != "1" {
		t.Fatalf("cursor = %q, want 1", sink.cursor.PageToken)
	}
}

func TestImapIngest_BatchCapTruncates(t *testing.T) {
	srv := newMockIMAPServer(t, 1,
		mockIMAPMsg{uid: 1, raw: mockRaw(1)},
		mockIMAPMsg{uid: 2, raw: mockRaw(2)},
		mockIMAPMsg{uid: 3, raw: mockRaw(3)},
	)
	th := newTestHostWithService(ImapMailServiceID, srv.endpoint(), "https://aegis.example.com")
	th.setCredentials(ImapMailServiceID, map[string]string{"username": "u", "password": "p"})
	sink := newMockSink()

	res, err := NewImapMailConnector(th).IngestMail(context.Background(), sink,
		shared.MailIngestOptions{MaxMessages: 2})
	if err != nil {
		t.Fatalf("IngestMail: %v", err)
	}
	if res.Stored != 2 || !res.Truncated || res.Done {
		t.Fatalf("stored=%d truncated=%v done=%v, want 2/true/false", res.Stored, res.Truncated, res.Done)
	}
	if sink.cursor.PageToken != "2" {
		t.Fatalf("cursor = %q, want 2", sink.cursor.PageToken)
	}
}

func TestImapIngest_MissingConfig(t *testing.T) {
	th := newTestHost("")
	sink := newMockSink()
	_, err := NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err == nil || !strings.Contains(err.Error(), "endpoint not set") {
		t.Fatalf("err = %v, want endpoint-not-set config error", err)
	}

	th = newTestHostWithService(ImapMailServiceID, "imap://mail.example.com:993", "")
	_, err = NewImapMailConnector(th).IngestMail(context.Background(), sink, shared.MailIngestOptions{})
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err = %v, want missing-credentials config error", err)
	}
}

func TestImapFlags_Mapping(t *testing.T) {
	if !imapFlagsRead([]string{"\\Seen"}) {
		t.Fatal("\\Seen must map to Read")
	}
	if imapFlagsRead([]string{"\\Flagged"}) {
		t.Fatal("\\Flagged alone must not map to Read")
	}
	got := imapFlagsToLabels([]string{"\\Seen", "\\Draft", "\\Junk", "custom"})
	want := []string{"SEEN", "DRAFT"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("labels = %v, want %v (keywords stay server-side)", got, want)
	}
}

func TestImapSourceID_ScopesUIDValidity(t *testing.T) {
	if got := imapSourceID(42, 7); got != "42:7" {
		t.Fatalf("source id = %q, want 42:7", got)
	}
}

// TestImapClient_LiteralSplice pins the response parser: a FETCH line whose
// body arrives as a server literal must come back whole, flags and all.
func TestImapClient_LiteralSplice(t *testing.T) {
	srv := newMockIMAPServer(t, 1, mockIMAPMsg{
		uid: 9, flags: []string{"\\Seen", "\\Flagged"}, raw: "Subject: x\r\n\r\nhello\r\n",
	})
	cfg, err := parseTransportEndpoint(srv.endpoint(), imapDefaultPort)
	if err != nil {
		t.Fatalf("parseTransportEndpoint: %v", err)
	}
	c, err := imapDial(context.Background(), cfg)
	if err != nil {
		t.Fatalf("imapDial: %v", err)
	}
	defer c.logout()
	if err := c.imapLogin("u", "p"); err != nil {
		t.Fatalf("imapLogin: %v", err)
	}
	if _, err := c.imapSelect("INBOX"); err != nil {
		t.Fatalf("imapSelect: %v", err)
	}
	uids, err := c.imapUIDSearch("ALL")
	if err != nil {
		t.Fatalf("imapUIDSearch: %v", err)
	}
	if len(uids) != 1 || uids[0] != 9 {
		t.Fatalf("uids = %v, want [9]", uids)
	}
	msg, err := c.imapUIDFetch(9)
	if err != nil {
		t.Fatalf("imapUIDFetch: %v", err)
	}
	if string(msg.Raw) != "Subject: x\r\n\r\nhello\r\n" {
		t.Fatalf("raw = %q, want the message bytes unmodified", msg.Raw)
	}
	if strings.Join(msg.Flags, ",") != "\\Seen,\\Flagged" {
		t.Fatalf("flags = %v, want [\\Seen \\Flagged]", msg.Flags)
	}
}

func TestParseTransportEndpoint(t *testing.T) {
	cases := []struct {
		raw      string
		def      string
		wantHost string
		wantPort string
		wantTLS  string
		wantDir  string
		wantErr  bool
	}{
		{raw: "imaps://mail.example.com", def: "993", wantHost: "mail.example.com", wantPort: "993", wantTLS: "implicit"},
		{raw: "imap://mail.example.com:143", def: "993", wantHost: "mail.example.com", wantPort: "143", wantTLS: "starttls"},
		{raw: "imap://mail.example.com:143?tls=none", def: "993", wantHost: "mail.example.com", wantPort: "143", wantTLS: "none"},
		{raw: "imaps://mail.example.com:993?folder=Archive", def: "993", wantHost: "mail.example.com", wantPort: "993", wantTLS: "implicit", wantDir: "Archive"},
		{raw: "pop3s://pop.example.com", def: "995", wantHost: "pop.example.com", wantPort: "995", wantTLS: "implicit"},
		{raw: "pop3://pop.example.com:110?tls=none", def: "995", wantHost: "pop.example.com", wantPort: "110", wantTLS: "none"},
		{raw: "", def: "993", wantErr: true},
		{raw: "smtp://mail.example.com", def: "993", wantErr: true},
		{raw: "imap://", def: "993", wantErr: true},
		{raw: "imap://mail.example.com?tls=bogus", def: "993", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseTransportEndpoint(tc.raw, tc.def)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("parseTransportEndpoint(%q): want error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseTransportEndpoint(%q): %v", tc.raw, err)
		}
		if got.Host != tc.wantHost || got.Port != tc.wantPort || got.TLSMode != tc.wantTLS || got.Folder != tc.wantDir {
			t.Fatalf("parseTransportEndpoint(%q) = %+v, want host=%s port=%s tls=%s folder=%s",
				tc.raw, got, tc.wantHost, tc.wantPort, tc.wantTLS, tc.wantDir)
		}
	}
}

