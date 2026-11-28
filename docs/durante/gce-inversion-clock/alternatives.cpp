// Additional independent solutions for the seeded inversion-counting problem.
#include "../gce-qualified-judge/corpus/generator.hpp"
#include <algorithm>
#include <cstdint>
#include <iostream>
#include <vector>

#if SOLUTION == 1
std::uint64_t inversions(std::vector<int>& values, int) {
    const int n = static_cast<int>(values.size());
    std::vector<int> scratch(n);
    std::uint64_t count = 0;
    for (int width = 1; width < n; width *= 2) {
        for (int begin = 0; begin < n; begin += width * 2) {
            const int middle = std::min(begin + width, n);
            const int end = std::min(middle + width, n);
            int left = begin, right = middle;
            for (int out = begin; out < end; ++out) {
                if (left < middle && (right == end || values[left] <= values[right])) {
                    scratch[out] = values[left++];
                } else {
                    scratch[out] = values[right++];
                    count += middle - left;
                }
            }
        }
        values.swap(scratch);
    }
    return count;
}
#elif SOLUTION == 2
std::uint64_t inversions(std::vector<int>& values, int alphabet) {
    std::vector<int> frequency(alphabet + 1);
    std::uint64_t count = 0;
    for (auto it = values.rbegin(); it != values.rend(); ++it) {
        // Value v gets index v+1; query strictly smaller values in the suffix.
        for (int i = *it; i > 0; i &= i - 1) count += frequency[i];
        for (int i = *it + 1; i <= alphabet; i += i & -i) ++frequency[i];
    }
    return count;
}
#else
#error "select bottom-up merge (1) or right-to-left Fenwick tree (2)"
#endif

int main() {
    int batches, n, alphabet;
    std::uint32_t seed;
    if (!(std::cin >> batches >> n >> alphabet >> seed)) return 1;
    Generator random(seed);
    std::vector<int> values(n);
    std::uint64_t total = 0;
    for (int batch = 0; batch < batches; ++batch) {
        for (int& value : values) value = random.next() % alphabet;
        total += inversions(values, alphabet);
    }
    std::cout << total << '\n';
}
