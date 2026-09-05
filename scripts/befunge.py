#!/usr/bin/env python3
import random
import sys


def pop(stack: list[int]) -> int:
    if not stack:
        return 0
    return stack.pop()


_WHITESPACE = frozenset(b" \t\n\r\x0b\x0c")


def read_int(buf: bytes, i: int) -> tuple[int, int]:
    """Read a decimal integer from buf starting at i, scanf("%d")-style.

    Skips leading whitespace, accepts an optional sign, then digits, and stops
    at the first non-digit, returning the value and the advanced cursor so that
    a following `~` continues from the same shared input position. When no digit
    is present (EOF or a non-numeric byte) it yields 0 and leaves the cursor at
    the first non-whitespace byte.
    """
    n = len(buf)
    while i < n and buf[i] in _WHITESPACE:
        i += 1
    start = i
    if i < n and buf[i] in (0x2B, 0x2D):  # '+' or '-'
        i += 1
    digits_start = i
    while i < n and 0x30 <= buf[i] <= 0x39:
        i += 1
    if i == digits_start:
        return 0, start
    return int(buf[start:i].decode("ascii")), i


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: befunge.py <program>", file=sys.stderr)
        return 2

    with open(sys.argv[1], "r", encoding="utf-8", newline="") as fh:
        lines = fh.read().replace("\r\n", "\n").replace("\r", "\n").split("\n")

    if lines and lines[-1] == "":
        lines.pop()
    width = max(80, *(len(line) for line in lines)) if lines else 80
    height = max(25, len(lines))
    grid = [list(line.ljust(width)) for line in lines]
    while len(grid) < height:
        grid.append(list(" " * width))

    data = sys.stdin.buffer.read()
    input_pos = 0  # single shared cursor for both `&` and `~`
    stack: list[int] = []
    x = y = 0
    dx, dy = 1, 0
    string_mode = False

    while True:
        op = grid[y][x]
        if string_mode and op != '"':
            stack.append(ord(op))
        elif op.isdigit():
            stack.append(int(op))
        elif op == "+":
            a = pop(stack)
            b = pop(stack)
            stack.append(b + a)
        elif op == "-":
            a = pop(stack)
            b = pop(stack)
            stack.append(b - a)
        elif op == "*":
            a = pop(stack)
            b = pop(stack)
            stack.append(b * a)
        elif op == "/":
            a = pop(stack)
            b = pop(stack)
            stack.append(0 if a == 0 else b // a)
        elif op == "%":
            a = pop(stack)
            b = pop(stack)
            stack.append(0 if a == 0 else b % a)
        elif op == "!":
            stack.append(1 if pop(stack) == 0 else 0)
        elif op == "`":
            a = pop(stack)
            b = pop(stack)
            stack.append(1 if b > a else 0)
        elif op == ">":
            dx, dy = 1, 0
        elif op == "<":
            dx, dy = -1, 0
        elif op == "^":
            dx, dy = 0, -1
        elif op == "v":
            dx, dy = 0, 1
        elif op == "?":
            dx, dy = random.choice(((1, 0), (-1, 0), (0, 1), (0, -1)))
        elif op == "_":
            dx, dy = (1, 0) if pop(stack) == 0 else (-1, 0)
        elif op == "|":
            dx, dy = (0, 1) if pop(stack) == 0 else (0, -1)
        elif op == '"':
            string_mode = not string_mode
        elif op == ":":
            value = pop(stack)
            stack.append(value)
            stack.append(value)
        elif op == "\\":
            a = pop(stack)
            b = pop(stack)
            stack.append(a)
            stack.append(b)
        elif op == "$":
            pop(stack)
        elif op == ".":
            sys.stdout.write(str(pop(stack)) + " ")
            sys.stdout.flush()
        elif op == ",":
            sys.stdout.write(chr(pop(stack) & 0xFF))
            sys.stdout.flush()
        elif op == "#":
            x = (x + dx) % width
            y = (y + dy) % height
        elif op == "p":
            row = pop(stack)
            col = pop(stack)
            value = pop(stack)
            if 0 <= row < height and 0 <= col < width:
                grid[row][col] = chr(value & 0xFF)
        elif op == "g":
            row = pop(stack)
            col = pop(stack)
            if 0 <= row < height and 0 <= col < width:
                stack.append(ord(grid[row][col]))
            else:
                stack.append(0)
        elif op == "&":
            value, input_pos = read_int(data, input_pos)
            stack.append(value)
        elif op == "~":
            if input_pos < len(data):
                stack.append(data[input_pos])
                input_pos += 1
            else:
                stack.append(-1)
        elif op == "@":
            return 0

        x = (x + dx) % width
        y = (y + dy) % height


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:  # pragma: no cover - surfaced through process exit
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
