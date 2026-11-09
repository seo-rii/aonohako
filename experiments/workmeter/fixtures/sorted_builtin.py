import sys

scale = int(sys.stdin.readline())
assert scale in (1, 2, 4)
size = 65536 * scale
values = [(index * 48271) % size for index in range(size)]
for _ in range(8):
    result = sorted(values)
print(len(result), result[0], result[-1], sum(result))
