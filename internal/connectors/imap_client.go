package connectors

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Minimal IMAP (RFC 3501) client for mail archive ingest (issue #420).
// Subset: greeting, CAPABILITY, STARTTLS, LOGIN / AUTHENTICATE PLAIN,
// SELECT, UID SEARCH, UID FETCH (FLAGS + BODY.PEEK[]), LOGOUT. No IDLE, no
// CONDSTORE — the ingest is a bounded batch walk. Stdlib-only: the protocol
// subset is small enough to own, and mail archival must not inherit a
// dependency's parsing quirks.

// imapDefaultTimeout bounds one protocol round trip. Whole-ingest pacing is
// the runner's job; this only keeps a dead server from wedging a batch.
const imapDefaultTimeout = 60 * time.Second

// imapBatchMaxMessages is the ceiling one ingest run may hydrate, shared by
// the IMAP and POP3 walks so neither can exhaust memory in a single batch.
const imapBatchMaxMessages = 1000

// imapLiteralMarker is the placeholder readLine substitutes for a server
// literal so the FETCH parser can zip the payload back in.
const imapLiteralMarker = "\x00LIT\x00"

// imapLine is one logical IMAP response line with any server literals
// spliced out of it. Text carries imapLiteralMarker in each literal's place,
// so callers zip Literals back by marker position.
type imapLine struct {
	Text     string
	Literals [][]byte
}

// firstLiteral returns the payload of the first spliced literal, if any.
func (l imapLine) firstLiteral() []byte {
	if len(l.Literals) == 0 || !strings.Contains(l.Text, imapLiteralMarker) {
		return nil
	}
	return l.Literals[0]
}

// imapClient is one IMAP session. Not safe for concurrent use; the mail
// runner serializes ingest calls (docs/mail-ingest.md).
type imapClient struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	tag  int
}

// imapDial opens a session and consumes the server greeting.
func imapDial(ctx context.Context, cfg transportConfig) (*imapClient, error) {
	d := net.Dialer{Timeout: imapDefaultTimeout}
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	var conn net.Conn
	var err error
	if cfg.TLSMode == "implicit" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("imap dial %s: %w", addr, err)
	}
	c := &imapClient{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}
	greeting, err := c.readLine()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("imap greeting: %w", err)
	}
	if strings.HasPrefix(greeting.Text, "* BYE") {
		conn.Close()
		return nil, errors.New("imap server refused the connection")
	}
	if cfg.TLSMode == "starttls" {
		if err := c.startTLS(cfg.Host); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return c, nil
}

// startTLS upgrades the session in place (RFC 3501 §6.2.1), refusing
// servers that do not advertise the capability.
func (c *imapClient) startTLS(host string) error {
	untagged, resp, err := c.command("CAPABILITY")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "OK") {
		return fmt.Errorf("imap CAPABILITY failed: %s", resp)
	}
	if !strings.Contains(strings.ToUpper(imapLinesText(untagged)), "STARTTLS") {
		return errors.New("imap server does not advertise STARTTLS")
	}
	_, resp, err = c.command("STARTTLS")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "OK") {
		return fmt.Errorf("imap STARTTLS rejected: %s", resp)
	}
	tlsConn := tls.Client(c.conn, &tls.Config{ServerName: host})
	if err := tlsConn.Handshake(); err != nil {
		return fmt.Errorf("imap STARTTLS handshake: %w", err)
	}
	c.conn = tlsConn
	c.r = bufio.NewReader(tlsConn)
	c.w = bufio.NewWriter(tlsConn)
	return nil
}

// imapLogin authenticates. Tries AUTHENTICATE PLAIN when advertised, else
// LOGIN. On a plaintext link (tls=none) the password crosses the network
// unencrypted — the operator's explicit choice, same as any mail client.
func (c *imapClient) imapLogin(username, password string) error {
	untagged, resp, err := c.command("CAPABILITY")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "OK") {
		return fmt.Errorf("imap CAPABILITY failed: %s", resp)
	}
	caps := strings.ToUpper(imapLinesText(untagged))
	if strings.Contains(caps, "AUTH=PLAIN") {
		payload := base64.StdEncoding.EncodeToString([]byte("\x00" + username + "\x00" + password))
		_, resp, err := c.command("AUTHENTICATE PLAIN " + payload)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(resp, "OK") {
			return fmt.Errorf("imap authenticate failed: %s", resp)
		}
		return nil
	}
	_, resp, err = c.quotedCommand("LOGIN", username, password)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "OK") {
		return fmt.Errorf("imap login failed: %s", resp)
	}
	return nil
}

