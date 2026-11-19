package connectors

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Minimal POP3 (RFC 1939) client for mail archive ingest (issue #420).
// Subset: greeting, STLS, USER/PASS, UIDL, RETR, QUIT. No DELE — the walk
// is read-only and leaves mail on the server, like the IMAP ingest's
// BODY.PEEK[]. Stdlib-only: the protocol is small (one request, one
// response, dot-stuffed multi-line bodies) and mail archival must not
// inherit a dependency's parsing quirks.

// pop3DefaultTimeout bounds one protocol round trip.
const pop3DefaultTimeout = 60 * time.Second

// pop3Entry is one mailbox message: its relative number and stable UIDL.
type pop3Entry struct {
	Num  int
	UIDL string
}

// pop3Client is one POP3 session. Not safe for concurrent use.
type pop3Client struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

// pop3Dial opens a session, consumes the greeting, and upgrades to TLS via
// STLS when the endpoint asked for starttls.
func pop3Dial(ctx context.Context, cfg transportConfig) (*pop3Client, error) {
	d := net.Dialer{Timeout: pop3DefaultTimeout}
	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	var conn net.Conn
	var err error
	if cfg.TLSMode == "implicit" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: cfg.Host})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("pop3 dial %s: %w", addr, err)
	}
	c := &pop3Client{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}
	if err := c.readOK(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("pop3 greeting: %w", err)
	}
	if cfg.TLSMode == "starttls" {
		if err := c.startTLS(cfg.Host); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return c, nil
}

// startTLS upgrades the session in place (RFC 2595 §4).
func (c *pop3Client) startTLS(host string) error {
	if err := c.cmd("STLS"); err != nil {
		return fmt.Errorf("pop3 STLS rejected (server may not support it): %w", err)
	}
	tlsConn := tls.Client(c.conn, &tls.Config{ServerName: host})
	if err := tlsConn.Handshake(); err != nil {
		return fmt.Errorf("pop3 STLS handshake: %w", err)
	}
	c.conn = tlsConn
	c.r = bufio.NewReader(tlsConn)
	c.w = bufio.NewWriter(tlsConn)
	return nil
}

// pop3Login authenticates with USER/PASS.
func (c *pop3Client) pop3Login(username, password string) error {
	if err := c.cmd("USER " + username); err != nil {
		return fmt.Errorf("pop3 USER: %w", err)
	}
	if err := c.cmd("PASS " + password); err != nil {
		return fmt.Errorf("pop3 PASS: %w", err)
	}
	return nil
}

// pop3UIDL lists every message's stable unique id (RFC 1939 §7).
func (c *pop3Client) pop3UIDL() ([]pop3Entry, error) {
	if err := c.send("UIDL\r\n"); err != nil {
		return nil, err
	}
	if err := c.readOK(); err != nil {
		return nil, fmt.Errorf("pop3 UIDL: %w", err)
	}
	lines, err := c.readMultiLine()
	if err != nil {
		return nil, fmt.Errorf("pop3 UIDL list: %w", err)
	}
	var out []pop3Entry
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		out = append(out, pop3Entry{Num: n, UIDL: fields[1]})
	}
	return out, nil
}

// pop3Retr downloads one message's raw RFC 822 bytes (dot-unstuffed).
func (c *pop3Client) pop3Retr(num int) ([]byte, error) {
	if err := c.send(fmt.Sprintf("RETR %d\r\n", num)); err != nil {
		return nil, err
	}
	if err := c.readOK(); err != nil {
		return nil, fmt.Errorf("pop3 RETR %d: %w", num, err)
	}
	raw, err := c.readDotStuff()
	if err != nil {
		return nil, fmt.Errorf("pop3 RETR %d body: %w", num, err)
	}
	return raw, nil
}

// pop3Quit ends the session cleanly.
func (c *pop3Client) pop3Quit() {
	_, _ = c.w.WriteString("QUIT\r\n")
	_ = c.w.Flush()
	_, _ = c.readLine()
	_ = c.conn.Close()
}

// cmd sends a single-line command and consumes its one-line response.
func (c *pop3Client) cmd(line string) error {
	if err := c.send(line + "\r\n"); err != nil {
		return err
	}
	return c.readOK()
}

func (c *pop3Client) send(s string) error {
	_ = c.conn.SetDeadline(time.Now().Add(pop3DefaultTimeout))
	if _, err := c.w.WriteString(s); err != nil {
		return err
	}
	return c.w.Flush()
}

// readOK consumes a one-line status response, failing on -ERR.
func (c *pop3Client) readOK() error {
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if strings.HasPrefix(line, "+OK") {
		return nil
	}
	if strings.HasPrefix(line, "-ERR") {
		return errors.New(strings.TrimSpace(line))
	}
	return fmt.Errorf("unexpected pop3 response: %s", line)
}

// readLine reads one CRLF-terminated line without its terminator.
func (c *pop3Client) readLine() (string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(pop3DefaultTimeout))
	line, err := c.r.ReadString('\n')
	if err != nil {
		if err == io.EOF && line != "" {
			return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
		}
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

// readMultiLine consumes an RFC 1939 multi-line response body (content
// lines ended by a single "." line).
func (c *pop3Client) readMultiLine() ([]string, error) {
	var out []string
	for {
		line, err := c.readLine()
		if err != nil {
			return out, err
		}
		if line == "." {
			return out, nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		out = append(out, line)
	}
}

// readDotStuff consumes a multi-line response and rebuilds the raw message,
// re-attaching CRLF to each line so the result is the RFC 822 byte stream
// the server holds (which itself ends in CRLF).
func (c *pop3Client) readDotStuff() ([]byte, error) {
	var buf bytes.Buffer
	for {
		line, err := c.readLine()
		if err != nil {
			return buf.Bytes(), err
		}
		if line == "." {
			return buf.Bytes(), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		buf.WriteString(line)
		buf.WriteString("\r\n")
	}
}
