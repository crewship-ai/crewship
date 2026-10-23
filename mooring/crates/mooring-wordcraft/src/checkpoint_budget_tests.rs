//! Allocation evidence for tiny malformed host checkpoint updates.
use crate::*;
use std::{
    alloc::{GlobalAlloc, Layout, System},
    cell::Cell,
};
thread_local! { static TRACK: Cell<Option<usize>> = const { Cell::new(None) }; }
struct Tracked;
fn observe(size: usize) {
    let _ = TRACK.try_with(|slot| {
        if let Some(previous) = slot.get() {
            slot.set(Some(previous.max(size)));
        }
    });
}
unsafe impl GlobalAlloc for Tracked {
    unsafe fn alloc(&self, layout: Layout) -> *mut u8 {
        observe(layout.size());
        unsafe { System.alloc(layout) }
    }
    unsafe fn alloc_zeroed(&self, layout: Layout) -> *mut u8 {
        observe(layout.size());
        unsafe { System.alloc_zeroed(layout) }
    }
    unsafe fn dealloc(&self, ptr: *mut u8, layout: Layout) {
        unsafe { System.dealloc(ptr, layout) }
    }
    unsafe fn realloc(&self, ptr: *mut u8, layout: Layout, size: usize) -> *mut u8 {
        observe(size);
        unsafe { System.realloc(ptr, layout, size) }
    }
}
#[global_allocator]
static ALLOCATOR: Tracked = Tracked;
pub(crate) fn measure<T>(run: impl FnOnce() -> T) -> (T, usize) {
    TRACK.with(|slot| slot.set(Some(0)));
    let result = run();
    let maximum = TRACK.with(|slot| slot.replace(None)).unwrap();
    (result, maximum)
}
fn corrupt(update: &[u8]) -> Vec<u8> {
    let native = serde_json::from_str(include_str!("../tests/fixtures/d1-native.json")).unwrap();
    let ids = (0..8).map(|index| format!("budget-{index}")).collect();
    let mut checkpoint: Value =
        serde_json::from_slice(&Document::import(native, ids).unwrap().checkpoint().unwrap())
            .unwrap();
    checkpoint["update_base64"] =
        json!(mooring_protocol::boundary::encode_base64(update, MAX_CRDT_UPDATE_BYTES).unwrap());
    serde_json::to_vec(&checkpoint).unwrap()
}
#[test]
fn tiny_checkpoint_count_never_requests_amplified_allocation() {
    use mooring_text::yrs::encoding::write::Write;
    let mut update = vec![];
    update.write_var(1_000_000_u32);
    let bytes = corrupt(&update);
    TRACK.with(|slot| slot.set(Some(0)));
    let result = Document::restore(&bytes);
    let maximum = TRACK.with(|slot| slot.replace(None)).unwrap();
    assert!(result.is_err());
    assert!(maximum < 1_048_576, "tiny update requested {maximum} bytes");
}

#[test]
fn tiny_checkpoint_any_never_requests_amplified_allocation() {
    use mooring_text::yrs::encoding::write::Write;
    let mut update = vec![1, 1, 17, 0, 8, 1];
    update.write_string("wordcraft.metadata");
    update.push(1); // ContentAny count.
    update.push(118); // Recursive Any::Map.
    update.write_var(1_000_000_u32);
    let bytes = corrupt(&update);
    TRACK.with(|slot| slot.set(Some(0)));
    let result = Document::restore(&bytes);
    let maximum = TRACK.with(|slot| slot.replace(None)).unwrap();
    assert!(result.is_err());
    assert!(maximum < 1_048_576, "tiny Any requested {maximum} bytes");
}
