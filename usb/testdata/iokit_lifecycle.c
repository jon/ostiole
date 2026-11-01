#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "../iokit_darwin.h"

static kern_return_t close_result;
static unsigned closes, releases, reads;

static IOReturn close_interface(void* self) {
  (void)self;
  closes++;
  return close_result;
}

static ULONG release_interface(void* self) {
  (void)self;
  releases++;
  return 0;
}

static IOReturn read_pipe(void* self, UInt8 pipe, void* data, UInt32 size,
                          IOAsyncCallback1 callback, void* refcon) {
  (void)self;
  assert(pipe == 3 && size == 1);
  *(unsigned char*)data = 0x5a;
  reads++;
  callback(refcon, kIOReturnSuccess, (void*)(uintptr_t)size);
  return kIOReturnSuccess;
}

static void exercise_live_close(ostiole_usb_interface* interface) {
  close_result = kIOReturnBusy;
  assert(ostiole_usb_interface_close(interface) == kIOReturnBusy);
  assert(closes == 1 && releases == 0);
  assert(interface->source != NULL);
  assert(CFRunLoopSourceIsValid(interface->source));
  kern_return_t result;
  ostiole_usb_bulk_engine* engine =
      ostiole_usb_bulk_engine_open(interface, &result);
  assert(engine != NULL && result == kIOReturnSuccess);
  unsigned char data = 0;
  ostiole_usb_bulk_transfer* transfer =
      ostiole_usb_bulk_transfer_submit(engine, 3, 1, &data, 1, &result);
  assert(transfer != NULL && result == kIOReturnSuccess && reads == 1);
  ostiole_usb_transfer_event event =
      ostiole_usb_bulk_transfer_take(transfer, &data, 1);
  assert(event.available && event.transfer.done == 1 && data == 0x5a);
  ostiole_usb_bulk_transfer_free(transfer);
  ostiole_usb_bulk_engine_close(engine);
  close_result = kIOReturnSuccess;
  assert(ostiole_usb_interface_close(interface) == kIOReturnSuccess);
  assert(closes == 2 && releases == 1);
}

int main(int argc, char** argv) {
  assert(argc == 2);
  IOUSBInterfaceInterface300 table = {0};
  table.USBInterfaceClose = close_interface;
  table.Release = release_interface;
  table.ReadPipeAsync = read_pipe;
  IOUSBInterfaceInterface300* table_ptr = &table;
  ostiole_usb_interface* interface = calloc(1, sizeof(*interface));
  assert(interface != NULL);
  interface->interface = &table_ptr;
  if (strcmp(argv[1], "missing-source") == 0) {
    kern_return_t result;
    assert(ostiole_usb_bulk_engine_open(interface, &result) == NULL);
    assert(result == kIOReturnNotOpen);
    assert(closes == 0 && releases == 0);
    free(interface);
    return 0;
  }
  CFRunLoopSourceContext context = {0};
  interface->source = CFRunLoopSourceCreate(NULL, 0, &context);
  assert(interface->source != NULL);
  CFRunLoopSourceRef source = interface->source;
  CFRetain(source);
  if (strcmp(argv[1], "live-close") == 0) {
    exercise_live_close(interface);
  } else {
    if (strcmp(argv[1], "no-device") == 0) {
      close_result = kIOReturnNoDevice;
    } else if (strcmp(argv[1], "not-attached") == 0) {
      close_result = kIOReturnNotAttached;
    } else {
      assert(strcmp(argv[1], "invalid-destination") == 0);
      close_result = MACH_SEND_INVALID_DEST;
    }
    assert(ostiole_usb_interface_close(interface) == close_result);
    assert(closes == 1 && releases == 1);
  }
  assert(!CFRunLoopSourceIsValid(source));
  CFRelease(source);
  return 0;
}
