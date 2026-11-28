// Static range minimum using whole-block minima and either raw or precomputed edges.
#include "../gce-qualified-judge/corpus/generator.hpp"
#include <algorithm>
#include <cstdint>
#include <iostream>
#include <limits>
#include <vector>

int main() {
    int n, queries, min_width, max_width;
    std::uint32_t seed;
    if (!(std::cin >> n >> queries >> min_width >> max_width >> seed)) return 1;
    Generator random(seed);
    constexpr int block = BLOCK_SIZE;
    constexpr int infinity = std::numeric_limits<int>::max();
    std::vector<int> values(n), blocks((n + block - 1) / block, infinity);
    for (int i = 0; i < n; ++i) {
        values[i] = 1 + random.next() % 1000003;
        blocks[i / block] = std::min(blocks[i / block], values[i]);
    }
#if CACHED_EDGES
    std::vector<int> prefix(values), suffix(values);
    for (int i = 1; i < n; ++i)
        if (i % block != 0) prefix[i] = std::min(prefix[i - 1], values[i]);
    for (int i = n - 2; i >= 0; --i)
        if ((i + 1) % block != 0) suffix[i] = std::min(suffix[i + 1], values[i]);
#endif
    std::uint64_t result = 0;
    for (int q = 0; q < queries; ++q) {
        const int width = min_width + random.next() % (max_width - min_width + 1);
        const int left = random.next() % (n - width + 1);
        const int right = left + width - 1;
        const int first = left / block, last = right / block;
        int answer = infinity;
        if (first == last) {
            answer = *std::min_element(values.begin() + left, values.begin() + right + 1);
        } else {
#if CACHED_EDGES
            answer = std::min(suffix[left], prefix[right]);
#else
            for (int i = left; i < (first + 1) * block; ++i) answer = std::min(answer, values[i]);
            for (int i = last * block; i <= right; ++i) answer = std::min(answer, values[i]);
#endif
            for (int b = first + 1; b < last; ++b) answer = std::min(answer, blocks[b]);
        }
        result += answer;
    }
    std::cout << result << '\n';
}
