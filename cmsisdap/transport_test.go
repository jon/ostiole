package cmsisdap

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/jon/ostiole/usb"
)

type scriptedUSBClaim struct {
	input, output             *peerBulkTransfer
	inputSubmit, outputSubmit error
	abortErrs                 map[uint8]error
	operations                []string
	inputSize                 int
}

func (*scriptedUSBClaim) SetAltSetting(uint8) error { return nil }

func (*scriptedUSBClaim) Endpoint(context.Context, uint8) (usb.Endpoint, error) {
	return usb.Endpoint{}, errors.New("unexpected endpoint lookup")
}

func (c *scriptedUSBClaim) SubmitBulk(_ context.Context, endpoint uint8, buffer []byte) (usbBulkTransfer, error) {
	if endpoint&0x80 != 0 {
		c.operations = append(c.operations, "submit IN")
		c.inputSize = len(buffer)
		return c.input, c.inputSubmit
	}
	c.operations = append(c.operations, "submit OUT")
	return c.output, c.outputSubmit
}

func (c *scriptedUSBClaim) AbortBulk(endpoint uint8) error {
	if endpoint&0x80 != 0 {
		c.operations = append(c.operations, "abort IN")
	} else {
		c.operations = append(c.operations, "abort OUT")
	}
	return c.abortErrs[endpoint]
}

func (*scriptedUSBClaim) Close() error { return nil }

func TestExchangePrepostsNegotiatedResponseBuffer(t *testing.T) {
	claim := &scriptedUSBClaim{
		input:  &peerBulkTransfer{count: 3},
		output: &peerBulkTransfer{count: 2},
	}
	session := transportSession(claim)
	response, err := session.exchange(context.Background(), []byte{commandInfo, infoVendor})
	if err != nil {
		t.Fatal(err)
	}
	if len(response) != 3 || claim.inputSize != session.packetSize {
		t.Fatalf("response length = %d, input size = %d", len(response), claim.inputSize)
	}
	want := []string{"submit IN", "submit OUT"}
	if !reflect.DeepEqual(claim.operations, want) {
		t.Fatalf("operations = %v, want %v", claim.operations, want)
	}
}

func TestExchangeRejectsFailuresByPhase(t *testing.T) {
	want := errors.New("transfer failed")
	tests := []struct {
		name       string
		configure  func(*scriptedUSBClaim)
		operations []string
		poisoned   bool
		otherError bool
	}{
		{
			name: "submit IN",
			configure: func(claim *scriptedUSBClaim) {
				claim.inputSubmit = want
			},
			operations: []string{"submit IN"},
		},
		{
			name: "submit OUT",
			configure: func(claim *scriptedUSBClaim) {
				claim.outputSubmit = want
			},
			operations: []string{"submit IN", "submit OUT", "abort IN"},
		},
		{
			name: "submit OUT and abort IN",
			configure: func(claim *scriptedUSBClaim) {
				claim.outputSubmit = want
				claim.abortErrs[0x82] = errors.New("abort failed")
			},
			operations: []string{"submit IN", "submit OUT", "abort IN"},
			poisoned:   true,
		},
		{
			name: "complete OUT",
			configure: func(claim *scriptedUSBClaim) {
				claim.output.err = want
			},
			operations: []string{"submit IN", "submit OUT", "abort OUT", "abort IN"},
			poisoned:   true,
		},
		{
			name: "short OUT",
			configure: func(claim *scriptedUSBClaim) {
				claim.output.count = 1
			},
			operations: []string{"submit IN", "submit OUT", "abort IN"},
			poisoned:   true,
			otherError: true,
		},
		{
			name: "complete IN",
			configure: func(claim *scriptedUSBClaim) {
				claim.input.err = want
			},
			operations: []string{"submit IN", "submit OUT", "abort IN"},
			poisoned:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := &scriptedUSBClaim{
				input:     &peerBulkTransfer{count: 2},
				output:    &peerBulkTransfer{count: 2},
				abortErrs: make(map[uint8]error),
			}
			test.configure(claim)
			session := transportSession(claim)
			_, err := session.exchange(context.Background(), []byte{commandInfo, infoVendor})
			if !test.otherError && !errors.Is(err, want) {
				t.Fatalf("exchange() error = %v, want %v", err, want)
			}
			if errors.Is(err, ErrSessionPoisoned) != test.poisoned || session.poisoned != test.poisoned {
				t.Fatalf("exchange() error = %v, session poisoned = %t", err, session.poisoned)
			}
			if !reflect.DeepEqual(claim.operations, test.operations) {
				t.Fatalf("operations = %v, want %v", claim.operations, test.operations)
			}
		})
	}
}

func TestExchangePoisonsInvalidInputCompletion(t *testing.T) {
	tests := []struct {
		name  string
		count int
		text  string
		want  error
	}{
		{name: "negative", count: -1, text: "invalid count -1"},
		{name: "oversized", count: 65, text: "invalid count 65"},
		{name: "zero", count: 0, text: "made no progress", want: io.ErrNoProgress},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claim := &scriptedUSBClaim{input: &peerBulkTransfer{count: test.count}, output: &peerBulkTransfer{count: 2}}
			session := transportSession(claim)
			_, err := session.exchange(context.Background(), []byte{commandInfo, infoVendor})
			if !errors.Is(err, ErrSessionPoisoned) || !strings.Contains(err.Error(), test.text) {
				t.Fatalf("exchange() error = %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("exchange() error = %v, want %v", err, test.want)
			}
			before := append([]string(nil), claim.operations...)
			if _, err := session.exchange(context.Background(), []byte{commandInfo, infoVendor}); !errors.Is(err, ErrSessionPoisoned) {
				t.Fatalf("second exchange() error = %v", err)
			}
			if !reflect.DeepEqual(claim.operations, before) {
				t.Fatalf("second exchange operations = %v, want %v", claim.operations, before)
			}
		})
	}
}

func transportSession(claim usbClaim) *Session {
	return &Session{
		device:     &peerUSBDevice{},
		claim:      claim,
		command:    commandInterface{bulkOut: usb.Endpoint{Address: 0x01}, bulkIn: usb.Endpoint{Address: 0x82}},
		packetSize: 64,
	}
}
