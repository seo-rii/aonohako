// Nine correct submissions for the original seeded inversion-counting case.
#include "../gce-qualified-judge/corpus/generator.hpp"
#include <algorithm>
#include <cstdint>
#include <iostream>
#include <vector>

#if defined(BLOCK_SIZE)
std::uint64_t inversions(const std::vector<int>& values, int alphabet) {
    // Count greater values in the partial block and then in whole blocks.
    std::vector<int> frequency(alphabet);
    std::vector<int> totals((alphabet + BLOCK_SIZE - 1) / BLOCK_SIZE);
    std::uint64_t result = 0;
    for (int value : values) {
        int block = value / BLOCK_SIZE;
        int end = std::min((block + 1) * BLOCK_SIZE, alphabet);
        int greater = 0;
        for (int key = value + 1; key < end; ++key) greater += frequency[key];
        for (int b = block + 1; b < static_cast<int>(totals.size()); ++b) greater += totals[b];
        result += greater;
        ++frequency[value];
        ++totals[block];
    }
    return result;
}
#elif defined(INSERTION_CUTOFF)
std::uint64_t inversions(std::vector<int>& values, int) {
    // Insertion-sort each run, then combine the sorted runs using stable merges.
    const int n = static_cast<int>(values.size());
    std::uint64_t result = 0;
    for (int start = 0; start < n; start += INSERTION_CUTOFF) {
        const int end = std::min(start + INSERTION_CUTOFF, n);
        for (int i = start + 1; i < end; ++i) {
            const int item = values[i];
            int j = i;
            while (j > start && values[j - 1] > item) {
                values[j] = values[j - 1];
                --j;
            }
            values[j] = item;
            result += i - j;
        }
    }
    std::vector<int> scratch(n);
    for (int width = INSERTION_CUTOFF; width < n; width *= 2) {
        for (int start = 0; start < n; start += 2 * width) {
            const int middle = std::min(start + width, n);
            const int end = std::min(middle + width, n);
            int left = start, right = middle;
            for (int k = start; k < end; ++k) {
                if (right == end || (left < middle && values[left] <= values[right])) {
                    scratch[k] = values[left++];
                } else {
                    scratch[k] = values[right++];
                    result += middle - left;
                }
            }
        }
        values.swap(scratch);
    }
    return result;
}
#else
#error Specify BLOCK_SIZE or INSERTION_CUTOFF.
#endif

int main() {
    int batches, n, alphabet;
    std::uint32_t seed;
    if (!(std::cin >> batches >> n >> alphabet >> seed)) return 1;
    Generator random(seed);
    std::vector<int> values(n);
    std::uint64_t result = 0;
    for (int batch = 0; batch < batches; ++batch) {
        for (int& value : values) value = random.next() % alphabet;
        result += inversions(values, alphabet);
    }
    std::cout << result << '\n';
}
