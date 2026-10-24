//! Allocation-free preflight of this adapter's closed checkpoint vocabulary.
//! Not a general Yrs decoder or human admission policy. Run BEFORE any Update
//! decode: declared counts can otherwise allocate far more than encoded bytes.
use crate::{format, AdapterError, CHARACTER_FLAGS, CHARACTER_PREFIX, PROPS_PRESENT};
const MAX_CLIENTS: u64 = 4096;
const MAX_ITEMS: u64 = 262_144;
const MAX_U53: u64 = (1 << 53) - 1;

pub(crate) fn preflight(bytes: &[u8]) -> Result<(), AdapterError> {
    scan(bytes).ok_or(AdapterError::InvalidCheckpoint)
}
fn scan(bytes: &[u8]) -> Option<()> {
    let mut reader = Reader { bytes, at: 0 };
    let clients = reader.uint(MAX_CLIENTS)?;
    let mut total_items = 0_u64;
    for _ in 0..clients {
        let count = reader.uint(MAX_ITEMS)?;
        if count == 0 {
            return None;
        }
        total_items = total_items.checked_add(count)?;
        if total_items > MAX_ITEMS {
            return None;
        }
        reader.uint(MAX_U53)?; // Original CRDT client ID, not authentication.
        let mut clock = reader.uint(u32::MAX as u64)?;
        for _ in 0..count {
            let info = reader.byte()?;
            let len = match info {
                0 | 10 => reader.uint(u32::MAX as u64)?, // GC / skip.
                _ => {
                    if info & 0x10 != 0 {
                        return None;
                    }
                    let has_origin = info & 0x80 != 0;
                    let has_right = info & 0x40 != 0;
                    if has_origin {
                        reader.id()?;
                    }
                    if has_right {
                        reader.id()?;
                    }
                    if !has_origin && !has_right {
                        match reader.uint(1)? {
                            0 => reader.id()?,
                            _ => {
                                reader.string()?;
                            }
                        }
                        if info & 0x20 != 0 {
                            reader.string()?;
                        }
                    }
                    match info & 0x0f {
                        1 => reader.uint(u32::MAX as u64)?, // Deleted item.
                        4 => u32::try_from(reader.string()?.encode_utf16().count()).ok()? as u64,
                        6 => {
                            let key = reader.string()?;
                            if key != PROPS_PRESENT
                                && !CHARACTER_FLAGS
                                    .iter()
                                    .any(|flag| key.strip_prefix(CHARACTER_PREFIX) == Some(*flag))
                                && format::parse_key(key).is_err()
                            {
                                return None;
                            }
                            // Closed scalar JSON vocabulary: never recurse into
                            // attacker-selected Any maps/arrays or JSON trees.
                            if !matches!(
                                reader.string()?,
                                "true" | "false" | "null" | "\"inherit\""
                            ) {
                                return None;
                            }
                            1
                        }
                        7 => {
                            reader.uint(2)?;
                            1
                        } // Array, Map, Text only.
                        8 => {
                            let count = reader.uint(MAX_ITEMS)?;
                            if count == 0 {
                                return None;
                            }
                            // Our map metadata/block values and array IDs are
                            // strings. Reject recursive Any BEFORE upstream decode.
                            for _ in 0..count {
                                if reader.byte()? != 119 {
                                    return None;
                                }
                                reader.string()?;
                            }
                            count
                        }
                        _ => return None, // JSON arrays, embeds, binary, subdocs.
                    }
                }
            };
            if len == 0 {
                return None;
            }
            clock = clock.checked_add(len)?;
            if clock > u32::MAX as u64 {
                return None;
            }
        }
    }
    let clients = reader.uint(MAX_CLIENTS)?;
    let mut total_ranges = 0_u64;
    for _ in 0..clients {
        reader.uint(MAX_U53)?;
        let count = reader.uint(MAX_ITEMS)?;
        total_ranges = total_ranges.checked_add(count)?;
        if total_ranges > MAX_ITEMS {
            return None;
        }
        for _ in 0..count {
            let clock = reader.uint(u32::MAX as u64)?;
            let len = reader.uint(u32::MAX as u64)?;
            if len == 0 || clock.checked_add(len)? > u32::MAX as u64 {
                return None;
            }
        }
    }
    (reader.at == bytes.len()).then_some(())
}
struct Reader<'a> {
    bytes: &'a [u8],
    at: usize,
}
impl<'a> Reader<'a> {
    fn byte(&mut self) -> Option<u8> {
        let value = *self.bytes.get(self.at)?;
        self.at += 1;
        Some(value)
    }
    fn uint(&mut self, max: u64) -> Option<u64> {
        let mut result = 0_u64;
        for index in 0..8 {
            let byte = self.byte()?;
            result |= ((byte & 0x7f) as u64).checked_shl(index * 7)?;
            if byte & 0x80 == 0 {
                if result > max || (index != 0 && byte == 0) {
                    return None;
                }
                return Some(result);
            }
        }
        None
    }
    fn id(&mut self) -> Option<()> {
        self.uint(MAX_U53)?;
        self.uint(u32::MAX as u64)?;
        Some(())
    }
    fn string(&mut self) -> Option<&'a str> {
        let len = usize::try_from(self.uint(crate::MAX_NATIVE_BYTES as u64)?).ok()?;
        let end = self.at.checked_add(len)?;
        let bytes = self.bytes.get(self.at..end)?;
        self.at = end;
        std::str::from_utf8(bytes).ok()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_text::yrs::encoding::write::Write;
    fn any(tag: u8, tail: &[u8]) -> Vec<u8> {
        let mut bytes = vec![1, 1, 17, 0, 8, 1];
        bytes.write_string("wordcraft.metadata");
        bytes.push(1);
        bytes.push(tag);
        bytes.extend_from_slice(tail);
        bytes.push(0);
        bytes
    }
    #[test]
    fn hostile_counts_recursive_any_and_unknown_content_fail_before_decode() {
        let mut cases = vec![vec![], vec![0x80, 0, 0], vec![0xff; 16], vec![0, 0, 0]];
        for count in [MAX_CLIENTS + 1, u32::MAX as u64, u64::MAX] {
            let mut bytes = vec![];
            bytes.write_var(count);
            cases.push(bytes);
            let mut bytes = vec![1];
            bytes.write_var(count);
            cases.push(bytes);
        }
        let mut recursive = vec![];
        for _ in 0..4096 {
            recursive.extend_from_slice(&[117, 1]);
        }
        cases.push(any(117, &recursive));
        cases.push(any(118, &[255, 255, 255, 127]));
        cases.push(any(119, &[0xff; 16]));
        cases.push(any(119, &[1, 0xff])); // Invalid UTF-8.
        cases.push(any(125, &[1])); // Numeric Any is not native/ID storage.
        for kind in [2, 3, 5, 9, 16, 255] {
            cases.push(vec![1, 1, 17, 0, kind, 1, 0, 0]);
        }
        // GC clock overflow, overflowing delete range, noncanonical client ID.
        let mut bytes = vec![1, 1, 17];
        bytes.write_var(u32::MAX);
        bytes.extend_from_slice(&[0, 1, 0]);
        cases.push(bytes);
        let mut bytes = vec![0, 1, 17, 1];
        bytes.write_var(u32::MAX);
        bytes.push(1);
        cases.push(bytes);
        cases.push(vec![1, 1, 0x91, 0, 0, 0, 1, 0]);
        for bytes in cases {
            assert!(
                preflight(&bytes).is_err(),
                "accepted hostile bytes {bytes:?}"
            );
        }
    }
    #[test]
    fn preflight_has_no_allocations_and_consumes_exact_complete_frame() {
        use crate::checkpoint_budget_tests::measure;
        let mut bytes = vec![1, 1, 17, 0, 8, 1];
        bytes.write_string("wordcraft.metadata");
        bytes.extend_from_slice(&[1, 119]);
        bytes.write_string("original opaque native JSON");
        bytes.push(0);
        let (result, maximum) = measure(|| preflight(&bytes));
        assert!(result.is_ok());
        assert_eq!(maximum, 0);
        let mut trailing = bytes.clone();
        trailing.push(0);
        assert!(preflight(&trailing).is_err());
        for at in 0..bytes.len() {
            assert!(preflight(&bytes[..at]).is_err());
        }
    }
}
