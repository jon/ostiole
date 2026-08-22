package sim_test

import (
	"context"
	"testing"
	"time"

	dapsim "github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestPostedAccessPortReads(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	addAPFixture(t, target, 0, 0x04770031)
	conn := enteredConn(t, target)
	selectAP(t, conn, 0, 0x0f)

	posted, err := conn.ReadAP(t.Context(), 0x0c)
	if err != nil {
		t.Fatal(err)
	}
	if posted != 0 {
		t.Fatalf("posted response = %#08x, want 0", posted)
	}
	value, err := conn.ReadDP(t.Context(), 0x0c)
	if err != nil {
		t.Fatal(err)
	}
	if value != 0x04770031 {
		t.Fatalf("RDBUFF = %#08x, want 0x04770031", value)
	}
}

func TestSelectedAccessPortsRemainIndependent(t *testing.T) {
	target := dapsim.New(0x2ba01477)
	addAPFixture(t, target, 1, 0x14770031)
	conn := enteredConn(t, target)

	selectAP(t, conn, 1, 0)
	if err := conn.WriteAP(t.Context(), 0x00, 0x12345678); err != nil {
		t.Fatal(err)
	}
	if got := readPosted(t, conn, 1, 0, 0); got != 0x12345678 {
		t.Fatalf("AP1 register = %#08x, want 0x12345678", got)
	}
	if got := readPosted(t, conn, 0, 0x0f, 0x0c); got != 0 {
		t.Fatalf("absent AP identity = %#08x, want 0", got)
	}
}

func enteredConn(t *testing.T, target swdsim.Target) *swd.Conn {
	t.Helper()
	conn := swd.New(swdsim.New(target))
	if _, err := conn.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := conn.Release(ctx); err != nil {
			t.Errorf("Release(): %v", err)
		}
	})
	return conn
}

func selectAP(t *testing.T, conn *swd.Conn, ap, bank uint8) {
	t.Helper()
	value := uint32(ap)<<24 | uint32(bank)<<4
	if err := conn.WriteDP(t.Context(), 0x08, value); err != nil {
		t.Fatal(err)
	}
}

func readPosted(t *testing.T, conn *swd.Conn, ap, bank, addr uint8) uint32 {
	t.Helper()
	selectAP(t, conn, ap, bank)
	if _, err := conn.ReadAP(t.Context(), addr); err != nil {
		t.Fatal(err)
	}
	value, err := conn.ReadDP(t.Context(), 0x0c)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
