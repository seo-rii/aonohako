import sys

iterations, seed = map(int, sys.stdin.buffer.read().split())
checksum = 0
for i in range(iterations):
    value = (i * 40503 + seed) & 65535
    if value < 21845:
        checksum += value * value + 11
    elif value < 43690:
        checksum += value * value * value + 17
    else:
        checksum += (value ^ 0x5A5A) + 23
print(checksum & ((1 << 64) - 1))
