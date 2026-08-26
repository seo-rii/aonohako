//! Compile-time probe for the crates exposed to installed-mode submissions.

use itertools::Itertools;

pub fn catalog_probe() -> Vec<Vec<i32>> {
    (1..=3).permutations(2).collect()
}

#[cfg(test)]
mod tests {
    use super::catalog_probe;

    #[test]
    fn pinned_crate_is_usable() {
        assert_eq!(catalog_probe().len(), 6);
    }
}
