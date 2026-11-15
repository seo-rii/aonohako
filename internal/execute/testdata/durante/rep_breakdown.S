/* Exact REP work with no libc startup: 65 MOVS bytes, 66 STOS bytes,
 * three entries of each kind including a zero-count entry. Verify memory
 * independently so a correct count cannot hide changed program behavior. */
.global _start
.text
_start:
    cld
    lea source(%rip), %rsi
    lea destination(%rip), %rdi
    mov $37, %ecx
    rep movsb
    cmpb $0x5a, destination(%rip)
    jne failure
    cmpb $0x5a, destination+36(%rip)
    jne failure
    lea destination(%rip), %rdi
    mov $0x4141, %eax
    mov $13, %ecx
    rep stosw
    cmpb $0x41, destination(%rip)
    jne failure
    cmpb $0x41, destination+25(%rip)
    jne failure
    lea source(%rip), %rsi
    lea destination(%rip), %rdi
    mov $7, %ecx
    rep movsl
    cmpb $0x5a, destination+27(%rip)
    jne failure
    lea destination(%rip), %rdi
    movabs $0x4242424242424242, %rax
    mov $5, %ecx
    rep stosq
    cmpb $0x42, destination+39(%rip)
    jne failure
    xor %ecx, %ecx
    rep movsq
    rep stosb
    cmpb $0x42, destination+39(%rip)
    jne failure
    mov $1, %eax
    mov $1, %edi
    lea message(%rip), %rsi
    mov $(message_end-message), %edx
    syscall
    cmp $(message_end-message), %rax
    jne failure
    mov $60, %eax
    xor %edi, %edi
    syscall
failure:
    mov $60, %eax
    mov $91, %edi
    syscall
.section .rodata
source: .fill 128, 1, 0x5a
message: .ascii "rep-breakdown-ok\n"
message_end:
.bss
.align 16
destination: .skip 128
.section .note.GNU-stack,"",@progbits
