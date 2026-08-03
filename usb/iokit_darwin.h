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

typedef struct {
  IOUSBInterfaceInterface300** interface;
} ostiole_usb_interface;

typedef struct {
  uint8_t endpoint;
  uint8_t ref;
  uint8_t transfer_type;
} ostiole_usb_pipe;

kern_return_t ostiole_usb_iterator(io_iterator_t* iterator);
int ostiole_usb_attachment_read(io_service_t service,
                                ostiole_usb_attachment* attachment);
ostiole_usb_device* ostiole_usb_device_open(io_service_t service,
                                            kern_return_t* result);
ostiole_usb_device_close_results ostiole_usb_device_close(
    ostiole_usb_device* opened);
ostiole_usb_interface* ostiole_usb_find_interface(ostiole_usb_device* opened,
                                                  uint8_t wanted,
                                                  kern_return_t* result);
kern_return_t ostiole_usb_interface_open_seize(
    ostiole_usb_interface* interface);
kern_return_t ostiole_usb_interface_set_alternate(
    ostiole_usb_interface* interface, uint8_t alternate);
kern_return_t ostiole_usb_interface_pipe_count(ostiole_usb_interface* interface,
                                               uint8_t* count);
kern_return_t ostiole_usb_interface_pipe(ostiole_usb_interface* interface,
                                         uint8_t ref, ostiole_usb_pipe* pipe);
kern_return_t ostiole_usb_interface_close(ostiole_usb_interface* interface);

#endif
