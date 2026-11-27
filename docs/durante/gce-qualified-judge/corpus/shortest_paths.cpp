// Sum shortest-path distances from vertex zero in a directed, positively weighted graph.
#include "generator.hpp"
#include <algorithm>
#include <cstdint>
#include <functional>
#include <iostream>
#include <limits>
#include <numeric>
#include <queue>
#include <utility>
#include <vector>

struct Edge { int to, cost; };

int main() {
    int n, extra_edges, max_cost;
    std::uint32_t seed;
    if (!(std::cin >> n >> extra_edges >> max_cost >> seed)) return 1;
    Generator random(seed);
    std::vector<std::vector<Edge>> graph(n);
    for (int i = 0; i < n; ++i)
        graph[i].push_back({(i + 1) % n, 1 + static_cast<int>(random.next() % max_cost)});
    for (int i = 0; i < extra_edges; ++i) {
        int from = random.next() % n;
        int to = (from + 1 + random.next() % (n - 1)) % n;
        int cost = 1 + random.next() % max_cost;
        graph[from].push_back({to, cost});
    }
    const int inf = std::numeric_limits<int>::max();
    std::vector<int> distance(n, inf);
    distance[0] = 0;
#if SOLUTION == 1
    using Item = std::pair<int,int>;
    std::priority_queue<Item, std::vector<Item>, std::greater<Item>> todo;
    todo.emplace(0, 0);
    while (!todo.empty()) {
        auto [d, u] = todo.top();
        todo.pop();
        if (d != distance[u]) continue;
        for (auto [v, c] : graph[u])
            if (d + c < distance[v]) {
                distance[v] = d + c;
                todo.emplace(distance[v], v);
            }
    }
#elif SOLUTION == 2
    std::vector<std::vector<int>> buckets(max_cost + 1);
    buckets[0].push_back(0);
    int pending = 1;
    for (int d = 0; pending; ++d) {
        auto& bucket = buckets[d % buckets.size()];
        while (!bucket.empty()) {
            int u = bucket.back();
            bucket.pop_back();
            --pending;
            if (distance[u] != d) continue;
            for (auto [v, c] : graph[u])
                if (d + c < distance[v]) {
                    distance[v] = d + c;
                    buckets[distance[v] % buckets.size()].push_back(v);
                    ++pending;
                }
        }
    }
#elif SOLUTION == 3
    std::vector<bool> done(n);
    for (int step = 0; step < n; ++step) {
        int u = -1;
        for (int i = 0; i < n; ++i)
            if (!done[i] && (u < 0 || distance[i] < distance[u])) u = i;
        done[u] = true;
        for (auto [v, c] : graph[u]) distance[v] = std::min(distance[v], distance[u] + c);
    }
#elif SOLUTION == 4
    // Incorrect for weighted edges: breadth-first search minimizes the number of edges.
    std::queue<int> todo;
    todo.push(0);
    while (!todo.empty()) {
        int u = todo.front();
        todo.pop();
        for (const Edge& edge : graph[u])
            if (distance[edge.to] == inf) {
                distance[edge.to] = distance[u] + 1;
                todo.push(edge.to);
            }
    }
#else
#error unknown solution
#endif
    std::cout << std::accumulate(distance.begin(), distance.end(), std::uint64_t(0)) << '\n';
}
