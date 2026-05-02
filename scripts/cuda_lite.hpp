#pragma once

#include <cstdlib>
#include <cstring>

#define __global__
#define __device__
#define __host__

struct dim3 {
    unsigned int x;
    unsigned int y;
    unsigned int z;

    dim3(unsigned int x_ = 1, unsigned int y_ = 1, unsigned int z_ = 1)
        : x(x_), y(y_), z(z_) {}
};

inline dim3 threadIdx;
inline dim3 blockIdx;
inline dim3 blockDim;
inline dim3 gridDim;

enum cudaMemcpyKind {
    cudaMemcpyHostToHost = 0,
    cudaMemcpyHostToDevice = 1,
    cudaMemcpyDeviceToHost = 2,
    cudaMemcpyDeviceToDevice = 3,
};

using cudaError_t = int;
constexpr cudaError_t cudaSuccess = 0;

inline cudaError_t cudaMalloc(void **ptr, size_t size) {
    *ptr = std::malloc(size);
    return *ptr == nullptr ? 1 : cudaSuccess;
}

inline cudaError_t cudaFree(void *ptr) {
    std::free(ptr);
    return cudaSuccess;
}

inline cudaError_t cudaMemcpy(void *dst, const void *src, size_t count, cudaMemcpyKind) {
    std::memcpy(dst, src, count);
    return cudaSuccess;
}

inline cudaError_t cudaDeviceSynchronize() {
    return cudaSuccess;
}

namespace aonohako_cuda_lite {
template <class Kernel>
inline void launch(dim3 grid, dim3 block, Kernel kernel) {
    gridDim = grid;
    blockDim = block;
    for (unsigned int bz = 0; bz < grid.z; ++bz) {
        for (unsigned int by = 0; by < grid.y; ++by) {
            for (unsigned int bx = 0; bx < grid.x; ++bx) {
                blockIdx = dim3(bx, by, bz);
                for (unsigned int tz = 0; tz < block.z; ++tz) {
                    for (unsigned int ty = 0; ty < block.y; ++ty) {
                        for (unsigned int tx = 0; tx < block.x; ++tx) {
                            threadIdx = dim3(tx, ty, tz);
                            kernel();
                        }
                    }
                }
            }
        }
    }
}
}

#define CUDA_LAUNCH(kernel, grid, block, ...) \
    aonohako_cuda_lite::launch((grid), (block), [&]() { kernel(__VA_ARGS__); })
