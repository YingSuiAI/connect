//go:build goolm

package matrix

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/mockserver"
)

func TestTryInitCryptoUsesPureGoSQLite(t *testing.T) {
	ctx := context.Background()
	server := mockserver.Create(t)
	userID := id.UserID("@agent:example.test")
	deviceID := id.DeviceID("DIREXIO_TEST")
	client, _ := server.Login(t, ctx, userID, deviceID)

	delete(server.DeviceKeys[userID], deviceID)
	delete(server.OneTimeKeys[userID], deviceID)
	client.StateStore = nil
	client.Store = mautrix.NewMemorySyncStore()
	client.Crypto = nil

	p := &Platform{}
	helper, err := p.tryInitCrypto(ctx, client, []byte("01234567890123456789012345678901"), filepath.Join(t.TempDir(), "crypto.db"), false)
	if err != nil {
		if strings.Contains(err.Error(), "sqlite3-fk-wal") || strings.Contains(err.Error(), "unknown driver") {
			t.Fatalf("crypto helper used cgo-only sqlite driver: %v", err)
		}
		t.Fatalf("init crypto: %v", err)
	}
	t.Cleanup(func() { _ = helper.Close() })
}

func TestCryptoDatabaseURIWindowsPath(t *testing.T) {
	got := cryptoDatabaseURI(`C:\Users\agent\AppData\Local\Temp\crypto.db`)
	if !strings.HasPrefix(got, "file:///C:/Users/agent/AppData/Local/Temp/crypto.db?") {
		t.Fatalf("cryptoDatabaseURI() = %q", got)
	}
	if strings.Contains(got, `%5C`) {
		t.Fatalf("cryptoDatabaseURI() must not escape Windows separators: %q", got)
	}
}

func TestCryptoDatabaseURIPosixPath(t *testing.T) {
	got := cryptoDatabaseURI("/var/lib/direxio/crypto.db")
	if !strings.HasPrefix(got, "file:///var/lib/direxio/crypto.db?") {
		t.Fatalf("cryptoDatabaseURI() = %q", got)
	}
}

func TestCryptoDatabaseURIUNCPath(t *testing.T) {
	got := cryptoDatabaseURI(`\\fileserver\direxio\crypto.db`)
	if !strings.HasPrefix(got, "file://fileserver/direxio/crypto.db?") {
		t.Fatalf("cryptoDatabaseURI() = %q", got)
	}
}
