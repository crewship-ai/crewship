//! Native projection from one already authenticated, immutable document. The
//! host must bind every cursor to the same scope and confirmed checkpoint /
//! revision; an application stable ID alone is not a remote pagination token.
use crate::Document;
use serde::Serialize;
use serde_json::Value;

#[derive(Debug, PartialEq, Eq)]
pub enum ReadError {
    InvalidLimit,
    UnknownCursor,
    InvalidState,
    ItemTooLarge,
}
#[derive(Debug, Serialize)]
pub struct Paragraph {
    pub target_id: String,
    pub native: Value,
}
#[derive(Debug, Serialize)]
pub struct Page {
    pub paragraphs: Vec<Paragraph>,
    pub next_after: Option<String>,
}

/// The byte budget covers this complete serialized page, including stable IDs
/// and next cursor. The host reserves its own envelope overhead.
/// A native paragraph is never silently truncated or stripped of metadata.
pub fn page(
    document: &Document,
    after: Option<&str>,
    max_paragraphs: usize,
    max_bytes: usize,
) -> Result<Page, ReadError> {
    if max_paragraphs == 0 || max_paragraphs > 256 || max_bytes == 0 || max_bytes > 262144 {
        return Err(ReadError::InvalidLimit);
    }
    let ids = document.block_ids().map_err(|_| ReadError::InvalidState)?;
    let start = match after {
        None => 0,
        Some(id) => {
            ids.iter()
                .position(|candidate| candidate == id)
                .ok_or(ReadError::UnknownCursor)?
                + 1
        }
    };
    let mut native = document.native().map_err(|_| ReadError::InvalidState)?;
    let body = native
        .as_object_mut()
        .ok_or(ReadError::InvalidState)?
        .remove("body")
        .ok_or(ReadError::InvalidState)?;
    let body = body.as_array().ok_or(ReadError::InvalidState)?;
    if body.len() != ids.len() {
        return Err(ReadError::InvalidState);
    }
    let mut result = Page {
        paragraphs: Vec::new(),
        next_after: None,
    };
    if serde_json::to_vec(&result)
        .map_err(|_| ReadError::InvalidState)?
        .len()
        > max_bytes
    {
        return Err(ReadError::ItemTooLarge);
    }
    for index in start..body.len().min(start.saturating_add(max_paragraphs)) {
        result.paragraphs.push(Paragraph {
            target_id: ids[index].clone(),
            native: body[index].clone(),
        });
        result.next_after = (index + 1 < body.len()).then(|| ids[index].clone());
        if serde_json::to_vec(&result)
            .map_err(|_| ReadError::InvalidState)?
            .len()
            > max_bytes
        {
            result.paragraphs.pop();
            let Some(last) = result.paragraphs.last() else {
                return Err(ReadError::ItemTooLarge);
            };
            result.next_after = Some(last.target_id.clone());
            break;
        }
    }
    Ok(result)
}

/// Complete document-level native fields, excluding only body. No metadata is
/// silently omitted to fit a limit; the host budgets its own response envelope.
pub fn header(document: &Document, max_bytes: usize) -> Result<Value, ReadError> {
    if max_bytes == 0 || max_bytes > 262144 {
        return Err(ReadError::InvalidLimit);
    }
    let mut native = document.native().map_err(|_| ReadError::InvalidState)?;
    native
        .as_object_mut()
        .ok_or(ReadError::InvalidState)?
        .remove("body");
    if serde_json::to_vec(&native)
        .map_err(|_| ReadError::InvalidState)?
        .len()
        > max_bytes
    {
        return Err(ReadError::ItemTooLarge);
    }
    Ok(native)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    fn document() -> Document {
        let mut native: Value =
            serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
        native["body"]=Value::Array((0..43).map(|i| json!({"kind":"para","text":format!("oddíl {i} Č😀"),"runs":[{"len":format!("oddíl {i} Č😀").len()}],"props":{"pageBreakBefore":i%5==0},"section":{"fixture":i}})).collect());
        Document::import(native, (0..43).map(|i| format!("stable-{i}")).collect()).unwrap()
    }
    #[test]
    fn complete_readonly_native_reassembles_over_bounded_pages_with_stable_cursors() {
        let doc = document();
        assert!(doc.is_read_only());
        let original = doc.native().unwrap();
        let mut after = None;
        let mut reconstructed = header(&doc, 32768).unwrap();
        let mut body = Vec::new();
        let mut seen = Vec::new();
        loop {
            let result = page(&doc, after.as_deref(), 7, 400).unwrap();
            assert!(serde_json::to_vec(&result).unwrap().len() <= 400);
            assert!(result.paragraphs.len() < 7);
            for para in result.paragraphs {
                seen.push(para.target_id);
                body.push(para.native);
            }
            after = result.next_after;
            if after.is_none() {
                break;
            }
        }
        reconstructed["body"] = Value::Array(body);
        assert_eq!(reconstructed, original);
        assert_eq!(seen, doc.block_ids().unwrap());
    }
    #[test]
    fn invalid_cursor_limits_and_oversized_items_do_not_truncate() {
        let doc = document();
        assert_eq!(
            page(&doc, Some("foreign"), 7, 32768).unwrap_err(),
            ReadError::UnknownCursor
        );
        for (count, bytes) in [(0, 32768), (7, 0), (257, 32768), (7, 262145)] {
            assert_eq!(
                page(&doc, None, count, bytes).unwrap_err(),
                ReadError::InvalidLimit
            );
        }
        assert_eq!(
            page(&doc, None, 7, 64).unwrap_err(),
            ReadError::ItemTooLarge
        );
        assert_eq!(header(&doc, 64).unwrap_err(), ReadError::ItemTooLarge);
        assert_eq!(header(&doc, 262145).unwrap_err(), ReadError::InvalidLimit);
    }
}
