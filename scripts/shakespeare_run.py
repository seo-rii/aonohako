#!/usr/bin/env python3
"""Judge-oriented runner for the Shakespeare Programming Language.

Plays are parsed and executed by the pinned `shakespearelang` interpreter
(https://github.com/zmbc/shakespearelang). This wrapper replaces its console
oriented I/O with byte-exact judge I/O: numeric input skips leading whitespace
and accepts an optional sign like the reference spl2c `scanf("%d")` path, and
interpreter errors terminate with a non-zero exit status instead of zero.
"""

from __future__ import annotations

import sys

WHITESPACE = frozenset(b" \t\r\n\v\f")
DIGITS = frozenset(b"0123456789")


class SplRunnerError(Exception):
    """A deterministic input error raised outside the interpreter."""


class ByteInputManager:
    def __init__(self, stream, flush) -> None:
        self._stream = stream
        self._flush = flush
        self._buffer = b""
        self._pos = 0
        self._eof = False

    def _peek(self) -> int:
        if self._pos >= len(self._buffer) and not self._eof:
            # Interactive judges must observe pending output before we block.
            self._flush()
            self._buffer = self._stream.read1(65536)
            self._pos = 0
            if not self._buffer:
                self._eof = True
        return self._buffer[self._pos] if self._pos < len(self._buffer) else -1

    def _advance(self) -> None:
        self._pos += 1

    def consume_numeric_input(self) -> int:
        while self._peek() in WHITESPACE:
            self._advance()
        if self._peek() == -1:
            raise SplRunnerError("End of file encountered.")
        sign = 1
        if self._peek() in (ord("+"), ord("-")):
            if self._peek() == ord("-"):
                sign = -1
            self._advance()
        digits = bytearray()
        while self._peek() in DIGITS:
            digits.append(self._peek())
            self._advance()
        if not digits:
            raise SplRunnerError("No numeric input was given.")
        if self._peek() == ord("\n"):
            self._advance()
        return sign * int(digits)

    def consume_character_input(self) -> int:
        value = self._peek()
        if value != -1:
            self._advance()
        return value


class ByteOutputManager:
    def __init__(self, stream) -> None:
        self._stream = stream

    def output_number(self, number: int) -> None:
        self._stream.write(str(number).encode("ascii"))

    def output_character(self, code: int) -> None:
        if 0 <= code <= 0xFF:
            self._stream.write(bytes((code,)))
            return
        try:
            self._stream.write(chr(code).encode("utf-8"))
        except (ValueError, OverflowError):
            raise SplRunnerError(f"Invalid character code: {code}") from None


def main(argv: list[str]) -> int:
    check_only = len(argv) == 3 and argv[1] == "--check"
    if len(argv) != 2 and not check_only:
        print("usage: shakespeare_run.py [--check] <play>", file=sys.stderr)
        return 1

    from shakespearelang import Shakespeare
    from shakespearelang.errors import ShakespeareError

    stdout = sys.stdout.buffer
    try:
        with open(argv[-1], "r", encoding="utf-8") as source:
            play = source.read()
        interpreter = Shakespeare(play)
        if check_only:
            return 0
        interpreter.settings.input_manager = ByteInputManager(sys.stdin.buffer, stdout.flush)
        interpreter.settings.output_manager = ByteOutputManager(stdout)
        interpreter.run()
    except (ShakespeareError, SplRunnerError, OSError, UnicodeDecodeError) as exc:
        stdout.flush()
        print(f"shakespeare: {exc}", file=sys.stderr)
        return 1
    finally:
        stdout.flush()
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
