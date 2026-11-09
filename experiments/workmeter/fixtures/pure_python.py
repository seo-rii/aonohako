import sys

scale = int(sys.stdin.readline())
assert scale in (1, 2, 4)
value = 1
for _ in range(750000 * scale):
    value = (value * 1664525 + 1013904223) & 0xFFFFFFFF
print(value)
