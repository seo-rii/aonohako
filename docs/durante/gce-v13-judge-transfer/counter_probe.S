# Countable ET_EXEC without a libc entry point, loader or address-dependent work.
.section .text
.global _start
_start:
    lea src(%rip), %rdi
    mov $4096, %ecx
    mov $7, %eax
    cld
    rep stosb
    lea src(%rip), %rsi
    lea dst(%rip), %rdi
    mov $4096, %ecx
    rep movsb
    mov $20000, %ecx
.Lwork:
    add $3, %eax
    dec %ecx
    jnz .Lwork
    mov $1, %eax
    mov $1, %edi
    lea answer(%rip), %rsi
    mov $3, %edx
    syscall
    mov $60, %eax
    xor %edi, %edi
    syscall
.section .rodata
answer: .ascii "ok\n"
.section .bss
.balign 16
src: .skip 4096
dst: .skip 4096
.section .note.GNU-stack,"",@progbits
