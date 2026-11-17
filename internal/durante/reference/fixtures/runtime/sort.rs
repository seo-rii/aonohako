use std::io::{self, Read};

fn main() {
    let mut input = String::new();
    io::stdin().read_to_string(&mut input).unwrap();
    let size: usize = input.trim().parse().unwrap();
    let mut values: Vec<u32> = (0..size)
        .map(|i| ((i * 1664525 + 1013904223) & (size - 1)) as u32)
        .collect();
    values.sort_unstable();
    let mut checksum = 0u64;
    for (i, value) in values.iter().enumerate() {
        checksum = checksum.wrapping_add((i as u64 + 1) * (*value as u64 + 1));
    }
    println!("{checksum}");
}
