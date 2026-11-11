package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/library"
)

func TestHasContactProviders_GoogleContacts(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.GoogleContactsServiceID: connectors.NewGoogleContactsConnector(srv),
	}
	if !library.KindHasProviders("contacts", srv.library()) {
		t.Fatal("Google Contacts connector should register as ContactProvider")
	}
}
