ENTRY(counter_start)
SECTIONS {
    . = 0x20040020;
    .text : { *(.text) }
}
