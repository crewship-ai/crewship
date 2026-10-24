//! Experimental human-delta decoding. Not a transport or admission API.
//! The original bytes must pass the application root/effect audit separately.
use mooring_text::yrs::{
    encoding::{
        read::{Cursor, Error, Read},
        varint::VarInt,
    },
    updates::{
        decoder::{Decode, Decoder},
        encoder::Encode,
    },
    Any, ClientID, Update, ID,
};
use std::sync::Arc;

const MAX_DELTA: usize = 262_144;
const MAX_ORIGIN: u64 = (1 << 53) - 1;

// Private until origin grants, read context and full fork audit are qualified.
pub(crate) fn decode(bytes: &[u8], origin: u64, clock: u32) -> Result<Update, Error> {
    if bytes.is_empty() || bytes.len() > MAX_DELTA || origin == 0 || origin > MAX_ORIGIN {
        return Err(Error::UnexpectedValue);
    }
    let mut probe = AuditedDecoder::new(bytes);
    let clients: u32 = probe.read_var()?;
    if clients > 1 {
        return Err(Error::UnexpectedValue);
    }
    if clients == 1 {
        let blocks: u32 = probe.read_var()?;
        // Check BEFORE upstream allocation, including lengths declared in tiny frames.
        if blocks == 0 || blocks > 4096 || blocks as usize > bytes.len() {
            return Err(Error::UnexpectedValue);
        }
        if probe.read_client()? != ClientID::new(origin) || probe.read_var::<u32>()? != clock {
            return Err(Error::UnexpectedValue);
        }
        // String clock length cannot exceed encoded bytes. Format items each
        // consume bytes too; this bounds upstream unchecked clock increments.
        clock
            .checked_add(bytes.len() as u32)
            .ok_or(Error::UnexpectedValue)?;
    }
    let mut decoder = AuditedDecoder::new(bytes);
    let update = Update::decode(&mut decoder)?;
    if decoder.cursor.has_content() || update.encode_v1() != bytes {
        return Err(Error::UnexpectedValue);
    }
    Ok(update)
}

