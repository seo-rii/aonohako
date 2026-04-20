#!/usr/bin/env python3
import sys


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: brainfuck.py <program>", file=sys.stderr)
        return 2

    with open(sys.argv[1], "r", encoding="utf-8") as fh:
        program = "".join(ch for ch in fh.read() if ch in "><+-.,[]")

    jump = {}
    stack = []
    for idx, ch in enumerate(program):
        if ch == "[":
            stack.append(idx)
        elif ch == "]":
            if not stack:
                raise ValueError("unmatched closing bracket")
            left = stack.pop()
            jump[left] = idx
            jump[idx] = left
    if stack:
        raise ValueError("unmatched opening bracket")

    tape = {}
    ptr = 0
    pc = 0
    data = sys.stdin.buffer.read()
    data_pos = 0
    out = bytearray()

    while pc < len(program):
        op = program[pc]
        cell = tape.get(ptr, 0)
        if op == ">":
            ptr += 1
        elif op == "<":
            ptr -= 1
        elif op == "+":
            tape[ptr] = (cell + 1) & 0xFF
        elif op == "-":
            tape[ptr] = (cell - 1) & 0xFF
        elif op == ".":
            out.append(cell)
        elif op == ",":
            if data_pos < len(data):
                tape[ptr] = data[data_pos]
                data_pos += 1
            else:
                tape[ptr] = 0
        elif op == "[":
            if cell == 0:
                pc = jump[pc]
        elif op == "]":
            if cell != 0:
                pc = jump[pc]
        pc += 1

    sys.stdout.buffer.write(out)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - surfaced through process exit
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
