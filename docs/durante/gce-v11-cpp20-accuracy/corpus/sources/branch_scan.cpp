#include <cinttypes>
#include <cstdint>
#include <cstdio>
#include <vector>

static __attribute__((noinline)) uint64_t one(uint32_t i) {
    return uint64_t(i) * (i + 3) + 17;
}
static __attribute__((noinline)) uint64_t zero(uint32_t i) {
    return (uint64_t(i) * 37 + 11) ^ 0x5a5a;
}

int main() {
    uint32_t n, passes;
    if (std::scanf("%" SCNu32 " %" SCNu32, &n, &passes) != 2 || n < 2 ||
        n > (1U << 22) || (n & (n - 1)) || passes == 0 || passes > 64) return 2;
    std::vector<uint8_t> flag(n);
    for (uint32_t i = 0; i < n; ++i) {
        uint32_t j = ((i ^ (i >> 9)) * 5077U) & (n - 1);
        j = ((j ^ (j >> 7)) * 193489U) & (n - 1);
        flag[j ^ (j >> 5)] = i >= n / 2;
    }
    uint64_t sum = 0;
    for (uint32_t p = 0; p < passes; ++p)
        for (uint32_t i = 0; i < n; ++i)
            sum += flag[i] ? one(i ^ p) : zero(i ^ p);
    std::printf("%" PRIu64 "\n", sum);
}
