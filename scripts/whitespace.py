#!/usr/bin/env python3
import sys


SPACE = " "
TAB = "\t"
LF = "\n"


class ParseError(RuntimeError):
    pass


class InputBuffer:
    def __init__(self, data: bytes):
        self.data = data
        self.pos = 0

    def read_char(self) -> int:
        if self.pos >= len(self.data):
            return -1
        value = self.data[self.pos]
        self.pos += 1
        return value

    def read_number(self) -> int:
        while self.pos < len(self.data) and chr(self.data[self.pos]).isspace():
            self.pos += 1
        if self.pos >= len(self.data):
            return -1
        start = self.pos
        if self.data[self.pos] in (ord("+"), ord("-")):
            self.pos += 1
        while self.pos < len(self.data) and chr(self.data[self.pos]).isdigit():
            self.pos += 1
        token = self.data[start:self.pos].decode("ascii")
        if not token or token in {"+", "-"}:
            raise RuntimeError("invalid numeric input")
        return int(token, 10)


def read_number(code: str, pos: int) -> tuple[int, int]:
    if pos >= len(code):
        raise ParseError("unexpected end of number")
    sign = 1
    if code[pos] == TAB:
        sign = -1
    elif code[pos] != SPACE:
        raise ParseError("invalid number sign")
    pos += 1
    bits = []
    while pos < len(code) and code[pos] != LF:
        if code[pos] == SPACE:
            bits.append("0")
        elif code[pos] == TAB:
            bits.append("1")
        else:
            raise ParseError("invalid number body")
        pos += 1
    if pos >= len(code):
        raise ParseError("unterminated number")
    value = int("".join(bits or ["0"]), 2)
    return sign * value, pos + 1


def read_label(code: str, pos: int) -> tuple[str, int]:
    bits = []
    while pos < len(code) and code[pos] != LF:
        if code[pos] == SPACE:
            bits.append("0")
        elif code[pos] == TAB:
            bits.append("1")
        else:
            raise ParseError("invalid label")
        pos += 1
    if pos >= len(code):
        raise ParseError("unterminated label")
    return "".join(bits), pos + 1


def parse(code: str) -> tuple[list[tuple[str, object | None]], dict[str, int]]:
    pos = 0
    instructions: list[tuple[str, object | None]] = []
    labels: dict[str, int] = {}
    while pos < len(code):
        imp = code[pos]
        pos += 1
        if imp == SPACE:
            if pos >= len(code):
                raise ParseError("unexpected end of stack instruction")
            group = code[pos]
            pos += 1
            if group == SPACE:
                value, pos = read_number(code, pos)
                instructions.append(("push", value))
            elif group == LF:
                if pos >= len(code):
                    raise ParseError("unexpected end of stack control")
                op = code[pos]
                pos += 1
                mapping = {SPACE: "dup", TAB: "swap", LF: "discard"}
                if op not in mapping:
                    raise ParseError("invalid stack control")
                instructions.append((mapping[op], None))
            elif group == TAB:
                if pos >= len(code):
                    raise ParseError("unexpected end of stack access")
                op = code[pos]
                pos += 1
                if op == SPACE:
                    value, pos = read_number(code, pos)
                    instructions.append(("copy", value))
                elif op == LF:
                    value, pos = read_number(code, pos)
                    instructions.append(("slide", value))
                else:
                    raise ParseError("invalid stack access")
            else:
                raise ParseError("invalid stack instruction")
        elif imp == TAB:
            if pos >= len(code):
                raise ParseError("unexpected end of tab instruction")
            group = code[pos]
            pos += 1
            if group == SPACE:
                if pos + 1 >= len(code):
                    raise ParseError("unexpected end of arithmetic instruction")
                left = code[pos]
                right = code[pos + 1]
                pos += 2
                mapping = {
                    (SPACE, SPACE): "add",
                    (SPACE, TAB): "sub",
                    (SPACE, LF): "mul",
                    (TAB, SPACE): "div",
                    (TAB, TAB): "mod",
                }
                if (left, right) not in mapping:
                    raise ParseError("invalid arithmetic instruction")
                instructions.append((mapping[(left, right)], None))
            elif group == TAB:
                if pos >= len(code):
                    raise ParseError("unexpected end of heap instruction")
                op = code[pos]
                pos += 1
                mapping = {SPACE: "store", TAB: "retrieve"}
                if op not in mapping:
                    raise ParseError("invalid heap instruction")
                instructions.append((mapping[op], None))
            elif group == LF:
                if pos + 1 >= len(code):
                    raise ParseError("unexpected end of io instruction")
                left = code[pos]
                right = code[pos + 1]
                pos += 2
                mapping = {
                    (SPACE, SPACE): "out_char",
                    (SPACE, TAB): "out_num",
                    (TAB, SPACE): "read_char",
                    (TAB, TAB): "read_num",
                }
                if (left, right) not in mapping:
                    raise ParseError("invalid io instruction")
                instructions.append((mapping[(left, right)], None))
            else:
                raise ParseError("invalid tab instruction")
        elif imp == LF:
            if pos >= len(code):
                raise ParseError("unexpected end of flow instruction")
            group = code[pos]
            pos += 1
            if group == SPACE:
                if pos >= len(code):
                    raise ParseError("unexpected end of label instruction")
                op = code[pos]
                pos += 1
                label, pos = read_label(code, pos)
                mapping = {SPACE: "label", TAB: "call", LF: "jump"}
                if op not in mapping:
                    raise ParseError("invalid label instruction")
                if op == SPACE:
                    labels[label] = len(instructions)
                instructions.append((mapping[op], label))
            elif group == TAB:
                if pos >= len(code):
                    raise ParseError("unexpected end of branch instruction")
                op = code[pos]
                pos += 1
                if op == LF:
                    instructions.append(("return", None))
                else:
                    label, pos = read_label(code, pos)
                    mapping = {SPACE: "jump_zero", TAB: "jump_neg"}
                    if op not in mapping:
                        raise ParseError("invalid branch instruction")
                    instructions.append((mapping[op], label))
            elif group == LF:
                if pos >= len(code) or code[pos] != LF:
                    raise ParseError("invalid end instruction")
                pos += 1
                instructions.append(("exit", None))
            else:
                raise ParseError("invalid flow instruction")
        else:
            raise ParseError("invalid instruction prefix")
    return instructions, labels


