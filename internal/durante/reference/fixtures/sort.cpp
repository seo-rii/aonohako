#include <algorithm>
#include <cinttypes>
#include <cstdint>
#include <cstdio>
#include <vector>

int main() {
    std::uint64_t size;
    if (std::scanf("%" SCNu64, &size) != 1 || size < 2 ||
        size > (2ULL << 20) || (size & (size - 1)) != 0) {
        return 2;
    }
    std::vector<std::uint32_t> values(size);
    // This is a scrambled permutation, not sorted input. Its sorted order is
    // known independently, so a weighted checksum detects skipped sorting.
    for (std::uint64_t i = 0; i < size; ++i) {
        values[i] = (i * 1664525 + 1013904223) & (size - 1);
    }
    std::sort(values.begin(), values.end());
    std::uint64_t checksum = 0;
    for (std::uint64_t i = 0; i < size; ++i) {
        checksum += (i + 1) * (std::uint64_t(values[i]) + 1);
    }
    std::printf("%" PRIu64 "\n", checksum);
}
