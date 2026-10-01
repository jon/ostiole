.syntax unified
.cpu cortex-m33
.thumb

.section .counters, "aw", %progbits
.word 0, 0

.macro counter core, address
.section .text.core\core, "ax", %progbits
.thumb_func
.global counter_start\core
counter_start\core:
    cpsid i
    ldr r1, =\address
    movs r0, #0
counter_loop\core:
    adds r0, #1
    str r0, [r1]
    b counter_loop\core
.ltorg
.endm

counter 0, 0x20040000
counter 1, 0x20040004