struct AuditedDecoder<'a> {
    cursor: Cursor<'a>,
    delete_clock: u32,
    property_marker: bool,
}
impl<'a> AuditedDecoder<'a> {
    fn new(bytes: &'a [u8]) -> Self {
        Self {
            cursor: Cursor::new(bytes),
            delete_clock: 0,
            property_marker: false,
        }
    }
    fn id(&mut self) -> Result<ID, Error> {
        let client = self.read_client()?;
        Ok(ID::new(client, self.read_var()?))
    }
}
impl Read for AuditedDecoder<'_> {
    fn read_exact(&mut self, len: usize) -> Result<&[u8], Error> {
        if len > self.cursor.buf.len().saturating_sub(self.cursor.next) {
            return Err(Error::EndOfBuffer(len));
        }
        self.cursor.read_exact(len)
    }
    fn read_var<T: VarInt>(&mut self) -> Result<T, Error> {
        // Audit before ClientID::new (debug panic/release masking) or narrowing
        // u32 decoders. Canonical re-encoding also rejects wrapping/nonminimal
        // varints, including the delete-set's direct read_var::<u64> path.
        let start = self.cursor.next;
        let mut probe = Cursor {
            buf: self.cursor.buf,
            next: start,
        };
        let wide: u64 = probe.read_var()?;
        if wide > MAX_ORIGIN {
            return Err(Error::UnexpectedValue);
        }
        let mut wide_bytes = Vec::new();
        wide.write(&mut wide_bytes);
        if wide_bytes != self.cursor.buf[start..probe.next] {
            return Err(Error::InvalidVarInt);
        }
        let value = T::read(&mut self.cursor)?;
        let mut canonical = Vec::new();
        value.write(&mut canonical);
        if canonical != self.cursor.buf[start..self.cursor.next] {
            return Err(Error::InvalidVarInt);
        }
        Ok(value)
    }
}
impl Decoder for AuditedDecoder<'_> {
    fn reset_ds_cur_val(&mut self) {
        self.delete_clock = 0;
    }
    fn read_ds_clock(&mut self) -> Result<u32, Error> {
        self.delete_clock = self.read_var()?;
        Ok(self.delete_clock)
    }
    fn read_ds_len(&mut self) -> Result<u32, Error> {
        let len = self.read_var()?;
        if len == 0 || self.delete_clock.checked_add(len).is_none() {
            return Err(Error::UnexpectedValue);
        }
        Ok(len)
    }
    fn read_left_id(&mut self) -> Result<ID, Error> {
        self.id()
    }
    fn read_right_id(&mut self) -> Result<ID, Error> {
        self.id()
    }
    fn read_client(&mut self) -> Result<ClientID, Error> {
        let client: u64 = self.read_var()?;
        Ok(ClientID::new(client))
    }
    fn read_info(&mut self) -> Result<u8, Error> {
        let info = self.read_u8()?;
        // Text/format only; forbid parent_sub/map keys, GC, skips, embeds,
        // nested types and arbitrary Any values even if later deleted.
        if info & 0x20 != 0 || !matches!(info & 0x1f, 4 | 6) {
            return Err(Error::UnexpectedValue);
        }
        Ok(info)
    }
    fn read_parent_info(&mut self) -> Result<bool, Error> {
        match self.read_var::<u32>()? {
            0 => Ok(false),
            1 => Ok(true),
            _ => Err(Error::UnexpectedValue),
        }
    }
    fn read_type_ref(&mut self) -> Result<u8, Error> {
        Err(Error::UnexpectedValue)
    }
    fn read_len(&mut self) -> Result<u32, Error> {
        self.read_var()
    }
    fn read_any(&mut self) -> Result<Any, Error> {
        Err(Error::UnexpectedValue)
    }
    fn read_json(&mut self) -> Result<Any, Error> {
        let value = Any::from_json(self.read_string()?)?;
        if !matches!(value, Any::Bool(_) | Any::Null)
            || (self.property_marker && matches!(value, Any::Bool(false)))
        {
            return Err(Error::UnexpectedValue);
        }
        Ok(value)
    }
    fn read_key(&mut self) -> Result<Arc<str>, Error> {
        let key: Arc<str> = self.read_string()?.into();
        self.property_marker = key.as_ref() == crate::PROPS_PRESENT;
        if !self.property_marker
            && !crate::CHARACTER_FLAGS
                .iter()
                .any(|flag| key.as_ref() == format!("{}{flag}", crate::CHARACTER_PREFIX))
        {
            return Err(Error::UnexpectedValue);
        }
        Ok(key)
    }
    fn read_to_end(&mut self) -> Result<&[u8], Error> {
        Err(Error::UnexpectedValue)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mooring_text::yrs::{ClientID, Doc, Options, ReadTxn, StateVector, Text, Transact};

    fn delta() -> Vec<u8> {
        let doc = Doc::with_options(Options {
            client_id: ClientID::new(17),
            ..Options::default()
        });
        let text = doc.get_or_insert_text("wordcraft.test");
        text.insert(&mut doc.transact_mut(), 0, "čau 🦀");
        let bytes = doc
            .transact()
            .encode_state_as_update_v1(&StateVector::default());
        bytes
    }

    #[test]
    fn actual_text_update_requires_exact_host_origin_and_clock() {
        let bytes = delta();
        assert!(decode(&bytes, 17, 0).is_ok());
        assert!(decode(&bytes, 18, 0).is_err());
        assert!(decode(&bytes, 17, 1).is_err());
    }
    #[test]
    fn hostile_lengths_ids_and_delete_ranges_fail_without_panicking() {
        use mooring_text::yrs::encoding::write::Write;
        let mut cases = vec![vec![], vec![0xff; 128], vec![2], vec![0x80, 0]];
        for origin in [1_u64 << 53, u64::MAX] {
            let mut bytes = vec![1, 1];
            bytes.write_var(origin);
            bytes.extend_from_slice(&[0, 4, 1, 1, b'x', 1, b'a', 0]);
            cases.push(bytes);
            // Delete-set clients bypass Decoder::read_client upstream.
            let mut bytes = vec![0, 1];
            bytes.write_var(origin);
            bytes.extend_from_slice(&[1, 0, 1]);
            cases.push(bytes);
        }
        let mut allocation = vec![1];
        allocation.write_var(u32::MAX);
        cases.push(allocation);
        let mut range_overflow = vec![0, 1, 17, 1];
        range_overflow.write_var(u32::MAX);
        range_overflow.push(1);
        cases.push(range_overflow);
        let mut trailing = delta();
        trailing.push(0);
        cases.push(trailing);
        for bytes in cases {
            let result = std::panic::catch_unwind(|| decode(&bytes, 17, 0));
            assert!(result.is_ok(), "decoder panicked on {bytes:?}");
            assert!(result.unwrap().is_err(), "accepted malformed {bytes:?}");
        }
        assert!(decode(&delta(), 17, u32::MAX).is_err());
        assert!(decode(&delta(), 1 << 53, 0).is_err());
    }
}
