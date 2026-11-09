import re
import sys

scale = int(sys.stdin.readline())
assert scale in (1, 2, 4)
text = "aa1234bb|" * (65536 * scale)
pattern = re.compile(r"[0-9]+")
for _ in range(12):
    result = pattern.sub("", text)
print(len(result), result.count("aabb|"))
