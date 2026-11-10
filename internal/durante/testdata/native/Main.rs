use std::io::{self, Read};
fn main() { let mut input = String::new(); io::stdin().read_to_string(&mut input).unwrap(); let n: usize = input.trim().parse().unwrap(); let mut x = 1u32; for _ in 0..n { x = x.wrapping_mul(1664525).wrapping_add(1013904223); } println!("{}", x); }
