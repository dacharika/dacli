//! SQLite fixture adapter (#1257). This is not the production authentication
//! gateway, and the command kernel deliberately exposes no task-write transport.
#![forbid(unsafe_code)]
mod artifacts;
mod store;
pub use artifacts::verify_artifact_files;
#[cfg(feature = "qualification")]
pub use store::{CommitCheckpoint, QualificationPause};
pub use store::{FixtureCounts, SqliteFixtureStore};
pub use store_contracts;
