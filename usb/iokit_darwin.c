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
