// Sum single-source distances in the same seeded positive-weight graph as the judge pilot.
#include "../gce-qualified-judge/corpus/generator.hpp"
#include <algorithm>
#include <cstdint>
#include <deque>
#include <iostream>
#include <limits>
#include <numeric>
#include <set>
#include <utility>
#include <vector>

using Graph = std::vector<std::vector<std::pair<int, int>>>;
constexpr int inf = std::numeric_limits<int>::max();

#if defined(BLOCK_SIZE)
// Maintain one minimum per block of unsettled distances. A decrease updates
// that block in O(1). Removing the global minimum also rebuilds its block.
void solve(const Graph& graph, std::vector<int>& distance) {
    const int n = static_cast<int>(graph.size());
    std::vector<int> unsettled(n, inf);
    std::vector<int> minimum((n + BLOCK_SIZE - 1) / BLOCK_SIZE, inf);
    unsettled[0] = minimum[0] = 0;
    for (;;) {
        auto it = std::min_element(minimum.begin(), minimum.end());
        const int d = *it;
        if (d == inf) break;
        const int block = static_cast<int>(it - minimum.begin());
        auto first = unsettled.begin() + block * BLOCK_SIZE;
        auto end = unsettled.begin() + std::min(n, (block + 1) * BLOCK_SIZE);
        auto selected = std::find(first, end, d);
        const int u = static_cast<int>(selected - unsettled.begin());
        *selected = inf;
        *it = *std::min_element(first, end);
        for (auto [v, cost] : graph[u]) {
            int alt = d + cost;
            if (alt < distance[v]) {
                distance[v] = unsettled[v] = alt;
                auto& best = minimum[v / BLOCK_SIZE];
                best = std::min(best, alt);
            }
        }
    }
}
#elif defined(ORDERED_SET)
void solve(const Graph& graph, std::vector<int>& distance) {
    std::set<std::pair<int, int>> pending{{0, 0}};
    while (!pending.empty()) {
        auto [d, u] = *pending.begin();
        pending.erase(pending.begin());
        for (auto [v, cost] : graph[u]) {
            if (d + cost < distance[v]) {
                pending.erase({distance[v], v});
                distance[v] = d + cost;
                pending.emplace(distance[v], v);
            }
        }
    }
}
#elif defined(QUEUE_RELAX)
void solve(const Graph& graph, std::vector<int>& distance) {
    std::deque<int> pending{0};
    std::vector<bool> queued(graph.size());
    queued[0] = true;
    while (!pending.empty()) {
        int u = pending.front();
        pending.pop_front();
        queued[u] = false;
        for (auto [v, cost] : graph[u]) {
            if (distance[u] + cost < distance[v]) {
                distance[v] = distance[u] + cost;
                if (!queued[v]) {
                    if (QUEUE_RELAX && !pending.empty() && distance[v] < distance[pending.front()])
                        pending.push_front(v);
                    else
                        pending.push_back(v);
                    queued[v] = true;
                }
            }
        }
    }
}
#else
#error select a priority queue
#endif

int main() {
    int n, extra, max_cost;
    std::uint32_t seed;
    if (!(std::cin >> n >> extra >> max_cost >> seed) || n < 2 || extra < 0 || max_cost < 1) return 1;
    Generator rng(seed);
    Graph graph(n);
    for (int u = 0; u < n; ++u)
        graph[u].emplace_back((u + 1) % n, 1 + rng.next() % max_cost);
    for (int i = 0; i < extra; ++i) {
        int from = rng.next() % n;
        int to = (from + 1 + rng.next() % (n - 1)) % n;
        int cost = 1 + rng.next() % max_cost;
        graph[from].emplace_back(to, cost);
    }
    std::vector<int> distance(n, inf);
    distance[0] = 0;
    solve(graph, distance);
    std::cout << std::accumulate(distance.begin(), distance.end(), std::uint64_t{0}) << '\n';
}
