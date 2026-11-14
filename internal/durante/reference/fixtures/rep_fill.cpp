#include <cinttypes>
#include <cstddef>
#include <cstdint>
#include <cstdio>
#include <vector>

#if !defined(__x86_64__)
#error This fixed reference workload requires x86-64.
#endif

int main() {
    std::uint64_t size, repeats;
    if (std::scanf("%" SCNu64 " %" SCNu64, &size, &repeats) != 2 ||
        size < 256 || size > (4ULL << 20) || (size & (size - 1)) != 0 ||
        repeats == 0 || repeats > 2048) {
        return 2;
    }
    std::vector<std::uint8_t> buffer(size);
    std::uint64_t checksum = 0;
    for (std::uint64_t r = 0; r < repeats; ++r) {
        const std::uint8_t value = (r * 73 + 19) & 255;
        auto* out = buffer.data();
        std::size_t remaining = size;
        // Distinct from the training copy: every iteration explicitly fills
        // the complete buffer with REP STOSB and consumes its result.
        asm volatile("rep stosb"
                     : "+D"(out), "+c"(remaining)
                     : "a"(value)
                     : "memory");
        checksum += (r + 1) * buffer[(r * 4099) & (size - 1)];
    }
    for (std::uint64_t i = 0; i < size; ++i) {
        checksum += (i + 1) * buffer[i];
    }
    std::printf("%" PRIu64 "\n", checksum);
}
