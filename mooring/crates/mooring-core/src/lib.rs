//! Application-independent collaboration primitives. Not connected to a runtime.
#[cfg(not(target_arch = "wasm32"))]
pub mod actor;
pub mod adapter;
pub mod batch;
pub mod causal;
pub mod checkpoint_description;
pub mod delivery;
pub mod lease;
pub mod prepare;
pub mod resolve;
pub mod schedule;
pub mod session;
