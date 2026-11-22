#include <cinttypes>
#include <cstdint>
#include <cstdio>
#include <vector>

int main() {
    constexpr uint32_t size = 1U << 24;
    uint32_t active, steps;
    if (std::scanf("%" SCNu32 " %" SCNu32, &active, &steps) != 2 ||
        active < 2 || active > size || (active & (active - 1)) || steps == 0) return 2;
    std::vector<uint32_t> next(size);
    for (uint32_t i = 0; i < size; ++i)
        next[i] = (1664525U * i + 1013904223U) & (size - 1);

    uint32_t at = 713U & (active - 1);
    for (uint32_t i = 0; i < steps; ++i)
        at = next[at] & (active - 1);
    std::printf("%" PRIu32 "\n", at);
}
