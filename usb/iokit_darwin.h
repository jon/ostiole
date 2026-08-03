#ifndef OSTIOLE_IOKIT_DARWIN_H
#define OSTIOLE_IOKIT_DARWIN_H

#include <CoreFoundation/CoreFoundation.h>
#include <IOKit/IOCFPlugIn.h>
#include <IOKit/IOKitLib.h>
#include <IOKit/usb/IOUSBLib.h>
#include <stdint.h>

typedef struct {
  uint16_t vid;
  uint16_t pid;
  uint32_t location;
  uint8_t address;
} ostiole_usb_attachment;

kern_return_t ostiole_usb_iterator(io_iterator_t* iterator);
int ostiole_usb_attachment_read(io_service_t service,
                                ostiole_usb_attachment* attachment);

#endif
