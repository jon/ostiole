ENTRY(reset_handler)
MEMORY {
    FLASH (rx) : ORIGIN = 0, LENGTH = 256K
}
SECTIONS {
    .vectors : { KEEP(*(.vectors)) } > FLASH
    .text : { *(.text*) } > FLASH
    /DISCARD/ : { *(.comment) *(.ARM.attributes) }
}
