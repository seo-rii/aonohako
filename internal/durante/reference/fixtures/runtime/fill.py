import sys

size, repeats = map(int, sys.stdin.buffer.read().split())
buffer = bytearray(size)
checksum = 0
for r in range(repeats):
    value = (r * 73 + 19) & 255
    buffer[:] = bytes([value]) * size
    checksum += (r + 1) * buffer[(r * 4099) & (size - 1)]
for i, value in enumerate(buffer):
    checksum += (i + 1) * value
print(checksum & ((1 << 64) - 1))
