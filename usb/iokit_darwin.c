#include "iokit_darwin.h"

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
    if (property != NULL) CFRelease(property);
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
