use std::io::{self, Read};

fn main() {
    let mut input = String::new();
    io::stdin().read_to_string(&mut input).unwrap();
    let mut fields = input.split_whitespace().map(|s| s.parse::<u64>().unwrap());
    let iterations = fields.next().unwrap();
    let mut value = fields.next().unwrap();
    for _ in 0..iterations {
        value = value
            .wrapping_mul(6364136223846793005)
            .wrapping_add(1442695040888963407);
    }
    println!("{value}");
}
