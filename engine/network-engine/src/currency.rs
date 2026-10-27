//! ISO 4217 minor units, read from the table Go also embeds.

const TABLE: &str = include_str!("../../../internal/currency/minor_units.csv");

/// The decimal places of `code`'s minor unit, or `None` when the table does not hold `code`.
pub fn minor_units(code: &str) -> Option<u32> {
    TABLE
        .lines()
        .filter(|line| !line.starts_with('#'))
        .skip(1)
        .find_map(|line| {
            let (c, units) = line.split_once(',')?;
            if c == code { units.parse().ok() } else { None }
        })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn known_codes_have_their_minor_units() {
        assert_eq!(minor_units("USD"), Some(2));
        assert_eq!(minor_units("JPY"), Some(0));
        assert_eq!(minor_units("KWD"), Some(3));
    }

    #[test]
    fn codes_the_table_does_not_hold_have_none() {
        for code in ["ZZZ", "XAU", "CLF", "XTS", "usd", "", "code"] {
            assert_eq!(minor_units(code), None, "{code}");
        }
    }

    #[test]
    fn every_row_is_a_code_and_a_digit() {
        let rows: Vec<&str> = TABLE
            .lines()
            .filter(|line| !line.starts_with('#') && *line != "code,minor_units")
            .collect();
        assert_eq!(rows.len(), 155);
        for row in rows {
            let (code, units) = row.split_once(',').expect(row);
            assert!(
                code.len() == 3 && code.bytes().all(|b| b.is_ascii_uppercase()),
                "{row}"
            );
            assert!(
                units.len() == 1 && units.bytes().all(|b| b.is_ascii_digit()),
                "{row}"
            );
        }
    }
}
