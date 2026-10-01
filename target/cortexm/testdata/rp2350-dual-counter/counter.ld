ENTRY(counter_start0)
SECTIONS {
    .counters 0x20040000 : { *(.counters) }
    .core0 0x20040020 : { *(.text.core0) }
    .core1 0x20040060 : { *(.text.core1) }
}
