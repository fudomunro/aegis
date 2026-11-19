package connectors

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
)

// Shared dial-target configuration for the protocol mail transports
// (IMAP and POP3; issue #420). Both read a service Endpoint of the form
// <scheme>://<host>[:<port>][?tls=…][&folder=…] and secure the link the
// same way.

// Credential field names on the service entry for protocol mail transports
// (issue #420): svc.Credentials[MailCredentialUsername] and
// svc.Credentials[MailCredentialPassword].
const (
	MailCredentialUsername = "username"
	MailCredentialPassword = "password"
)

// transportConfig is the parsed endpoint for a protocol mail source: where
// to dial and how to secure the link.
type transportConfig struct {
	Host string
	Port string
	// TLSMode is "implicit" (TLS from the first byte), "starttls"
	// (upgrade after greeting), or "none" (plaintext).
	TLSMode string
	// Folder is the IMAP mailbox to walk ("" means INBOX). POP3 ignores it.
	Folder string
}

// parseTransportEndpoint parses an imap://, imaps://, pop3://, or pop3s://
// endpoint into a dial target. Query parameters: tls=implicit|starttls|none
// and folder=<imap mailbox>. The scheme's "s" form implies tls=implicit;
// the bare form defaults to starttls.
func parseTransportEndpoint(raw, defaultPort string) (transportConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return transportConfig{}, fmt.Errorf("%w: endpoint not set", shared.ErrMailConfig)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return transportConfig{}, fmt.Errorf("%w: bad endpoint: %v", shared.ErrMailConfig, err)
	}
	host := u.Hostname()
	if host == "" {
		return transportConfig{}, fmt.Errorf("%w: endpoint has no host", shared.ErrMailConfig)
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	var tlsMode string
	switch strings.ToLower(u.Scheme) {
	case "imaps", "pop3s":
		tlsMode = "implicit"
	case "imap", "pop3":
		tlsMode = "starttls"
	default:
		return transportConfig{}, fmt.Errorf("%w: unsupported endpoint scheme %q", shared.ErrMailConfig, u.Scheme)
	}
	if q := strings.ToLower(u.Query().Get("tls")); q != "" {
		switch q {
		case "implicit", "starttls", "none":
			tlsMode = q
		default:
			return transportConfig{}, fmt.Errorf("%w: tls must be implicit, starttls, or none", shared.ErrMailConfig)
		}
	}
	return transportConfig{
		Host:    host,
		Port:    port,
		TLSMode: tlsMode,
		Folder:  strings.TrimSpace(u.Query().Get("folder")),
	}, nil
}
