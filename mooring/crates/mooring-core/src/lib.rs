//! Application-independent collaboration primitives. Not connected to a runtime.
pub mod adapter;
pub mod batch;
pub mod causal;
pub mod lease;
pub mod prepare;
pub mod resolve;
pub mod session;
pub mod schedule;
#[cfg(not(target_arch = "wasm32"))]
pub mod actor;
