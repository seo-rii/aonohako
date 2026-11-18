import sys

iterations, value = map(int, sys.stdin.buffer.read().split())
mask = (1 << 64) - 1
for _ in range(iterations):
    value = (value * 6364136223846793005 + 1442695040888963407) & mask
print(value)
