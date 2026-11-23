import sys
n = int(sys.stdin.readline())
x = 1
for _ in range(n):
    x = (x * 1664525 + 1013904223) & 0xffffffff
print(x)
