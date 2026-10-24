//! Bounded closed v2 current-frontier helper, not a network decoder.
use super::experimental_human_current::Error;
use mooring_text::yrs::{
    encoding::read::{Cursor, Error as WireError, Read},
    updates::{
        decoder::{Decode, DecoderV1},
        encoder::Encode,
    },
    Any, ClientID, Update,
};
use std::collections::BTreeMap;
struct Rules<'a> {
    origin: u64,
    stamp: u64,
    history: &'a BTreeMap<String, Any>,
}
fn decoder<'a>(bytes: &'a [u8], rules: Option<Rules<'a>>) -> CheckedDecoder<'a> {
    CheckedDecoder {
        inner: DecoderV1::from(bytes),
        rules,
        key: None,
        values: BTreeMap::new(),
    }
}
pub(super) fn history(bytes: &[u8]) -> Result<BTreeMap<String, Any>, Error> {
    crate::checkpoint_wire::preflight(bytes).map_err(|_| Error::InvalidCheckpoint)?;
    let mut decoder = decoder(bytes, None);
    Update::decode(&mut decoder).map_err(|_| Error::InvalidCheckpoint)?;
    Ok(decoder.values)
}
pub(super) fn committed_own_history(
    bytes: &[u8],
    origin: u64,
) -> Result<BTreeMap<String, Any>, Error> {
    if bytes.is_empty() || bytes.len() > 262144 {
        return Err(Error::InvalidHostContext);
    }
    let values = history(bytes).map_err(|_| Error::InvalidHostContext)?;
    let mut decoder = decoder(
        bytes,
        Some(Rules {
            origin,
            stamp: 0,
            history: &values,
        }),
    );
    let update = Update::decode(&mut decoder).map_err(|_| Error::InvalidHostContext)?;
    if update
        .insertions(true)
        .iter()
        .any(|(client, _)| *client != ClientID::new(origin))
        || update.encode_v1() != bytes
    {
        return Err(Error::InvalidHostContext);
    }
    Ok(values)
}
pub(super) fn original(
    bytes: &[u8],
    origin: u64,
    clock: u32,
    stamp: u64,
    history: &BTreeMap<String, Any>,
) -> Result<Update, Error> {
    if bytes.is_empty() || bytes.len() > 262144 {
        return Err(Error::InvalidUpdate);
    }
    crate::checkpoint_wire::preflight(bytes).map_err(|_| Error::InvalidUpdate)?;
    let mut prefix = Cursor::new(bytes);
    let clients: u32 = prefix.read_var().map_err(|_| Error::InvalidUpdate)?;
    if clients > 1 {
        return Err(Error::InvalidUpdate);
    }
    if clients == 1 {
        let _: u32 = prefix.read_var().map_err(|_| Error::InvalidUpdate)?;
        let allocated_origin: u64 = prefix.read_var().map_err(|_| Error::InvalidUpdate)?;
        let allocated_clock: u32 = prefix.read_var().map_err(|_| Error::InvalidUpdate)?;
        if allocated_origin != origin {
            return Err(Error::InvalidUpdate);
        }
        if allocated_clock != clock {
            return Err(Error::UnqualifiedFrontier);
        }
    }
    let mut decoder = decoder(
        bytes,
        Some(Rules {
            origin,
            stamp,
            history,
        }),
    );
    let update = Update::decode(&mut decoder).map_err(|_| Error::UnqualifiedFrontier)?;
    if update.encode_v1() != bytes {
        return Err(Error::InvalidUpdate);
    }
    if update
        .insertions(true)
        .iter()
        .any(|(client, _)| *client != ClientID::new(origin))
    {
        return Err(Error::InvalidUpdate);
    }
    Ok(update)
}
struct CheckedDecoder<'a> {
    inner: mooring_text::yrs::updates::decoder::DecoderV1<'a>,
    rules: Option<Rules<'a>>,
    key: Option<std::sync::Arc<str>>,
    values: BTreeMap<String, Any>,
}
impl mooring_text::yrs::encoding::read::Read for CheckedDecoder<'_> {
    fn read_exact(
        &mut self,
        len: usize,
    ) -> Result<&[u8], mooring_text::yrs::encoding::read::Error> {
        self.inner.read_exact(len)
    }
}
impl mooring_text::yrs::updates::decoder::Decoder for CheckedDecoder<'_> {
    fn reset_ds_cur_val(&mut self) {
        self.inner.reset_ds_cur_val();
    }
    fn read_ds_clock(&mut self) -> Result<u32, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_ds_clock()
    }
    fn read_ds_len(&mut self) -> Result<u32, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_ds_len()
    }
    fn read_left_id(
        &mut self,
    ) -> Result<mooring_text::yrs::ID, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_left_id()
    }
    fn read_right_id(
        &mut self,
    ) -> Result<mooring_text::yrs::ID, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_right_id()
    }
    fn read_client(
        &mut self,
    ) -> Result<mooring_text::yrs::ClientID, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_client()
    }
    fn read_info(&mut self) -> Result<u8, mooring_text::yrs::encoding::read::Error> {
        let info = self.inner.read_info()?;
        if self.rules.is_some() && (info & 0x20 != 0 || !matches!(info & 0x1f, 4 | 6)) {
            return Err(WireError::UnexpectedValue);
        }
        Ok(info)
    }
    fn read_parent_info(&mut self) -> Result<bool, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_parent_info()
    }
    fn read_type_ref(&mut self) -> Result<u8, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_type_ref()
    }
    fn read_len(&mut self) -> Result<u32, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_len()
    }
    fn read_any(&mut self) -> Result<Any, mooring_text::yrs::encoding::read::Error> {
        self.inner.read_any()
    }
    fn read_json(&mut self) -> Result<Any, WireError> {
        let value = self.inner.read_json()?;
        if let Some(key) = &self.key {
            if key.starts_with(crate::format::PREFIX) {
                let known = self
                    .rules
                    .as_ref()
                    .and_then(|rules| rules.history.get(key.as_ref()))
                    .or_else(|| self.values.get(key.as_ref()));
                if matches!(value, Any::Null) {
                    if self.rules.is_some() && known.is_none() {
                        return Err(WireError::UnexpectedValue);
                    }
                } else {
                    if let Some(prior) = known {
                        if prior != &value {
                            return Err(WireError::UnexpectedValue);
                        }
                    }
                    self.values.insert(key.to_string(), value.clone());
                }
            }
        }
        Ok(value)
    }
    fn read_key(&mut self) -> Result<std::sync::Arc<str>, WireError> {
        let key = self.inner.read_key()?;
        if let Some(rules) = &self.rules {
            let parsed = crate::format::parse_key(&key).map_err(|_| WireError::UnexpectedValue)?;
            if parsed.origin != rules.origin
                || (parsed.stamp != rules.stamp && !rules.history.contains_key(key.as_ref()))
            {
                return Err(WireError::UnexpectedValue);
            }
        }
        self.key = Some(key.clone());
        Ok(key)
    }
    fn read_to_end(&mut self) -> Result<&[u8], mooring_text::yrs::encoding::read::Error> {
        self.inner.read_to_end()
    }
}
