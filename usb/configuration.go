package usb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	requestTypeStandardDeviceIn = 0x80
	requestGetConfiguration     = 0x08
	requestGetDescriptor        = 0x06
	descriptorDevice            = 0x01
	descriptorConfiguration     = 0x02
	descriptorInterface         = 0x04
	descriptorEndpoint          = 0x05
	classAudio                  = 0x01
)

// TransferType identifies a USB endpoint transfer type.
type TransferType uint8

const (
	// TransferControl identifies a control endpoint.
	TransferControl TransferType = iota
	// TransferIsochronous identifies an isochronous endpoint.
	TransferIsochronous
	// TransferBulk identifies a bulk endpoint.
	TransferBulk
	// TransferInterrupt identifies an interrupt endpoint.
	TransferInterrupt
)

// Configuration is a snapshot of one active USB configuration.
type Configuration struct {
	Value      uint8
	Interfaces []Interface
}

// Interface groups the alternate settings for one USB interface number.
type Interface struct {
	Number     uint8
	Alternates []AlternateSetting
}

// AlternateSetting describes one USB interface alternate setting.
type AlternateSetting struct {
	Number    uint8
	Class     uint8
	Subclass  uint8
	Protocol  uint8
	Endpoints []Endpoint
}

// Endpoint describes one endpoint in an interface alternate setting.
type Endpoint struct {
	Address       uint8
	TransferType  TransferType
	MaxPacketSize uint16
}

type controlTransferer interface {
	ControlTransfer(context.Context, uint8, uint8, uint16, uint16, []byte) (int, error)
}

// ActiveConfiguration returns a detached snapshot of the device's active USB
// configuration. It does not claim an interface or change device state.
func (d *Device) ActiveConfiguration(ctx context.Context) (Configuration, error) {
	if d == nil {
		return Configuration{}, errors.New("usb: nil device")
	}
	return activeConfiguration(ctx, d)
}

func activeConfiguration(ctx context.Context, device controlTransferer) (Configuration, error) {
	value, err := activeConfigurationValue(ctx, device)
	if err != nil {
		return Configuration{}, err
	}
	count, err := configurationCount(ctx, device)
	if err != nil {
		return Configuration{}, err
	}
	var active *configurationHeader
	for index := range count {
		header, err := configurationHeaderAt(ctx, device, index)
		if err != nil {
			return Configuration{}, err
		}
		if header.value != value {
			continue
		}
		if active != nil {
			return Configuration{}, fmt.Errorf("usb: active configuration value %d appears at indices %d and %d", value, active.index, index)
		}
		active = &header
	}
	if active == nil {
		return Configuration{}, fmt.Errorf("usb: active configuration value %d has no descriptor", value)
	}
	configuration, err := configurationAt(ctx, device, *active, value)
	if err != nil {
		return Configuration{}, err
	}
	current, err := configurationValue(ctx, device)
	if err != nil {
		return Configuration{}, fmt.Errorf("usb: re-read active configuration: %w", err)
	}
	if current != value {
		return Configuration{}, fmt.Errorf("usb: active configuration changed from %d to %d", value, current)
	}
	return configuration, nil
}

func activeConfigurationValue(ctx context.Context, device controlTransferer) (uint8, error) {
	value, err := configurationValue(ctx, device)
	if err != nil {
		return 0, fmt.Errorf("usb: read active configuration: %w", err)
	}
	if value == 0 {
		return 0, errors.New("usb: device is not configured")
	}
	return value, nil
}

func configurationValue(ctx context.Context, device controlTransferer) (uint8, error) {
	value := make([]byte, 1)
	if err := readStandard(ctx, device, requestGetConfiguration, 0, value); err != nil {
		return 0, err
	}
	return value[0], nil
}

func configurationCount(ctx context.Context, device controlTransferer) (int, error) {
	deviceDescriptor := make([]byte, 18)
	if err := readStandard(ctx, device, requestGetDescriptor, descriptorDevice<<8, deviceDescriptor); err != nil {
		return 0, fmt.Errorf("usb: read device descriptor: %w", err)
	}
	if int(deviceDescriptor[0]) != len(deviceDescriptor) || deviceDescriptor[1] != descriptorDevice || deviceDescriptor[17] == 0 {
		return 0, errors.New("usb: malformed device descriptor")
	}
	return int(deviceDescriptor[17]), nil
}

type configurationHeader struct {
	index int
	value uint8
	total int
}

