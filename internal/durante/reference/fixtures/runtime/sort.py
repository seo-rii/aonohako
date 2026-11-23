import sys

size = int(sys.stdin.buffer.read())
values = [(i * 1664525 + 1013904223) & (size - 1) for i in range(size)]
values.sort()
checksum = 0
for i, value in enumerate(values):
    checksum += (i + 1) * (value + 1)
print(checksum & ((1 << 64) - 1))
