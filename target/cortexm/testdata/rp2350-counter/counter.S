.syntax unified
.cpu cortex-m33
.thumb
.section .text, "ax", %progbits
.thumb_func
.global counter_start
counter_start:
    cpsid i
    ldr r1, =0x20040000
    movs r0, #0
counter_loop:
    adds r0, #1
    str r0, [r1]
    b counter_loop
.ltorg
