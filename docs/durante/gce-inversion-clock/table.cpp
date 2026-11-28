#include <cstdint>
#include <cstdio>
#include <vector>

int main() {
    unsigned mib, repetitions;
    if (std::scanf("%u%u", &mib, &repetitions) != 2 || mib == 0 || mib > 896 ||
        repetitions == 0 || repetitions > 8) return 1;
    std::vector<std::uint8_t> table(static_cast<std::size_t>(mib) << 20, 1);
    std::uint64_t sum = 0;
    for (unsigned pass = 0; pass < repetitions; ++pass) {
        for (std::size_t i = 0; i < table.size(); i += 4096) {
            sum += table[i];
            table[i] += 1;
        }
    }
    std::printf("%llu\n", static_cast<unsigned long long>(sum));
}
