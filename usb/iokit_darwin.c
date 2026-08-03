#include "iokit_darwin.h"

#include <stdlib.h>

kern_return_t ostiole_usb_iterator(io_iterator_t* iterator) {
  CFMutableDictionaryRef matching = IOServiceMatching("IOUSBHostDevice");
  if (matching == NULL) {
    return kIOReturnNoMemory;
  }
  return IOServiceGetMatchingServices(kIOMainPortDefault, matching, iterator);
}

static int ostiole_registry_uint(io_service_t service, CFStringRef key,
                                 uint32_t* value) {
  CFTypeRef property =
      IORegistryEntryCreateCFProperty(service, key, kCFAllocatorDefault, 0);
  if (property == NULL || CFGetTypeID(property) != CFNumberGetTypeID()) {
    if (property != NULL) {
      CFRelease(property);
    }
    return 0;
  }
  int ok = CFNumberGetValue((CFNumberRef)property, kCFNumberSInt32Type, value);
  CFRelease(property);
  return ok;
}

int ostiole_usb_attachment_read(io_service_t service,
                                ostiole_usb_attachment* attachment) {
  uint32_t vid, pid, location, address;
  if (!ostiole_registry_uint(service, CFSTR("idVendor"), &vid) ||
      !ostiole_registry_uint(service, CFSTR("idProduct"), &pid) ||
      !ostiole_registry_uint(service, CFSTR("locationID"), &location) ||
      !ostiole_registry_uint(service, CFSTR("USB Address"), &address)) {
    return 0;
  }
  attachment->vid = (uint16_t)vid;
  attachment->pid = (uint16_t)pid;
  attachment->location = location;
  attachment->address = (uint8_t)address;
  return 1;
}

ostiole_usb_device* ostiole_usb_device_open(io_service_t service,
                                            kern_return_t* result) {
  IOCFPlugInInterface** plugin = NULL;
  IOUSBDeviceInterface320** device = NULL;
  SInt32 score = 0;
  *result = IOCreatePlugInInterfaceForService(
      service, kIOUSBDeviceUserClientTypeID, kIOCFPlugInInterfaceID, &plugin,
      &score);
  if (*result != kIOReturnSuccess) {
    return NULL;
  }
  HRESULT query = (*plugin)->QueryInterface(
      plugin, CFUUIDGetUUIDBytes(kIOUSBDeviceInterfaceID320), (LPVOID*)&device);
  (*plugin)->Release(plugin);
  if (query != S_OK || device == NULL) {
    if (device != NULL) {
      (*device)->Release(device);
    }
    *result = kIOReturnUnsupported;
    return NULL;
  }
  *result = (*device)->USBDeviceOpen(device);
  if (*result != kIOReturnSuccess) {
    (*device)->Release(device);
    return NULL;
  }
  ostiole_usb_device* opened = calloc(1, sizeof(*opened));
  if (opened == NULL) {
    (*device)->USBDeviceClose(device);
    (*device)->Release(device);
    *result = kIOReturnNoMemory;
    return NULL;
  }
  opened->service = service;
  opened->device = device;
  return opened;
}

ostiole_usb_device_close_results ostiole_usb_device_close(
    ostiole_usb_device* opened) {
  ostiole_usb_device_close_results results;
  results.device_close = (*opened->device)->USBDeviceClose(opened->device);
  (*opened->device)->Release(opened->device);
  results.service_release = IOObjectRelease(opened->service);
  free(opened);
  return results;
}

kern_return_t ostiole_usb_device_control(ostiole_usb_device* opened,
                                         uint8_t request_type, uint8_t request,
                                         uint16_t value, uint16_t index,
                                         void* data, uint16_t length,
                                         uint32_t timeout, uint16_t* done) {
  IOUSBDevRequestTO transfer = {
      .bmRequestType = request_type,
      .bRequest = request,
      .wValue = value,
      .wIndex = index,
      .wLength = length,
      .pData = data,
      .noDataTimeout = timeout,
      .completionTimeout = timeout,
  };
  kern_return_t result =
      (*opened->device)->DeviceRequestTO(opened->device, &transfer);
  *done = (uint16_t)transfer.wLenDone;
  return result;
}

