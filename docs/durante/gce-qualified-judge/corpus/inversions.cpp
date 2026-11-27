// Sum the number of strictly inverted pairs in the generated ranking lists.
#include "generator.hpp"
#include <algorithm>
#include <cstdint>
#include <iostream>
#include <vector>

#if SOLUTION == 1
std::uint64_t merge_count(std::vector<int>& values, std::vector<int>& buffer, int l, int r) {
    if (r - l < 2) return 0;
    int m = l + (r-l)/2;
    std::uint64_t count = merge_count(values, buffer, l, m);
    count += merge_count(values, buffer, m, r);
    int a = l, b = m;
    for (int i = l; i < r; ++i) {
        if (b == r || (a < m && values[a] <= values[b])) {
            buffer[i] = values[a++];
        } else {
            count += m - a;
            buffer[i] = values[b++];
        }
    }
    std::copy(buffer.begin() + l, buffer.begin() + r, values.begin() + l);
    return count;
}
#endif

int main() {
    int batches, n, alphabet;
    std::uint32_t seed;
    if (!(std::cin >> batches >> n >> alphabet >> seed)) return 1;
    Generator random(seed);
    std::vector<int> values(n);
    std::uint64_t total = 0;
    for (int batch = 0; batch < batches; ++batch) {
        for (int& v : values) v = random.next() % alphabet;
#if SOLUTION == 1
        std::vector<int> buffer(n);
        total += merge_count(values, buffer, 0, n);
#elif SOLUTION == 2 || SOLUTION == 4
        std::vector<int> tree(alphabet + 1);
        for (int i = 0; i < n; ++i) {
            int key = values[i] + 1;
            int end = key;
#if SOLUTION == 4
            // A strict prefix here counts ties as inversions.
            --end;
#endif
            int prefix = 0;
            for (; end > 0; end -= end & -end) prefix += tree[end];
            total += i - prefix;
            for (int k = key; k <= alphabet; k += k & -k) ++tree[k];
        }
#elif SOLUTION == 3
        for (int i = 0; i < n; ++i)
            for (int j = i + 1; j < n; ++j) total += values[i] > values[j];
#else
#error unknown solution
#endif
    }
    std::cout << total << '\n';
}
