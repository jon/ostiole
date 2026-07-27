package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/jon/ostiole/ftdi"
	"github.com/jon/ostiole/swd"
	"github.com/jon/ostiole/usb"
)

const (
	transferTimeout = 5 * time.Second
)

type device struct {
	raw *ftdi.Channel
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), transferTimeout)
	defer cancel()
	dpidr, err := readDPIDR(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("DPIDR=%#08x\n", dpidr)
}

func readDPIDR(ctx context.Context) (value uint32, err error) {
	enumerator := usb.New()
	attachments, err := enumerator.List(ctx, ftdi.SupportedDevices())
	if err != nil {
		return 0, err
	}
	if len(attachments) != 1 {
		return 0, fmt.Errorf(
			"ostiole: require exactly one supported FTDI attachment; found %d",
			len(attachments),
		)
	}
	opened, err := enumerator.Open(ctx, attachments[0])
	if err != nil {
		return 0, err
	}
	channel, err := ftdi.Open(ctx, opened, ftdi.Config{
		ClockHz:   400_000,
		ProductID: attachments[0].PID,
		Port:      ftdi.PortA,
		Interface: ftdi.SWD,
	})
	if err != nil {
		return 0, err
	}
	raw := &device{raw: channel}
	defer func() {
		err = errors.Join(err, raw.raw.Close())
	}()
	if err := raw.jtagToSWD(ctx); err != nil {
		return 0, err
	}
	value, err = raw.transferRead(ctx, 0xa5)
	if err != nil {
		return 0, err
	}
	if value == 0 || value == math.MaxUint32 || value&1 == 0 {
		return 0, fmt.Errorf("ostiole: invalid DPIDR %#08x", value)
	}
	return value, nil
}

func (d *device) jtagToSWD(ctx context.Context) error {
	seq := &swd.Sequence{}
	seq.AppendN(56, true, true)
	seq.AppendByte(true, 0x9e)
	seq.AppendByte(true, 0xe7)
	seq.AppendN(56, true, true)
	seq.AppendN(8, true, false)
	_, err := d.raw.SWDIO(
		ctx,
		seq.Direction(),
		seq.Output(),
		seq.Bits(),
	)
	return err
}

func (d *device) transferRead(
	ctx context.Context,
	request byte,
) (uint32, error) {
	first := &swd.Sequence{}
	first.AppendByte(true, request)
	first.AppendN(4, false, false)
	input, err := d.raw.SWDIO(
		ctx,
		first.Direction(),
		first.Output(),
		first.Bits(),
	)
	if err != nil {
		return 0, err
	}
	ack := byte(0)
	for bit := range 3 {
		if bitAt(input, 9+bit) {
			ack |= 1 << uint(bit)
		}
	}
	if ack != 0b001 {
		return 0, fmt.Errorf("ostiole: SWD ACK=%03b", ack)
	}
	second := &swd.Sequence{}
	second.AppendN(33, false, false)
	second.AppendN(1, false, false)
	second.AppendN(8, true, false)
	input, err = d.raw.SWDIO(
		ctx,
		second.Direction(),
		second.Output(),
		second.Bits(),
	)
	if err != nil {
		return 0, err
	}
	value := extractUint32(input)
	if bitAt(input, 32) != parity32(value) {
		return 0, errors.New("ostiole: SWD read parity error")
	}
	return value, nil
}

func bitAt(buf []byte, bit int) bool {
	return buf[bit/8]>>(uint(bit)%8)&1 != 0
}

func extractUint32(buf []byte) uint32 {
	var value uint32
	for bit := range 32 {
		if bitAt(buf, bit) {
			value |= 1 << uint(bit)
		}
	}
	return value
}

func parity32(value uint32) bool {
	parity := false
	for bit := range 32 {
		if value>>uint(bit)&1 != 0 {
			parity = !parity
		}
	}
	return parity
}
