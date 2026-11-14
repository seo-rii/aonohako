#include <algorithm>
#include <cstdint>
#include <iostream>
#include <vector>

// Deterministic C++ execution fixture. Timing-model weights in its tests are
// synthetic, so this does not supply a calibrated reference machine.
int main() {
    std::uint32_t size;
    if (!(std::cin >> size) || size > 65536) return 2;
    if (size == 0) {
        volatile std::uint64_t value = 0;
        for (;;) value = value + 1;
    }
    std::vector<std::uint32_t> values(size);
    std::uint32_t value = 42;
    for (auto &item : values) {
        value = value * 1664525U + 1013904223U;
        item = value;
    }
    std::sort(values.begin(), values.end());
    std::uint64_t checksum = 0;
    for (std::size_t i = 0; i < values.size(); ++i)
        checksum += std::uint64_t(values[i]) * (i + 1);
    std::cout << checksum << '\n';
}
