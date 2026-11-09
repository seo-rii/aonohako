import sys

scale = int(sys.stdin.readline())
assert scale in (1, 2, 4)
text = "abcXYZ012|" * (65536 * scale)
checksum = 0
for _ in range(24):
    result = text.replace("abc", "Q")
    checksum += result.count("Q") + result.count("XYZ") + result.count("012")
print(len(result), checksum)
