// Sum the minimum in each generated inclusive interval.
#include "generator.hpp"
#include <algorithm>
#include <bit>
#include <cstdint>
#include <iostream>
#include <limits>
#include <vector>

int main() {
    int n, queries, min_width, max_width;
    std::uint32_t seed;
    if (!(std::cin >> n >> queries >> min_width >> max_width >> seed)) return 1;
    Generator random(seed);
    std::vector<int> values(n);
    for (int& value : values) value = 1 + random.next() % 1000003;
#if SOLUTION == 1
    const int base = std::bit_ceil(static_cast<unsigned>(n));
    std::vector<int> tree(2 * base, std::numeric_limits<int>::max());
    std::copy(values.begin(), values.end(), tree.begin() + base);
    for (int i = base - 1; i > 0; --i) tree[i] = std::min(tree[2*i], tree[2*i+1]);
#elif SOLUTION == 2 || SOLUTION == 4
    std::vector<std::vector<int>> table{values};
    for (int size = 2; size <= n; size *= 2) {
        const auto& prev = table.back();
        std::vector<int> row(n - size + 1);
        for (int i = 0; i + size <= n; ++i) row[i] = std::min(prev[i], prev[i + size/2]);
        table.push_back(std::move(row));
    }
#endif
    std::uint64_t total = 0;
    for (int i = 0; i < queries; ++i) {
        int width = min_width + random.next() % (max_width - min_width + 1);
        int left = random.next() % (n - width + 1);
        int right = left + width;
#if SOLUTION == 1
        int smallest = std::numeric_limits<int>::max();
        for (left += base, right += base; left < right; left /= 2, right /= 2) {
            if (left % 2) smallest = std::min(smallest, tree[left++]);
            if (right % 2) smallest = std::min(smallest, tree[--right]);
        }
        total += smallest;
#elif SOLUTION == 2 || SOLUTION == 4
#if SOLUTION == 4
        // Boundary bug: omit the last element from every non-singleton interval.
        if (width > 1) { --right; --width; }
#endif
        int level = std::bit_width(static_cast<unsigned>(width)) - 1;
        total += std::min(table[level][left], table[level][right - (1 << level)]);
#elif SOLUTION == 3
        total += *std::min_element(values.begin() + left, values.begin() + right);
#else
#error unknown solution
#endif
    }
    std::cout << total << '\n';
}
