// Seeded problem inputs use xorshift32 with unsigned wraparound.
#pragma once

#include <cstdint>

class Generator {
public:
    explicit Generator(std::uint32_t seed) : state_(seed) {}

    std::uint32_t next() {
        state_ ^= state_ << 13;
        state_ ^= state_ >> 17;
        state_ ^= state_ << 5;
        return state_;
    }

private:
    std::uint32_t state_;
};
