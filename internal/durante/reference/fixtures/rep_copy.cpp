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
    std::vector<std::uint8_t> source(size), destination(size);
    for (std::uint64_t i = 0; i < size; ++i) {
        source[i] = (i * 131 + 17) & 255;
    }
    std::uint64_t checksum = 0;
    for (std::uint64_t r = 0; r < repeats; ++r) {
        source[0] = (r & 255) ^ 17;
        source[size - 1] = ((r >> 8) & 255) ^ 91;
        auto* out = destination.data();
        const auto* in = source.data();
        std::size_t remaining = size;
        // The x86-64 ABI supplies a clear direction flag. Updated pointer and
        // count operands plus the memory clobber describe the entire copy.
        asm volatile("rep movsb"
                     : "+D"(out), "+S"(in), "+c"(remaining)
                     :
                     : "memory");
        checksum += (r + 1) * destination[(r * 4099) & (size - 1)];
    }
    for (std::uint64_t i = 0; i < size; ++i) {
        checksum += (i + 1) * destination[i];
    }
    std::printf("%" PRIu64 "\n", checksum);
}
