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

typedef struct {
  io_service_t service;
  IOUSBDeviceInterface320** device;
} ostiole_usb_device;

typedef struct {
  kern_return_t device_close;
  kern_return_t service_release;
} ostiole_usb_device_close_results;

kern_return_t ostiole_usb_iterator(io_iterator_t* iterator);
int ostiole_usb_attachment_read(io_service_t service,
                                ostiole_usb_attachment* attachment);
ostiole_usb_device* ostiole_usb_device_open(io_service_t service,
                                            kern_return_t* result);
ostiole_usb_device_close_results ostiole_usb_device_close(
    ostiole_usb_device* opened);

#endif
