import sys
import zlib

scale = int(sys.stdin.readline())
assert scale in (1, 2, 4)
data = b"a" * (1000000 * scale)
checksum = 0
for _ in range(24):
    result = zlib.decompress(zlib.compress(data))
    assert result == data
    checksum += len(result)
print(checksum)
