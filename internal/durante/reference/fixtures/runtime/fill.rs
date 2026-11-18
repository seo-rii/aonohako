use std::hint::black_box;
use std::io::{self, Read};

fn main() {
    let mut input = String::new();
    io::stdin().read_to_string(&mut input).unwrap();
    let mut fields = input
        .split_whitespace()
        .map(|s| s.parse::<usize>().unwrap());
    let size = fields.next().unwrap();
    let repeats = fields.next().unwrap();
    let mut buffer = vec![0u8; size];
    let mut checksum = 0u64;
    for r in 0..repeats {
        let value = ((r * 73 + 19) & 255) as u8;
        black_box(&mut buffer).fill(value);
        checksum = checksum.wrapping_add((r as u64 + 1) * buffer[(r * 4099) & (size - 1)] as u64);
    }
    for (i, value) in buffer.iter().enumerate() {
        checksum = checksum.wrapping_add((i as u64 + 1) * *value as u64);
    }
    println!("{checksum}");
}