func configurationHeaderAt(ctx context.Context, device controlTransferer, index int) (configurationHeader, error) {
	header := make([]byte, 9)
	descriptorValue := uint16(descriptorConfiguration)<<8 | uint16(index)
	if err := readStandard(ctx, device, requestGetDescriptor, descriptorValue, header); err != nil {
		return configurationHeader{}, fmt.Errorf("usb: read configuration %d header: %w", index, err)
	}
	if int(header[0]) != len(header) || header[1] != descriptorConfiguration {
		return configurationHeader{}, fmt.Errorf("usb: malformed configuration %d header", index)
	}
	return configurationHeader{index: index, value: header[5], total: int(binary.LittleEndian.Uint16(header[2:4]))}, nil
}

func configurationAt(ctx context.Context, device controlTransferer, header configurationHeader, active uint8) (Configuration, error) {
	if header.total < 9 {
		return Configuration{}, fmt.Errorf("usb: invalid configuration %d length %d", header.index, header.total)
	}
	raw := make([]byte, header.total)
	descriptorValue := uint16(descriptorConfiguration)<<8 | uint16(header.index)
	if err := readStandard(ctx, device, requestGetDescriptor, descriptorValue, raw); err != nil {
		return Configuration{}, fmt.Errorf("usb: read configuration %d: %w", header.index, err)
	}
	if raw[5] != active {
		return Configuration{}, fmt.Errorf("usb: configuration %d value changed from %d to %d", header.index, active, raw[5])
	}
	configuration, err := parseConfiguration(raw)
	if err != nil {
		return Configuration{}, fmt.Errorf("usb: parse configuration %d: %w", header.index, err)
	}
	return configuration, nil
}

func readStandard(ctx context.Context, device controlTransferer, request uint8, value uint16, data []byte) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	count, err := device.ControlTransfer(ctx, requestTypeStandardDeviceIn, request, value, 0, data)
	if err != nil {
		return err
	}
	if count != len(data) {
		return fmt.Errorf("%w: got %d of %d bytes", io.ErrUnexpectedEOF, count, len(data))
	}
	return nil
}

func parseConfiguration(raw []byte) (Configuration, error) {
	if !validConfigurationHeader(raw) {
		return Configuration{}, errors.New("usb: malformed configuration descriptor")
	}
	parser := configurationParser{
		configuration: Configuration{Value: raw[5]}, interfaces: make(map[uint8]int),
		defaultAlternates:  make(map[uint8]bool),
		endpointInterfaces: make(map[uint8]uint8), endpointNumberInterfaces: make(map[uint8]map[uint8]bool),
		controlEndpointInterfaces: make(map[uint8]uint8),
	}
	for position := int(raw[0]); position < len(raw); {
		descriptor, next, err := configurationDescriptor(raw, position)
		if err != nil {
			return Configuration{}, err
		}
		switch descriptor[1] {
		case descriptorInterface:
			err = parser.addInterface(descriptor, position)
		case descriptorEndpoint:
			err = parser.addEndpoint(descriptor, position)
		}
		if err != nil {
			return Configuration{}, err
		}
		position = next
	}
	if err := parser.finish(int(raw[4])); err != nil {
		return Configuration{}, err
	}
	return parser.configuration, nil
}

func validConfigurationHeader(raw []byte) bool {
	if len(raw) < 9 {
		return false
	}
	return raw[0] == 9 && raw[1] == descriptorConfiguration && int(binary.LittleEndian.Uint16(raw[2:4])) == len(raw) && raw[7]&0x80 != 0 && raw[7]&0x1f == 0
}

type configurationParser struct {
	configuration             Configuration
	interfaces                map[uint8]int
	defaultAlternates         map[uint8]bool
	endpointInterfaces        map[uint8]uint8
	endpointNumberInterfaces  map[uint8]map[uint8]bool
	controlEndpointInterfaces map[uint8]uint8
	alternate                 *AlternateSetting
	alternateEndpoints        map[uint8]bool
	alternateEndpointNumbers  map[uint8]uint8
	interfaceNumber           uint8
	wantEndpoints             int
}

const (
	endpointNumberSeen = 1 << iota
	controlEndpointSeen
)

func configurationDescriptor(raw []byte, position int) ([]byte, int, error) {
	length := int(raw[position])
	if length < 2 || position+length > len(raw) {
		return nil, 0, fmt.Errorf("usb: malformed descriptor at offset %d", position)
	}
	return raw[position : position+length], position + length, nil
}

