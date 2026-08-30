package cmsisdap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	commandInfo = 0x00

	infoVendor          = 0x01
	infoProduct         = 0x02
	infoSerial          = 0x03
	infoProtocolVersion = 0x04
	infoFirmwareVersion = 0x09
	infoCapabilities    = 0xf0
	infoPacketCount     = 0xfe
	infoPacketSize      = 0xff

	minimumPacketSize = 4
)

func (s *Session) readInfo(ctx context.Context) error {
	if err := s.readPacketGeometry(ctx); err != nil {
		return err
	}
	if err := s.readCapabilities(ctx); err != nil {
		return err
	}
	return s.readStrings(ctx)
}

func (s *Session) readPacketGeometry(ctx context.Context) error {
	packetSize, present, err := s.infoData(ctx, infoPacketSize)
	if err != nil {
		return err
	}
	if present {
		if len(packetSize) != 2 {
			return fmt.Errorf("cmsisdap: DAP_Info packet-size length = %d, want 2", len(packetSize))
		}
		size := int(binary.LittleEndian.Uint16(packetSize))
		if size < minimumPacketSize {
			return fmt.Errorf("cmsisdap: DAP_Info packet size = %d, want at least %d", size, minimumPacketSize)
		}
		s.packetSize = size
	}
	s.info.PacketSize = s.packetSize

	packetCount, present, err := s.infoData(ctx, infoPacketCount)
	if err != nil {
		return err
	}
	s.info.PacketCount = 1
	if present {
		if len(packetCount) != 1 || packetCount[0] == 0 {
			return fmt.Errorf("cmsisdap: invalid DAP_Info packet count %x", packetCount)
		}
		s.info.PacketCount = int(packetCount[0])
	}
	return nil
}

func (s *Session) readCapabilities(ctx context.Context) error {
	capabilities, present, err := s.infoData(ctx, infoCapabilities)
	if err != nil {
		return err
	}
	if !present || len(capabilities) < 1 || len(capabilities) > 2 {
		return fmt.Errorf("cmsisdap: DAP_Info capabilities length = %d, want 1 or 2", len(capabilities))
	}
	s.info.Capabilities = Capabilities{bytes: capabilities}
	return nil
}

func (s *Session) readStrings(ctx context.Context) error {
	protocolVersion, present, err := s.stringInfo(ctx, infoProtocolVersion)
	if err != nil {
		return err
	}
	if !present || protocolVersion == "" {
		return errors.New("cmsisdap: DAP_Info returned no protocol version")
	}
	s.info.ProtocolVersion = protocolVersion

	if s.info.Vendor, _, err = s.stringInfo(ctx, infoVendor); err != nil {
		return err
	}
	if s.info.Product, present, err = s.stringInfo(ctx, infoProduct); err != nil {
		return err
	}
	if !present {
		s.info.Product = s.info.USB.Product
	}
	if s.info.Serial, present, err = s.stringInfo(ctx, infoSerial); err != nil {
		return err
	}
	if !present {
		s.info.Serial = s.info.USB.Serial
	}
	s.info.FirmwareVersion, _, err = s.stringInfo(ctx, infoFirmwareVersion)
	return err
}

func (s *Session) infoData(ctx context.Context, id byte) ([]byte, bool, error) {
	response, err := s.exchange(ctx, []byte{commandInfo, id})
	if err != nil {
		return nil, false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): %w", id, err)
	}
	if len(response) != 0 && response[0] == 0xff {
		return nil, false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): command is not implemented", id)
	}
	if len(response) < 2 {
		return nil, false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): short response", id)
	}
	if response[0] != commandInfo {
		return nil, false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): response command = %#02x", id, response[0])
	}
	length := int(response[1])
	if length > s.packetSize-2 {
		return nil, false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): declared length %d exceeds packet size %d", id, length, s.packetSize)
	}
	if len(response) < 2+length {
		return nil, false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): got %d bytes, need %d", id, len(response), 2+length)
	}
	if length == 0 {
		return nil, false, nil
	}
	return append([]byte(nil), response[2:2+length]...), true, nil
}

func (s *Session) stringInfo(ctx context.Context, id byte) (string, bool, error) {
	data, present, err := s.infoData(ctx, id)
	if err != nil || !present {
		return "", present, err
	}
	if data[len(data)-1] != 0 {
		return "", false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): string is not NUL-terminated", id)
	}
	data = data[:len(data)-1]
	for _, value := range data {
		if value == 0 {
			return "", false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): string contains an embedded NUL", id)
		}
	}
	if !utf8.Valid(data) {
		return "", false, fmt.Errorf("cmsisdap: DAP_Info(%#02x): string is not UTF-8", id)
	}
	return string(data), true, nil
}
