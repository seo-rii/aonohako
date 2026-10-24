#!/usr/bin/env python3
"""A dependency-free Unlambda 2.0 interpreter.

This follows David Madore's Unlambda 2.0 specification and reference
interpreter: http://www.madore.org/~david/programs/unlambda/

Evaluation runs on an explicit CPS machine whose continuations are immutable
heap-allocated frames, so neither deep programs nor ``c`` rely on Python
recursion, and capturing a continuation is O(1).
"""

from __future__ import annotations

import sys


# Value and expression tags. Every value is also a (self-evaluating) expression.
APP = 0
K, K1, S, S1, S2, I, V, D, D1, C, CONT, E, DOT, READ, CMP, PIPE = range(1, 17)

# Continuation frame tags; the empty continuation is None.
F_ARG = 0  # evaluated the operator; next evaluate the operand (unless d)
F_APPLY_TO = 1  # evaluated the operand; apply the stored operator to it
F_APPLY_WITH = 2  # forced a promise; apply the result to the stored operand
F_S2 = 3  # evaluated `xz; next evaluate `yz (unless `xz is d) and apply

K_VALUE = (K,)
S_VALUE = (S,)
I_VALUE = (I,)
V_VALUE = (V,)
D_VALUE = (D,)
C_VALUE = (C,)
E_VALUE = (E,)
READ_VALUE = (READ,)
PIPE_VALUE = (PIPE,)
DOT_VALUES = tuple((DOT, byte) for byte in range(256))
CMP_VALUES = tuple((CMP, byte) for byte in range(256))

BUILTINS = {
    ord("k"): K_VALUE,
    ord("s"): S_VALUE,
    ord("i"): I_VALUE,
    ord("v"): V_VALUE,
    ord("d"): D_VALUE,
    ord("c"): C_VALUE,
    ord("e"): E_VALUE,
    ord("r"): DOT_VALUES[10],
    ord("@"): READ_VALUE,
    ord("|"): PIPE_VALUE,
}
for _letter in b"ksivdcer":
    BUILTINS[_letter - 32] = BUILTINS[_letter]
WHITESPACE = frozenset(b" \t\n\r\v\f")
OUTPUT_CHUNK = 8192


class UnlambdaError(Exception):
    """A deterministic source or command-line error."""


def parse(source: bytes):
    pending: list = []  # None awaits an operator, otherwise holds the operator
    result = None
    length = len(source)
    pos = 0
    while pos < length:
        byte = source[pos]
        pos += 1
        if byte in WHITESPACE:
            continue
        if byte == 35:  # '#'
            newline = source.find(b"\n", pos)
            pos = length if newline < 0 else newline + 1
            continue
        if result is not None:
            raise UnlambdaError(f"unexpected trailing input at byte {pos - 1}")
        if byte == 96:  # '`'
            pending.append(None)
            continue
        if byte == 46 or byte == 63:  # '.x' or '?x'
            if pos >= length:
                raise UnlambdaError(f"unexpected end of source after byte {pos - 1}")
            node = (DOT_VALUES if byte == 46 else CMP_VALUES)[source[pos]]
            pos += 1
        else:
            node = BUILTINS.get(byte)
            if node is None:
                raise UnlambdaError(f"unknown character {chr(byte)!r} at byte {pos - 1}")
        while pending:
            operator = pending[-1]
            if operator is None:
                pending[-1] = node
                break
            pending.pop()
            node = (APP, operator, node)
        else:
            result = node
    if result is None:
        raise UnlambdaError("unexpected end of source")
    return result


def run(program, input_stream, output_stream) -> None:
    out = bytearray()
    current = -1  # no current character yet
    expr = program
    k = None
    try:
        while True:
            # Evaluate expr: descend into operators, pushing operand frames.
            while expr[0] == APP:
                k = (F_ARG, expr[2], k)
                expr = expr[1]
            value = expr

            # Return value to k, or apply f to x; loops until a new expr.
            while True:
                if k is None:
                    return
                frame = k[0]
                if frame == F_ARG:
                    if value is D_VALUE:
                        value = (D1, k[1])
                        k = k[2]
                        continue
                    expr = k[1]
                    k = (F_APPLY_TO, value, k[2])
                    break
                if frame == F_APPLY_TO:
                    f = k[1]
                    x = value
                    k = k[2]
                elif frame == F_APPLY_WITH:
                    f = value
                    x = k[1]
                    k = k[2]
                else:  # F_S2: frame holds y and z
                    if value is D_VALUE:
                        value = (D1, (APP, k[1], k[2]))
                        k = k[3]
                        continue
                    f = k[1]
                    x = k[2]
                    k = (F_APPLY_TO, value, k[3])

                while True:  # apply f to x
                    tag = f[0]
                    if tag == I:
                        value = x
                    elif tag == K1:
                        value = f[1]
                    elif tag == S2:
                        k = (F_S2, f[2], x, k)
                        f = f[1]
                        continue
                    elif tag == S1:
                        value = (S2, f[1], x)
                    elif tag == K:
                        value = (K1, x)
                    elif tag == S:
                        value = (S1, x)
                    elif tag == DOT:
                        out.append(f[1])
                        if len(out) >= OUTPUT_CHUNK:
                            output_stream.write(out)
                            out.clear()
                        value = x
                    elif tag == V:
                        value = V_VALUE
                    elif tag == D1:
                        k = (F_APPLY_WITH, x, k)
                        expr = f[1]
                        value = None
                    elif tag == D:
                        value = (D1, x)
                    elif tag == C:
                        f = x
                        x = (CONT, k)
                        continue
                    elif tag == CONT:
                        k = f[1]
                        value = x
                    elif tag == E:
                        return
                    elif tag == READ:
                        if out:
                            output_stream.write(out)
                            out.clear()
                        output_stream.flush()
                        incoming = input_stream.read(1)
                        f = x
                        if incoming:
                            current = incoming[0]
                            x = I_VALUE
                        else:
                            current = -1
                            x = V_VALUE
                        continue
                    elif tag == CMP:
                        f, x = x, (I_VALUE if f[1] == current else V_VALUE)
                        continue
                    else:  # PIPE
                        f, x = x, (DOT_VALUES[current] if current >= 0 else V_VALUE)
                        continue
                    break
                if value is None:
                    break
    finally:
        if out:
            output_stream.write(out)
        output_stream.flush()


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print("usage: unlambda.py <source>", file=sys.stderr)
        return 1
    try:
        with open(argv[1], "rb") as source:
            program = parse(source.read())
        run(program, sys.stdin.buffer, sys.stdout.buffer)
    except (UnlambdaError, OSError) as exc:
        print(f"unlambda: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
