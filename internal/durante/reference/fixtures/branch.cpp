#include <cinttypes>
#include <cstdint>
#include <cstdio>

// Distinct non-inlined paths retain conditional control flow under the API's
// C++20 -O2 compilation. All returned values contribute to the checksum.
static __attribute__((noinline)) std::uint64_t square_path(std::uint64_t x) {
    return x * x + 11;
}

static __attribute__((noinline)) std::uint64_t cube_path(std::uint64_t x) {
    return x * x * x + 17;
}

static __attribute__((noinline)) std::uint64_t xor_path(std::uint64_t x) {
    return (x ^ 0x5a5a) + 23;
}

int main() {
    std::uint64_t iterations, seed;
    if (std::scanf("%" SCNu64 " %" SCNu64, &iterations, &seed) != 2 ||
        iterations == 0 || iterations > (64ULL << 20) || seed >= (1ULL << 16)) {
        return 2;
    }
    std::uint64_t checksum = 0;
    for (std::uint64_t i = 0; i < iterations; ++i) {
        // An odd multiplier permutes all 16-bit inputs in every full cycle.
        const std::uint64_t x = (i * 40503 + seed) & 65535;
        if (x < 21845) {
            checksum += square_path(x);
        } else if (x < 43690) {
            checksum += cube_path(x);
        } else {
            checksum += xor_path(x);
        }
    }
    std::printf("%" PRIu64 "\n", checksum);
}
