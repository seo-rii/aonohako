use std::io::{self, Read};

fn main() {
    let mut input = String::new();
    io::stdin().read_to_string(&mut input).unwrap();
    let mut fields = input.split_whitespace().map(|s| s.parse::<u64>().unwrap());
    let iterations = fields.next().unwrap();
    let seed = fields.next().unwrap();
    let mut checksum = 0u64;
    for i in 0..iterations {
        let value = (i * 40503 + seed) & 65535;
        let term = if value < 21845 {
            value * value + 11
        } else if value < 43690 {
            value * value * value + 17
        } else {
            (value ^ 0x5a5a) + 23
        };
        checksum = checksum.wrapping_add(term);
    }
    println!("{checksum}");
}
