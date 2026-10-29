//! Bounded syntax and canonical facts for scoped fixtures (#1258/#1271).
//! Constructors validate shape; they do not authenticate a caller or grant authority.
#![no_std]
#![forbid(unsafe_code)]
extern crate alloc;

mod canonical;
mod command;
mod credential;
mod error;
mod facts;
mod identity;
mod outcome;

pub use canonical::*;
pub use command::*;
pub use credential::*;
pub use error::*;
pub use facts::*;
pub use identity::*;
pub use outcome::*;

pub const MAXIMUM_REQUEST_BYTES: usize = 512 * 1024;
pub const MAXIMUM_CREDENTIAL_BYTES: usize = 128;
pub const MAXIMUM_LINEAGE: usize = 8;
pub const MAXIMUM_GRANTS: usize = 6;
pub const MAXIMUM_FIXTURE_LIFETIME_MILLIS: u64 = 3_600_000;
pub const SCOPED_SCHEMA_VERSION: u16 = 2;
pub const SCOPED_STORE_FORMAT: &str = "dacli-scoped-fixture/v2";
