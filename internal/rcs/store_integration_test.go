package rcs

import (
	"context"
	"os"
	"testing"

	"github.com/zagnu-labs/rcs-messenger/internal/db"
)

// Runs only against a real Postgres: RCS_TEST_DATABASE_URL=postgres://... go test ./internal/rcs
func TestResultStoreAgainstPostgres(t *testing.T) {
	dsn := os.Getenv("RCS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("RCS_TEST_DATABASE_URL not set")
	}
	conn, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`DELETE FROM rcs_send_results`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	store := NewResultStore(conn)
	const optOut, leaked, clean = "+15550000001", "+15550000002", "+15550000003"

	if ok, err := store.OptedOut(ctx, optOut); err != nil || ok {
		t.Fatalf("nobody has opted out yet: ok=%v err=%v", ok, err)
	}
	if recorded, err := store.BlockNumber(ctx, optOut, "replied STOP over RCS"); err != nil || !recorded {
		t.Fatalf("BlockNumber: recorded=%v err=%v", recorded, err)
	}
	if ok, _ := store.OptedOut(ctx, optOut); !ok {
		t.Error("the number that replied STOP must read as opted out")
	}
	if ok, _ := store.OptedOut(ctx, clean); ok {
		t.Error("an unrelated number must not read as opted out")
	}
	// An opt-out is not an unencrypted-send block and must not masquerade as one.
	if ok, _ := store.Blocked(ctx, optOut); ok {
		t.Error("an opt-out is not a known-unencrypted block")
	}

	if err := store.Record(ctx, SendRecord{Phone: leaked, Outcome: OutcomeSuccessRCS}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.Blocked(ctx, leaked); !ok {
		t.Error("a number whose send went out unencrypted must be blocked")
	}
	if ok, _ := store.OptedOut(ctx, leaked); ok {
		t.Error("an unencrypted send is not an opt-out")
	}

	// End to end through the service: STOP in, refused out.
	f := &fakeSender{enabled: true}
	svc := NewService(f, Config{Enabled: true}).SetResults(store).SetBlocker(store)
	if _, err := svc.HandleInbound(ctx, Inbound{From: clean, Text: "stop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(ctx, clean, "hi"); err == nil {
		t.Fatal("a number that replied STOP must be refused")
	}
	if f.sends != 0 {
		t.Error("nothing may reach the phone after a STOP")
	}
}
