package core

import (
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
)

func TestHasContactProviders_GoogleContacts(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.connectors = map[string]connectors.ServiceConnector{
		connectors.GoogleContactsServiceID: connectors.NewGoogleContactsConnector(srv),
	}
	if !kindHasProviders("contacts", srv) {
		t.Fatal("Google Contacts connector should register as ContactProvider")
	}
}
