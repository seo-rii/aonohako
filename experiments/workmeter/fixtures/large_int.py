import sys

scale = int(sys.stdin.readline())
assert scale in (1, 2, 4)
width = 4096 * scale
modulus = (1 << width) - 159
factor = (1 << (width - 1)) + 217
value = 1
for _ in range(4000):
    value = value * factor % modulus
print(value.bit_length(), value % 1000000007)
