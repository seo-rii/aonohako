#!/usr/bin/env python3
"""A small, reference-compatible Malbolge interpreter.

This follows Ben Olmstead's original 1998 interpreter and specification:
https://www.lscheffer.com/malbolge_interp.html
https://www.lscheffer.com/malbolge_spec.html

Olmstead explicitly placed the language, documentation, and interpreter in
the public domain. The safe handling of malformed runtime memory below avoids
the original C interpreter's out-of-bounds access and non-graphical busy loop.
"""

from __future__ import annotations

import sys


MEMORY_SIZE = 59049
ASCII_WHITESPACE = b" \t\n\r\v\f"
VALID_OPCODES = b"ji*p</vo"
XLAT1 = (
    b"+b(29e*j1VMEKLyC})8&m#~W>qxdRp0wkrUo[D7,XTcA\"lI"
    b".v%{gJh4G\\-=O@5`_3i<?Z';FNQuY]szf$!BS/|t:Pn6^Ha"
)
XLAT2 = (
    b"5z]&gqtyfr$(we4{WP)H-Zn,[%\\3dL+Q;>U!pJS72FhOA1C"
    b"B6v^=I_0/8|jsb9m<.TVac`uY*MK'X~xDl}REokN:#?G\"i@"
)
POWERS_OF_NINE = (1, 9, 81, 729, 6561)
CRAZY_TABLE = (
    (4, 3, 3, 1, 0, 0, 1, 0, 0),
    (4, 3, 5, 1, 0, 2, 1, 0, 2),
    (5, 5, 4, 2, 2, 1, 2, 2, 1),
    (4, 3, 3, 1, 0, 0, 7, 6, 6),
    (4, 3, 5, 1, 0, 2, 7, 6, 8),
    (5, 5, 4, 2, 2, 1, 8, 8, 7),
    (7, 6, 6, 7, 6, 6, 4, 3, 3),
    (7, 6, 8, 7, 6, 8, 4, 3, 5),
    (8, 8, 7, 8, 8, 7, 5, 5, 4),
)


class MalbolgeError(Exception):
    """A deterministic source or command-line error."""


def crazy(x: int, y: int) -> int:
    result = 0
    for power in POWERS_OF_NINE:
        result += CRAZY_TABLE[(y // power) % 9][(x // power) % 9] * power
    return result


def load_program(data: bytes) -> list[int]:
    memory: list[int] = []
    for offset, byte in enumerate(data):
        if byte in ASCII_WHITESPACE:
            continue
        position = len(memory)
        if position >= MEMORY_SIZE:
            raise MalbolgeError("source exceeds 59049 instructions")
        if byte < 33 or byte > 126:
            raise MalbolgeError(
                f"source contains non-graphical ASCII at byte {offset}"
            )
        opcode = XLAT1[(byte - 33 + position) % len(XLAT1)]
        if opcode not in VALID_OPCODES:
            raise MalbolgeError(f"invalid opcode at instruction {position}")
        memory.append(byte)

    if len(memory) < 2:
        raise MalbolgeError("source must contain at least two instructions")

    while len(memory) < MEMORY_SIZE:
        memory.append(crazy(memory[-1], memory[-2]))
    return memory


def execute(memory: list[int], input_stream, output_stream) -> None:
    accumulator = 0
    code = 0
    data = 0

    while True:
        instruction = memory[code]
        if instruction < 33 or instruction > 126:
            return
        opcode = XLAT1[(instruction - 33 + code) % len(XLAT1)]

        if opcode == ord("j"):
            data = memory[data]
        elif opcode == ord("i"):
            code = memory[data]
        elif opcode == ord("*"):
            memory[data] = memory[data] // 3 + memory[data] % 3 * 19683
            accumulator = memory[data]
        elif opcode == ord("p"):
            memory[data] = crazy(accumulator, memory[data])
            accumulator = memory[data]
        elif opcode == ord("<"):
            output_stream.write(bytes((accumulator & 0xFF,)))
            output_stream.flush()
        elif opcode == ord("/"):
            incoming = input_stream.read(1)
            accumulator = incoming[0] if incoming else MEMORY_SIZE - 1
        elif opcode == ord("v"):
            return

        # The i instruction can redirect C before encryption. Treat an
        # invalid destination as the specification's clean non-graphic halt
        # instead of reproducing the original C implementation's undefined
        # XLAT2 access.
        instruction = memory[code]
        if instruction < 33 or instruction > 126:
            return
        memory[code] = XLAT2[instruction - 33]
        code = (code + 1) % MEMORY_SIZE
        data = (data + 1) % MEMORY_SIZE


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: malbolge.py <source>", file=sys.stderr)
        return 1
    try:
        with open(argv[1], "rb") as source:
            memory = load_program(source.read())
        execute(memory, sys.stdin.buffer, sys.stdout.buffer)
    except (MalbolgeError, OSError) as exc:
        print(f"malbolge: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