// imapSelect selects a mailbox and returns its UIDVALIDITY.
func (c *imapClient) imapSelect(mailbox string) (uint32, error) {
	if mailbox == "" {
		mailbox = "INBOX"
	}
	untagged, resp, err := c.quotedCommand("SELECT", mailbox)
	if err != nil {
		return 0, err
	}
	if !strings.HasPrefix(resp, "OK") {
		return 0, fmt.Errorf("imap SELECT %s failed: %s", mailbox, resp)
	}
	for _, line := range untagged {
		// "* OK [UIDVALIDITY 1234567890] ..." → "OK [UIDVALIDITY …]"
		text := line.Text
		up := strings.ToUpper(text)
		i := strings.Index(up, "[UIDVALIDITY ")
		if i < 0 {
			continue
		}
		rest := text[i+len("[UIDVALIDITY "):]
		j := strings.IndexByte(rest, ']')
		if j <= 0 {
			continue
		}
		if n, err := strconv.ParseUint(strings.TrimSpace(rest[:j]), 10, 32); err == nil {
			return uint32(n), nil
		}
	}
	return 0, nil
}

// imapUIDSearch returns UIDs matching the given criteria (RFC 3501 §6.4.8),
// e.g. "UID 42:*".
func (c *imapClient) imapUIDSearch(criteria string) ([]uint32, error) {
	untagged, resp, err := c.command("UID SEARCH " + criteria)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(resp, "OK") {
		return nil, fmt.Errorf("imap UID SEARCH failed: %s", resp)
	}
	var uids []uint32
	for _, line := range untagged {
		// readUntilTag strips the "* " prefix, so a search line reads
		// "SEARCH 1 2 3".
		fields := strings.Fields(line.Text)
		if len(fields) < 1 || !strings.EqualFold(fields[0], "SEARCH") {
			continue
		}
		for _, f := range fields[1:] {
			if n, err := strconv.ParseUint(f, 10, 32); err == nil {
				uids = append(uids, uint32(n))
			}
		}
	}
	return uids, nil
}

// imapMessage is one fetched message: its flags and raw RFC 822 bytes.
type imapMessage struct {
	UID   uint32
	Flags []string
	Raw   []byte
}

// imapUIDFetch fetches flags and the raw RFC 822 body for one UID without
// marking it read (BODY.PEEK[]).
func (c *imapClient) imapUIDFetch(uid uint32) (imapMessage, error) {
	out := imapMessage{UID: uid}
	untagged, resp, err := c.command(fmt.Sprintf("UID FETCH %d (UID FLAGS BODY.PEEK[])", uid))
	if err != nil {
		return out, err
	}
	if !strings.HasPrefix(resp, "OK") {
		return out, fmt.Errorf("imap UID FETCH %d failed: %s", uid, resp)
	}
	for _, line := range untagged {
		if !strings.Contains(strings.ToUpper(line.Text), "FETCH") {
			continue
		}
		out.Flags = parseImapFlags(line.Text)
		if raw := line.firstLiteral(); raw != nil {
			out.Raw = raw
		}
	}
	return out, nil
}

// parseImapFlags extracts the FLAGS (...) parenthesized list from a FETCH
// line whose literals have already been spliced out.
func parseImapFlags(line string) []string {
	up := strings.ToUpper(line)
	i := strings.Index(up, "FLAGS ")
	if i < 0 {
		return nil
	}
	rest := line[i+len("FLAGS "):]
	open := strings.IndexByte(rest, '(')
	if open < 0 {
		return nil
	}
	cl := strings.IndexByte(rest[open:], ')')
	if cl < 0 {
		return nil
	}
	return strings.Fields(rest[open+1 : open+cl])
}