def pop(stack: list[int]) -> int:
    if not stack:
        raise RuntimeError("stack underflow")
    return stack.pop()


def run(program_path: str) -> int:
    with open(program_path, "r", encoding="utf-8", newline="") as fh:
        source = fh.read().replace("\r\n", "\n").replace("\r", "\n")
    if any(ch not in (SPACE, TAB, LF) for ch in source):
        raise ParseError("source contains non-whitespace characters")
    instructions, labels = parse(source)
    stack: list[int] = []
    heap: dict[int, int] = {}
    calls: list[int] = []
    input_buffer = InputBuffer(sys.stdin.buffer.read())
    output: list[str] = []
    ip = 0
    while ip < len(instructions):
        op, arg = instructions[ip]
        if op == "push":
            stack.append(int(arg))
        elif op == "dup":
            stack.append(stack[-1])
        elif op == "copy":
            index = int(arg)
            stack.append(stack[-(index + 1)])
        elif op == "swap":
            stack[-1], stack[-2] = stack[-2], stack[-1]
        elif op == "discard":
            pop(stack)
        elif op == "slide":
            count = int(arg)
            top = pop(stack)
            if count < 0 or count > len(stack):
                raise RuntimeError("invalid slide count")
            del stack[-count:]
            stack.append(top)
        elif op == "add":
            b = pop(stack)
            a = pop(stack)
            stack.append(a + b)
        elif op == "sub":
            b = pop(stack)
            a = pop(stack)
            stack.append(a - b)
        elif op == "mul":
            b = pop(stack)
            a = pop(stack)
            stack.append(a * b)
        elif op == "div":
            b = pop(stack)
            a = pop(stack)
            stack.append(int(a / b))
        elif op == "mod":
            b = pop(stack)
            a = pop(stack)
            stack.append(a % b)
        elif op == "store":
            value = pop(stack)
            addr = pop(stack)
            heap[addr] = value
        elif op == "retrieve":
            addr = pop(stack)
            stack.append(heap.get(addr, 0))
        elif op == "label":
            pass
        elif op == "call":
            calls.append(ip)
            ip = labels[str(arg)]
            continue
        elif op == "jump":
            ip = labels[str(arg)]
            continue
        elif op == "jump_zero":
            if pop(stack) == 0:
                ip = labels[str(arg)]
                continue
        elif op == "jump_neg":
            if pop(stack) < 0:
                ip = labels[str(arg)]
                continue
        elif op == "return":
            if not calls:
                raise RuntimeError("empty call stack")
            ip = calls.pop() + 1
            continue
        elif op == "exit":
            break
        elif op == "out_char":
            output.append(chr(pop(stack) & 0xFF))
        elif op == "out_num":
            output.append(str(pop(stack)))
        elif op == "read_char":
            heap[pop(stack)] = input_buffer.read_char()
        elif op == "read_num":
            heap[pop(stack)] = input_buffer.read_number()
        ip += 1
    sys.stdout.write("".join(output))
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("usage: whitespace.py <program>", file=sys.stderr)
        raise SystemExit(2)
    try:
        raise SystemExit(run(sys.argv[1]))
    except Exception as exc:  # pragma: no cover - surfaced through process exit
        print(str(exc), file=sys.stderr)
        raise SystemExit(1)