static IOUSBInterfaceInterface300** ostiole_usb_query_interface(
    io_service_t service) {
  IOCFPlugInInterface** plugin = NULL;
  IOUSBInterfaceInterface300** interface = NULL;
  SInt32 score = 0;
  if (IOCreatePlugInInterfaceForService(
          service, kIOUSBInterfaceUserClientTypeID, kIOCFPlugInInterfaceID,
          &plugin, &score) != kIOReturnSuccess) {
    return NULL;
  }
  HRESULT query = (*plugin)->QueryInterface(
      plugin, CFUUIDGetUUIDBytes(kIOUSBInterfaceInterfaceID300),
      (LPVOID*)&interface);
  (*plugin)->Release(plugin);
  if (query != S_OK || interface == NULL) {
    if (interface != NULL) {
      (*interface)->Release(interface);
    }
    return NULL;
  }
  return interface;
}

ostiole_usb_interface* ostiole_usb_find_interface(ostiole_usb_device* opened,
                                                  uint8_t wanted,
                                                  kern_return_t* result) {
  IOUSBFindInterfaceRequest request = {
      kIOUSBFindInterfaceDontCare, kIOUSBFindInterfaceDontCare,
      kIOUSBFindInterfaceDontCare, kIOUSBFindInterfaceDontCare};
  io_iterator_t iterator = 0;
  *result = (*opened->device)
                ->CreateInterfaceIterator(opened->device, &request, &iterator);
  if (*result != kIOReturnSuccess) {
    return NULL;
  }
  io_service_t service;
  while ((service = IOIteratorNext(iterator)) != 0) {
    IOUSBInterfaceInterface300** interface =
        ostiole_usb_query_interface(service);
    IOObjectRelease(service);
    if (interface == NULL) {
      continue;
    }
    UInt8 number = 0;
    *result = (*interface)->GetInterfaceNumber(interface, &number);
    if (*result == kIOReturnSuccess && number == wanted) {
      IOObjectRelease(iterator);
      ostiole_usb_interface* found = calloc(1, sizeof(*found));
      if (found == NULL) {
        (*interface)->Release(interface);
        *result = kIOReturnNoMemory;
        return NULL;
      }
      found->interface = interface;
      return found;
    }
    (*interface)->Release(interface);
  }
  IOObjectRelease(iterator);
  *result = kIOReturnNotFound;
  return NULL;
}

kern_return_t ostiole_usb_interface_open_seize(
    ostiole_usb_interface* interface) {
  return (*interface->interface)->USBInterfaceOpenSeize(interface->interface);
}

kern_return_t ostiole_usb_interface_set_alternate(
    ostiole_usb_interface* interface, uint8_t alternate) {
  return (*interface->interface)
      ->SetAlternateInterface(interface->interface, alternate);
}

kern_return_t ostiole_usb_interface_pipe_count(ostiole_usb_interface* interface,
                                               uint8_t* count) {
  return (*interface->interface)->GetNumEndpoints(interface->interface, count);
}

kern_return_t ostiole_usb_interface_pipe(ostiole_usb_interface* interface,
                                         uint8_t ref, ostiole_usb_pipe* pipe) {
  UInt8 direction, number, interval;
  UInt16 max_packet;
  kern_return_t result =
      (*interface->interface)
          ->GetPipeProperties(interface->interface, ref, &direction, &number,
                              &pipe->transfer_type, &max_packet, &interval);
  if (result != kIOReturnSuccess) {
    return result;
  }
  pipe->endpoint = number | (direction == kUSBIn ? 0x80 : 0);
  pipe->ref = ref;
  return kIOReturnSuccess;
}

kern_return_t ostiole_usb_interface_read(ostiole_usb_interface* interface,
                                         uint8_t ref, void* data,
                                         uint32_t* size, uint32_t timeout) {
  return (*interface->interface)
      ->ReadPipeTO(interface->interface, ref, data, size, timeout, timeout);
}

kern_return_t ostiole_usb_interface_write(ostiole_usb_interface* interface,
                                          uint8_t ref, void* data,
                                          uint32_t size, uint32_t timeout) {
  return (*interface->interface)
      ->WritePipeTO(interface->interface, ref, data, size, timeout, timeout);
}

kern_return_t ostiole_usb_interface_close(ostiole_usb_interface* interface) {
  kern_return_t result =
      (*interface->interface)->USBInterfaceClose(interface->interface);
  (*interface->interface)->Release(interface->interface);
  free(interface);
  return result;
}
