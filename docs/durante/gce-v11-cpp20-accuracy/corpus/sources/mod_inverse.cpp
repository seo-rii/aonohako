#include <cinttypes>
#include <cstdint>
#include <cstdio>

int main() {
    constexpr int64_t prime = 1000003;
    uint32_t n;
    if (std::scanf("%" SCNu32, &n) != 1 || n == 0 || n > 1000000) return 2;
    uint64_t checksum = 0;
    for (uint32_t i = 0; i < n; ++i) {
        int64_t r = prime, next_r = 1 + (uint64_t(i) * 8191 + 104729) % (prime - 1);
        int64_t c = 0, next_c = 1;
        while (next_r != 0) {
            const int64_t q = r / next_r;
            const int64_t remainder = r - q * next_r, coeff = c - q * next_c;
            r = next_r;
            next_r = remainder;
            c = next_c;
            next_c = coeff;
        }
        checksum += uint64_t(i + 1) * uint64_t(c < 0 ? c + prime : c);
    }
    std::printf("%" PRIu64 "\n", checksum);
}