func (p *configurationParser) addInterface(descriptor []byte, position int) error {
	if len(descriptor) != 9 {
		return fmt.Errorf("usb: invalid interface descriptor length %d at offset %d", len(descriptor), position)
	}
	if err := p.finishAlternate(); err != nil {
		return err
	}
	iface := p.interfaceFor(descriptor[2])
	for _, existing := range iface.Alternates {
		if existing.Number == descriptor[3] {
			return fmt.Errorf("usb: duplicate interface %d alternate %d", descriptor[2], descriptor[3])
		}
	}
	iface.Alternates = append(iface.Alternates, AlternateSetting{
		Number: descriptor[3], Class: descriptor[5], Subclass: descriptor[6], Protocol: descriptor[7],
	})
	if descriptor[3] == 0 {
		p.defaultAlternates[descriptor[2]] = true
	}
	p.alternate = &iface.Alternates[len(iface.Alternates)-1]
	p.alternateEndpoints = make(map[uint8]bool)
	p.alternateEndpointNumbers = make(map[uint8]uint8)
	p.interfaceNumber = descriptor[2]
	p.wantEndpoints = int(descriptor[4])
	return nil
}

func (p *configurationParser) interfaceFor(number uint8) *Interface {
	index, ok := p.interfaces[number]
	if !ok {
		index = len(p.configuration.Interfaces)
		p.interfaces[number] = index
		p.configuration.Interfaces = append(p.configuration.Interfaces, Interface{Number: number})
	}
	return &p.configuration.Interfaces[index]
}

func (p *configurationParser) addEndpoint(descriptor []byte, position int) error {
	if p.alternate == nil {
		return fmt.Errorf("usb: invalid endpoint descriptor at offset %d", position)
	}
	endpoint, err := endpointFromDescriptor(descriptor, position, p.alternate.Class)
	if err != nil {
		return err
	}
	if err := p.validateEndpoint(endpoint.Address, endpoint.TransferType); err != nil {
		return err
	}
	p.recordEndpoint(endpoint.Address, endpoint.TransferType)
	p.alternate.Endpoints = append(p.alternate.Endpoints, endpoint)
	return nil
}

func endpointFromDescriptor(descriptor []byte, position int, class uint8) (Endpoint, error) {
	if len(descriptor) != 7 && len(descriptor) != 9 {
		return Endpoint{}, fmt.Errorf("usb: invalid endpoint descriptor length %d at offset %d", len(descriptor), position)
	}
	if len(descriptor) == 9 && class != classAudio {
		return Endpoint{}, fmt.Errorf("usb: nine-byte endpoint descriptor outside audio interface at offset %d", position)
	}
	address := descriptor[2]
	if address&0x0f == 0 || address&0x70 != 0 {
		return Endpoint{}, fmt.Errorf("usb: invalid endpoint address %#02x at offset %d", address, position)
	}
	attributes := descriptor[3]
	transferType := TransferType(attributes & 0x03)
	if !validEndpointAttributes(attributes, transferType) {
		return Endpoint{}, fmt.Errorf("usb: invalid endpoint attributes %#02x at offset %d", attributes, position)
	}
	encodedPacketSize := binary.LittleEndian.Uint16(descriptor[4:6])
	if !validEndpointInterval(descriptor[6], transferType, encodedPacketSize) {
		return Endpoint{}, fmt.Errorf("usb: invalid endpoint interval %d at offset %d", descriptor[6], position)
	}
	maxPacketSize, ok := endpointMaxPacketSize(encodedPacketSize, transferType)
	if !ok {
		return Endpoint{}, fmt.Errorf("usb: invalid endpoint maximum packet size %#04x at offset %d", encodedPacketSize, position)
	}
	return Endpoint{
		Address: address, TransferType: transferType, MaxPacketSize: maxPacketSize,
	}, nil
}

func validEndpointAttributes(attributes uint8, transferType TransferType) bool {
	if transferType != TransferIsochronous {
		return true
	}
	usage := attributes & 0x30
	return usage != 0x10 || attributes&0x0c == 0
}

func validEndpointInterval(interval uint8, transferType TransferType, encodedPacketSize uint16) bool {
	if transferType == TransferIsochronous && (interval == 0 || interval > 16) {
		return false
	}
	if transferType == TransferInterrupt && interval == 0 {
		return false
	}
	periodic := transferType == TransferIsochronous || transferType == TransferInterrupt
	return !periodic || encodedPacketSize&0x1800 == 0 || interval == 1
}

