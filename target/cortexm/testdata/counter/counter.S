.syntax unified
.cpu cortex-m0
.thumb

.section .vectors, "a", %progbits
.word 0x20004000
.word reset_handler
.rept 46
.word default_handler
.endr

.section .text, "ax", %progbits
.thumb_func
.global reset_handler
reset_handler:
    cpsid i
    ldr r1, =0x20000000
    movs r0, #0
counter_loop:
    adds r0, #1
    str r0, [r1]
    b counter_loop

.thumb_func
default_handler:
    b default_handler
.ltorg
