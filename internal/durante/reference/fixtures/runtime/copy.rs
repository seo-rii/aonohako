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
    let mut source: Vec<u8> = (0..size).map(|i| ((i * 131 + 17) & 255) as u8).collect();
    let mut destination = vec![0u8; size];
    let mut checksum = 0u64;
    for r in 0..repeats {
        source[0] = ((r & 255) ^ 17) as u8;
        source[size - 1] = (((r >> 8) & 255) ^ 91) as u8;
        black_box(&mut destination).copy_from_slice(black_box(&source));
        checksum =
            checksum.wrapping_add((r as u64 + 1) * destination[(r * 4099) & (size - 1)] as u64);
    }
    for (i, value) in destination.iter().enumerate() {
        checksum = checksum.wrapping_add((i as u64 + 1) * *value as u64);
    }
    println!("{checksum}");
}
