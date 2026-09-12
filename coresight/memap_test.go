package coresight_test

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/jon/ostiole/coresight"
	"github.com/jon/ostiole/dap"
	"github.com/jon/ostiole/dap/sim"
	"github.com/jon/ostiole/swd"
	swdsim "github.com/jon/ostiole/swd/sim"
)

func TestIdentifyThroughMEMAP(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			target := sim.New(0x2ba01477)
			ap := dap.NewAPSel(0)
			if err := target.AddMEMAP(ap, 0x04770021, nil); err != nil {
				t.Fatal(err)
			}
			cfg := uint32(2)
			if order == binary.BigEndian {
				cfg |= 1
			}
			if err := target.SetMEMAPCFG(ap, cfg); err != nil {
				t.Fatal(err)
			}
			const base = 0x100001000
			for address, word := range memoryAt(base, 9).words {
				var bytes [4]byte
				order.PutUint32(bytes[:], word)
				if err := target.SetMEMAPBytes(ap, address, bytes[:]); err != nil {
					t.Fatal(err)
				}
			}
			dp := dap.NewDebugPort(dap.SWDP(swd.New(swdsim.New(target))))
			t.Cleanup(func() { releaseSimulation(t, dp.Release) })
			if _, err := dp.Connect(t.Context()); err != nil {
				t.Fatal(err)
			}
			mem, err := dap.OpenMemAP(t.Context(), dp, ap)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { releaseSimulation(t, mem.Release) })
			got, err := coresight.Identify(t.Context(), mem, base)
			if err != nil {
				t.Fatal(err)
			}
			if got.CIDR != 0xb105900d || got.PIDR != 0x24523bb906 || got.DEVARCH != 0x47721a14 {
				t.Fatalf("identity=%+v", got)
			}
		})
	}
}

func releaseSimulation(t *testing.T, release func(context.Context) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := release(ctx); err != nil {
		t.Error(err)
	}
}
