import sys

size, repeats = map(int, sys.stdin.buffer.read().split())
source = bytearray((i * 131 + 17) & 255 for i in range(size))
destination = bytearray(size)
checksum = 0
for r in range(repeats):
    source[0] = (r & 255) ^ 17
    source[-1] = ((r >> 8) & 255) ^ 91
    destination[:] = source
    checksum += (r + 1) * destination[(r * 4099) & (size - 1)]
for i, value in enumerate(destination):
    checksum += (i + 1) * value
print(checksum & ((1 << 64) - 1))