// imapLinesText joins untagged line texts (capability probes).
func imapLinesText(lines []imapLine) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, l.Text)
	}
	return strings.Join(parts, " ")
}

// command sends one command whose arguments are already protocol-safe and
// collects untagged lines until the tagged completion.
func (c *imapClient) command(args string) ([]imapLine, string, error) {
	c.tag++
	tag := fmt.Sprintf("A%04d", c.tag)
	if err := c.send(tag + " " + args + "\r\n"); err != nil {
		return nil, "", err
	}
	return c.readUntilTag(tag)
}

// quotedCommand sends a command whose string arguments must be quoted.
func (c *imapClient) quotedCommand(verb string, args ...string) ([]imapLine, string, error) {
	var b strings.Builder
	b.WriteString(verb)
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(imapQuote(a))
	}
	return c.command(b.String())
}

// imapQuote quotes a string argument per RFC 3501 §4.3.
func imapQuote(s string) string {
	needs := s == ""
	for _, r := range s {
		if r <= ' ' || r == '"' || r == '\\' || r == '(' || r == ')' || r == '{' {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func (c *imapClient) send(s string) error {
	_ = c.conn.SetDeadline(time.Now().Add(imapDefaultTimeout))
	if _, err := c.w.WriteString(s); err != nil {
		return err
	}
	return c.w.Flush()
}

// readUntilTag collects untagged lines until the tagged completion arrives.
func (c *imapClient) readUntilTag(tag string) ([]imapLine, string, error) {
	var untagged []imapLine
	for {
		line, err := c.readLine()
		if err != nil {
			return untagged, "", err
		}
		switch {
		case strings.HasPrefix(line.Text, "*"):
			trimmed := strings.TrimSpace(line.Text[1:])
			untagged = append(untagged, imapLine{Text: trimmed, Literals: line.Literals})
		case strings.HasPrefix(line.Text, "+"):
			// A continuation means the server wants more data; this subset
			// never sends literals in commands, so refuse rather than stall.
			return untagged, "", fmt.Errorf("unexpected imap continuation: %s", line.Text)
		case strings.HasPrefix(line.Text, tag+" "):
			return untagged, strings.TrimSpace(line.Text[len(tag)+1:]), nil
		default:
			return untagged, "", fmt.Errorf("unexpected imap response line: %s", line.Text)
		}
	}
}

// readLine reads one logical IMAP line, splicing server literals ({n} + CRLF
// + n bytes) out of the stream. Each spliced payload lands in the returned
// line's Literals and imapLiteralMarker is substituted in the text so
// callers can zip it back by position.
func (c *imapClient) readLine() (imapLine, error) {
	_ = c.conn.SetDeadline(time.Now().Add(imapDefaultTimeout))
	var lits [][]byte
	var line []byte
	for {
		chunk, err := c.r.ReadString('\n')
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				return imapLine{Text: string(line), Literals: lits}, nil
			}
			return imapLine{}, err
		}
		body := strings.TrimSuffix(strings.TrimSuffix(chunk, "\n"), "\r")
		if n, ok := literalSize(body); ok {
			// {n} ends this physical line: the next n bytes are the literal
			// payload, after which the logical line continues.
			open := strings.LastIndexByte(body, '{')
			line = append(line, body[:open]...)
			line = append(line, imapLiteralMarker...)
			payload := make([]byte, n)
			if _, err := io.ReadFull(c.r, payload); err != nil {
				return imapLine{}, err
			}
			lits = append(lits, payload)
			continue
		}
		line = append(line, body...)
		return imapLine{Text: string(line), Literals: lits}, nil
	}
}

// literalSize reports whether the physical line ends with a literal marker
// {n}, returning n.
func literalSize(line string) (int, bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, false
	}
	open := strings.LastIndexByte(line, '{')
	if open < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(line[open+1 : len(line)-1])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (c *imapClient) logout() {
	_, _, _ = c.command("LOGOUT")
	_ = c.conn.Close()
}
