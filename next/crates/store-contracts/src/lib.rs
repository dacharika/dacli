//! Engine-neutral fixture task commands and results (#1257). This port supplies
//! no authentication, permission grant or source-authority selection.
#![no_std]
#![forbid(unsafe_code)]
extern crate alloc;
use alloc::{string::String, vec::Vec};
use core::fmt::Write;

pub const MAXIMUM_BODY_BYTES: usize = 256 * 1024;
pub const MAXIMUM_ARTIFACT_REFERENCES: usize = 16;
pub const MAXIMUM_QUERY_ROWS: usize = 1024;
pub const CREATE_TASK_ACTION: &str = "CreateTask";

#[derive(Clone, Copy, Debug, Eq, Ord, PartialEq, PartialOrd)]
pub struct ContentDigest(pub [u8; 32]);
impl ContentDigest {
    pub fn hex(self) -> String {
        let mut text = String::with_capacity(64);
        for byte in self.0 {
            let _ = write!(text, "{byte:02x}");
        }
        text
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct StoreIdentity {
    pub authority_id: String,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TaskScope {
    pub authority_id: String,
    pub workspace_id: String,
    pub project_id: String,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TaskReference {
    pub scope: TaskScope,
    pub task_id: String,
}

/// Remaining monotonic budget data, not a wall-clock timestamp or a promise that
/// the operating system will complete disk I/O by a hard realtime deadline.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct OperationBudget {
    pub deadline_millis: u32,
    pub busy_wait_millis: u32,
}
impl Default for OperationBudget {
    fn default() -> Self {
        Self {
            deadline_millis: 5_000,
            busy_wait_millis: 1_000,
        }
    }
}
impl OperationBudget {
    pub fn validate(self) -> Result<(), StoreError> {
        if self.deadline_millis > 30_000
            || self.busy_wait_millis > self.deadline_millis
            || self.busy_wait_millis > 5_000
        {
            return Err(StoreError::InvalidInput("budget"));
        }
        Ok(())
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CreateTask {
    pub scope: TaskScope,
    pub operation_key: String,
    pub task_id: String,
    /// Only zero (absent) is admitted by this initial create-only command.
    pub expected_version: u64,
    pub title: String,
    pub body: Vec<u8>,
    /// A bounded logical set. Adapters canonicalize ordering and reject repeats.
    pub artifact_references: Vec<ContentDigest>,
}
impl CreateTask {
    pub fn validate(&self) -> Result<(), StoreError> {
        self.scope.validate()?;
        validate_identity(&self.operation_key, "operation_key")?;
        validate_identity(&self.task_id, "task_id")?;
        if self.expected_version != 0 {
            return Err(StoreError::InvalidInput("expected_version"));
        }
        if self.title.trim().is_empty()
            || self.title.len() > 1024
            || self.title.chars().any(char::is_control)
        {
            return Err(StoreError::InvalidInput("title"));
        }
        if self.body.len() > MAXIMUM_BODY_BYTES {
            return Err(StoreError::InvalidInput("body"));
        }
        if self.artifact_references.len() > MAXIMUM_ARTIFACT_REFERENCES {
            return Err(StoreError::InvalidInput("artifact_references"));
        }
        let mut refs = self.artifact_references.clone();
        refs.sort();
        if refs.windows(2).any(|pair| pair[0] == pair[1]) {
            return Err(StoreError::InvalidInput("artifact_references"));
        }
        Ok(())
    }
}
impl StoreIdentity {
    pub fn validate(&self) -> Result<(), StoreError> {
        validate_identity(&self.authority_id, "authority_id")
    }
}
impl TaskScope {
    pub fn validate(&self) -> Result<(), StoreError> {
        validate_identity(&self.authority_id, "authority_id")?;
        validate_identity(&self.workspace_id, "workspace_id")?;
        validate_identity(&self.project_id, "project_id")
    }
}
impl TaskReference {
    pub fn validate(&self) -> Result<(), StoreError> {
        self.scope.validate()?;
        validate_identity(&self.task_id, "task_id")
    }
}
pub fn validate_identity(value: &str, field: &'static str) -> Result<(), StoreError> {
    if value.is_empty()
        || value.len() > 128
        || value.chars().any(|c| c.is_control() || c.is_whitespace())
    {
        return Err(StoreError::InvalidInput(field));
    }
    Ok(())
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CommandReceipt {
    pub reference: TaskReference,
    pub operation_key: String,
    pub version: u64,
    pub event_sequence: u64,
    pub input_digest: ContentDigest,
    pub body_digest: ContentDigest,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CreateTaskResult {
    pub receipt: CommandReceipt,
    pub replayed: bool,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TaskRecord {
    pub reference: TaskReference,
    pub version: u64,
    pub title: String,
    pub body: Vec<u8>,
    pub body_digest: ContentDigest,
    pub artifact_references: Vec<ContentDigest>,
    pub event_sequence: u64,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TaskSummary {
    pub reference: TaskReference,
    pub version: u64,
    pub body_digest: ContentDigest,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuditEvent {
    pub receipt: CommandReceipt,
    pub action: String,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum CommandObservation {
    Committed(CommandReceipt),
    Absent,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum StorageFailure {
    Full,
    Io,
    ReadOnly,
    CannotOpen,
    Other,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum StoreError {
    InvalidInput(&'static str),
    IdentityMismatch,
    UnsupportedSchema,
    ConfigurationMismatch,
    Busy,
    DeadlineExceeded,
    OperationConflict,
    TaskAlreadyExists,
    StaleVersion { expected: u64, actual: u64 },
    CorruptData,
    Storage(StorageFailure),
    UnknownCommitOutcome,
    NeedsReconciliation,
    DestinationExists,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ArtifactState {
    Missing,
    Corrupt,
    Unreadable,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ArtifactProblem {
    pub digest: ContentDigest,
    pub state: ArtifactState,
}

pub trait TaskStore {
    fn create_task(
        &mut self,
        command: &CreateTask,
        budget: OperationBudget,
    ) -> Result<CreateTaskResult, StoreError>;
    fn read_task(
        &self,
        reference: &TaskReference,
        expected_version: Option<u64>,
    ) -> Result<Option<TaskRecord>, StoreError>;
    fn query_tasks(&self, scope: &TaskScope, limit: usize) -> Result<Vec<TaskSummary>, StoreError>;
    fn rebuild_query_index(&mut self, budget: OperationBudget) -> Result<usize, StoreError>;
    /// Reconcile the same command identity on a fresh verified store connection.
    /// Absence is an observation of this fixture DB, not a distributed recovery proof.
    fn reconcile_create(
        &self,
        command: &CreateTask,
        budget: OperationBudget,
    ) -> Result<CommandObservation, StoreError>;
}
