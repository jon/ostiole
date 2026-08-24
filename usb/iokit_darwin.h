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
  CFRunLoopSourceRef source;
} ostiole_usb_interface;

typedef struct {
  uint8_t endpoint;
  uint8_t ref;
  uint8_t transfer_type;
  uint16_t max_packet_size;
} ostiole_usb_pipe;

typedef struct {
  kern_return_t result;
  kern_return_t cleanup;
  uint32_t done;
} ostiole_usb_transfer_result;

typedef struct ostiole_usb_bulk_engine ostiole_usb_bulk_engine;
typedef struct ostiole_usb_bulk_transfer ostiole_usb_bulk_transfer;

typedef struct {
  int available;
  ostiole_usb_transfer_result transfer;
} ostiole_usb_transfer_event;

kern_return_t ostiole_usb_iterator(io_iterator_t* iterator);
int ostiole_usb_attachment_read(io_service_t service,
                                ostiole_usb_attachment* attachment);
ostiole_usb_device* ostiole_usb_device_open(io_service_t service,
                                            kern_return_t* result);
ostiole_usb_device_close_results ostiole_usb_device_close(
    ostiole_usb_device* opened);
kern_return_t ostiole_usb_device_control(ostiole_usb_device* opened,
                                         uint8_t request_type, uint8_t request,
                                         uint16_t value, uint16_t index,
                                         void* data, uint16_t length,
                                         uint32_t timeout, uint16_t* done);
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
ostiole_usb_bulk_engine* ostiole_usb_bulk_engine_open(
    ostiole_usb_interface* interface, kern_return_t* result);
ostiole_usb_bulk_transfer* ostiole_usb_bulk_transfer_submit(
    ostiole_usb_bulk_engine* engine, uint8_t pipe_ref, uint8_t input,
    const void* data, uint32_t size, kern_return_t* result);
void ostiole_usb_bulk_engine_poll(ostiole_usb_bulk_engine* engine,
                                  uint32_t timeout);
ostiole_usb_transfer_event ostiole_usb_bulk_transfer_take(
    ostiole_usb_bulk_transfer* transfer, void* data, uint32_t size);
kern_return_t ostiole_usb_bulk_engine_abort(ostiole_usb_bulk_engine* engine,
                                            uint8_t pipe_ref);
void ostiole_usb_bulk_transfer_free(ostiole_usb_bulk_transfer* transfer);
void ostiole_usb_bulk_engine_close(ostiole_usb_bulk_engine* engine);
kern_return_t ostiole_usb_interface_close(ostiole_usb_interface* interface);

#endif
