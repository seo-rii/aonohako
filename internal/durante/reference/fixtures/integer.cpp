#include <cinttypes>
#include <cstdint>
#include <cstdio>

int main() {
    std::uint64_t iterations, value;
    if (std::scanf("%" SCNu64 " %" SCNu64, &iterations, &value) != 2 ||
        iterations == 0 || iterations > (128ULL << 20)) {
        return 2;
    }
    // Every multiplication depends on the previous result. Unsigned overflow
    // deliberately defines arithmetic modulo 2^64.
    for (std::uint64_t i = 0; i < iterations; ++i) {
        value = value * 6364136223846793005ULL + 1442695040888963407ULL;
    }
    std::printf("%" PRIu64 "\n", value);
}