func endpointMaxPacketSize(encoded uint16, transferType TransferType) (uint16, bool) {
	transactions := encoded & 0x1800
	if encoded&0xe000 != 0 || !validEndpointTransactions(transactions, transferType) {
		return 0, false
	}
	size := encoded & 0x07ff
	minimum := endpointPacketSizeMinimum(transactions)
	return size, size <= 1024 && (size >= minimum || transactions == 0 && size == 0 && transferType == TransferInterrupt)
}

func validEndpointTransactions(transactions uint16, transferType TransferType) bool {
	return transactions != 0x1800 && (transactions == 0 || transferType == TransferIsochronous || transferType == TransferInterrupt)
}

func endpointPacketSizeMinimum(transactions uint16) uint16 {
	switch transactions {
	case 0:
		return 1
	case 0x0800:
		return 513
	default:
		return 683
	}
}

func (p *configurationParser) validateEndpoint(address uint8, transferType TransferType) error {
	number := address & 0x0f
	if p.alternateEndpoints[address] {
		return fmt.Errorf("usb: duplicate endpoint %#02x in interface %d alternate %d", address, p.interfaceNumber, p.alternate.Number)
	}
	seen := p.alternateEndpointNumbers[number]
	if transferType == TransferControl && seen&endpointNumberSeen != 0 || seen&controlEndpointSeen != 0 {
		return fmt.Errorf("usb: conflicting control endpoint number %d in interface %d alternate %d", number, p.interfaceNumber, p.alternate.Number)
	}
	if owner, ok := p.endpointInterfaces[address]; ok && owner != p.interfaceNumber {
		return fmt.Errorf("usb: endpoint %#02x belongs to interfaces %d and %d", address, owner, p.interfaceNumber)
	}
	if owner, ok := p.controlEndpointInterfaces[number]; ok && owner != p.interfaceNumber {
		return fmt.Errorf("usb: control endpoint number %d belongs to interfaces %d and %d", number, owner, p.interfaceNumber)
	}
	if transferType == TransferControl {
		for owner := range p.endpointNumberInterfaces[number] {
			if owner != p.interfaceNumber {
				return fmt.Errorf("usb: control endpoint number %d belongs to interfaces %d and %d", number, owner, p.interfaceNumber)
			}
		}
	}
	return nil
}

func (p *configurationParser) recordEndpoint(address uint8, transferType TransferType) {
	number := address & 0x0f
	p.alternateEndpoints[address] = true
	p.alternateEndpointNumbers[number] |= endpointNumberSeen
	if transferType == TransferControl {
		p.alternateEndpointNumbers[number] |= controlEndpointSeen
		p.controlEndpointInterfaces[number] = p.interfaceNumber
	}
	p.endpointInterfaces[address] = p.interfaceNumber
	owners := p.endpointNumberInterfaces[number]
	if owners == nil {
		owners = make(map[uint8]bool)
		p.endpointNumberInterfaces[number] = owners
	}
	owners[p.interfaceNumber] = true
}

func (p *configurationParser) finishAlternate() error {
	if p.alternate != nil && len(p.alternate.Endpoints) != p.wantEndpoints {
		return errors.New("usb: interface endpoint count does not match its descriptor")
	}
	return nil
}

func (p *configurationParser) finish(wantInterfaces int) error {
	if err := p.finishAlternate(); err != nil {
		return err
	}
	if wantInterfaces == 0 {
		return errors.New("usb: configuration descriptor has no interfaces")
	}
	if len(p.configuration.Interfaces) != wantInterfaces {
		return errors.New("usb: interface count does not match configuration descriptor")
	}
	for _, iface := range p.configuration.Interfaces {
		if int(iface.Number) >= wantInterfaces {
			return fmt.Errorf("usb: interface %d is outside configuration interface count %d", iface.Number, wantInterfaces)
		}
		if !p.defaultAlternates[iface.Number] {
			return fmt.Errorf("usb: interface %d has no alternate setting zero", iface.Number)
		}
		for _, alternate := range iface.Alternates {
			if int(alternate.Number) >= len(iface.Alternates) {
				return fmt.Errorf("usb: interface %d alternate setting %d is outside range 0 through %d", iface.Number, alternate.Number, len(iface.Alternates)-1)
			}
		}
	}
	return nil
}
