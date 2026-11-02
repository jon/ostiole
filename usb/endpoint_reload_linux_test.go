//go:build linux

package usb

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestLinuxKnownAlternateReloadsEndpointDescriptors(t *testing.T) {
	for _, loaded := range []bool{false, true} {
		t.Run(fmt.Sprintf("previouslyLoaded=%v", loaded), func(t *testing.T) { checkLinuxAlternateReload(t, loaded) })
	}
}

func checkLinuxAlternateReload(t *testing.T, loaded bool) {
	t.Helper()
	first, second := endpointDescriptor(0x81), endpointDescriptor(0x82)
	binary.LittleEndian.PutUint16(second[4:6], 32)
	raw := rawConfiguration(7, 1, interfaceDescriptor(0, 0, 1), first, interfaceDescriptor(0, 1, 1), second)
	fixture := configurationFixture(t, [][]byte{raw}, 7)
	d := fixture.Device
	reply, err := os.CreateTemp(t.TempDir(), "control-reply")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reply.Close(); err != nil {
			t.Error(err)
		}
	})
	host := &endpointReloadHost{t: t, reply: reply, raw: raw}
	d.ioctl = host.ioctl
	claim, err := d.ClaimInterface(0)
	if err != nil {
		t.Fatal(err)
	}
	if loaded {
		if _, err := claim.Endpoint(t.Context(), 0x81); err != nil {
			t.Fatal(err)
		}
	}
	before := host.gets
	if err := claim.SetAltSetting(1); err != nil {
		t.Fatal(err)
	}
	got, err := claim.Endpoint(t.Context(), 0x82)
	if err != nil || got != (Endpoint{Address: 0x82, TransferType: TransferBulk, MaxPacketSize: 32}) {
		t.Fatalf("alternate endpoint: %+v %v", got, err)
	}
	if _, err := claim.Endpoint(t.Context(), 0x81); err == nil {
		t.Fatal("old alternate endpoint remained active")
	}
	if host.gets != before || host.sets != 1 {
		t.Fatalf("known alternate reread or repeated: gets%d before%d sets%d", host.gets, before, host.sets)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if host.releases != 1 || d.CleanupPending() {
		t.Fatal("interface/device cleanup not complete")
	}
}

type endpointReloadHost struct {
	t                    *testing.T
	reply                *os.File
	raw                  []byte
	alternate            byte
	gets, sets, releases int
}

func (h *endpointReloadHost) ioctl(_ uintptr, request uintptr, arg any) (uintptr, error) {
	switch request {
	case usbfsClaimInterface:
		if *arg.(*uint32) != 0 {
			h.t.Fatal("claimed wrong interface")
		}
		return 0, nil
	case usbfsSetInterface:
		if *arg.(*[2]uint32) != [2]uint32{0, 1} {
			h.t.Fatal("selected wrong alternate")
		}
		h.alternate, h.sets = 1, h.sets+1
		return 0, nil
	case usbfsReleaseInterface:
		h.releases++
		return 0, nil
	case usbfsControl:
		control := arg.(*usbControlTransfer)
		data := endpointReloadReply(h.t, control, h.raw, h.alternate, &h.gets)
		if _, err := h.reply.WriteAt(data, 0); err != nil {
			h.t.Fatal(err)
		}
		if _, err := h.reply.Seek(0, 0); err != nil {
			h.t.Fatal(err)
		}
		count, _, errno := syscall.Syscall(syscall.SYS_READ, h.reply.Fd(), control.Data, uintptr(len(data)))
		if errno != 0 {
			return count, errno
		}
		return count, nil
	default:
		h.t.Fatalf("unexpected ioctl %#x", request)
		return 0, syscall.EINVAL
	}
}

func endpointReloadReply(t *testing.T, control *usbControlTransfer, raw []byte, alternate byte, gets *int) []byte {
	t.Helper()
	switch {
	case control.RequestType == 0x81 && control.Request == requestGetInterface && control.Length == 1:
		*gets++
		return []byte{alternate}
	case control.RequestType == 0x80 && control.Request == requestGetConfiguration && control.Length == 1:
		return []byte{7}
	case control.RequestType == 0x80 && control.Request == requestGetDescriptor && control.Value == 0x100 && control.Length == 18:
		data := make([]byte, 18)
		data[0], data[1], data[17] = 18, 1, 1
		return data
	case control.RequestType == 0x80 && control.Request == requestGetDescriptor && control.Value == 0x200 && control.Length == 9:
		return raw[:9]
	default:
		t.Fatalf("unexpected endpoint metadata request: %+v", control)
		return nil
	}
}

func TestLinuxAttachmentCacheAcceptsExtendedHeaders(t *testing.T) {
	first, second := extendedConfiguration(2, 0, 0), extendedConfiguration(3, 2, 4)
	first[5] = 1
	fixture := configurationFixture(t, [][]byte{first, second}, 7)
	got, err := activeConfiguration(t.Context(), fixture)
	if err != nil || got.Value != 7 || len(got.Interfaces) != 1 {
		t.Fatalf("extended attachment descriptors: %+v %v", got, err)
	}
}

func TestLinuxCacheRejectsHeaderPastConfigurationTotal(t *testing.T) {
	fixture := configurationFixture(t, [][]byte{singleInterfaceConfiguration(1), singleInterfaceConfiguration(7)}, 7)
	if _, err := fixture.file.WriteAt([]byte{255}, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := activeConfiguration(t.Context(), fixture); err == nil {
		t.Fatal("accepted cached header longer than its configuration")
	}
}
