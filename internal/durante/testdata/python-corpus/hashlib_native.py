import hashlib
import sys

scale = int(sys.stdin.readline())
assert scale in (1, 2, 4)
data = b"a" * (1000000 * scale)
for _ in range(128):
    digest = hashlib.sha256(data).hexdigest()
print(digest)
