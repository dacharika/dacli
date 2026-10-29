use rusqlite::{
    backup::{Backup, StepResult},
    params, Connection, ErrorCode, OpenFlags, OptionalExtension, TransactionBehavior,
};
use sha2::{Digest, Sha256};
use std::{
    fs::OpenOptions,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};
use store_contracts::*;
const APPLICATION_ID: i64 = 0x44534631;
const SCHEMA_VERSION: i64 = 1;
const FORMAT: &str = "dacli-task-fixture/v1";
const MAXIMUM_FIXTURE_TASKS: i64 = 1024;
const QUERY_SCHEMA: &str = "CREATE TABLE IF NOT EXISTS task_query (workspace_id TEXT NOT NULL, project_id TEXT NOT NULL, task_id TEXT NOT NULL, version INTEGER NOT NULL, body_digest BLOB NOT NULL, PRIMARY KEY(workspace_id,project_id,task_id))";

pub struct SqliteFixtureStore {
    connection: Connection,
    path: PathBuf,
    identity: StoreIdentity,
    uncertain: bool,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct FixtureCounts {
    pub tasks: u64,
    pub versions: u64,
    pub events: u64,
    pub results: u64,
}
struct Deadline {
    end: Instant,
    busy: Duration,
}
impl Deadline {
    fn new(budget: OperationBudget) -> Result<Self, StoreError> {
        budget.validate()?;
        Ok(Self {
            end: Instant::now() + Duration::from_millis(budget.deadline_millis.into()),
            busy: Duration::from_millis(budget.busy_wait_millis.into()),
        })
    }
    fn check(&self) -> Result<(), StoreError> {
        if Instant::now() >= self.end {
            Err(StoreError::DeadlineExceeded)
        } else {
            Ok(())
        }
    }
    fn configure(&self, connection: &Connection) -> Result<(), StoreError> {
        self.check()?;
        connection
            .busy_timeout(
                self.busy
                    .min(self.end.saturating_duration_since(Instant::now())),
            )
            .map_err(map_sql)
    }
    fn map(&self, error: rusqlite::Error) -> StoreError {
        match map_sql(error) {
            StoreError::Busy if Instant::now() >= self.end => StoreError::DeadlineExceeded,
            other => other,
        }
    }
}
impl SqliteFixtureStore {
    pub fn create_new(path: &Path, identity: StoreIdentity) -> Result<Self, StoreError> {
        identity.validate()?;
        let normalized = normalized_path(path)?;
        let path = normalized.as_path();
        reserve_file(path)?;
        let mut connection = connect(path, false)?;
        configure(&connection, true)?;
        let tx = connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .map_err(map_sql)?;
        tx.execute_batch(include_str!("schema.sql"))
            .map_err(map_sql)?;
        tx.execute(
            "INSERT INTO store_identity VALUES (1,?1,?2)",
            params![identity.authority_id, FORMAT],
        )
        .map_err(map_sql)?;
        tx.pragma_update(None, "application_id", APPLICATION_ID)
            .map_err(map_sql)?;
        tx.pragma_update(None, "user_version", SCHEMA_VERSION)
            .map_err(map_sql)?;
        tx.commit().map_err(|_| StoreError::UnknownCommitOutcome)?;
        Ok(Self {
            connection,
            path: path.to_path_buf(),
            identity,
            uncertain: false,
        })
    }
    pub fn open_existing(path: &Path, identity: StoreIdentity) -> Result<Self, StoreError> {
        identity.validate()?;
        let normalized = normalized_path(path)?;
        let path = normalized.as_path();
        let connection = connect(path, false)?;
        verify_identity(&connection, &identity)?;
        configure(&connection, false)?;
        Ok(Self {
            connection,
            path: path.to_path_buf(),
            identity,
            uncertain: false,
        })
    }
    pub fn sqlite_version(&self) -> Result<String, StoreError> {
        self.connection
            .query_row("SELECT sqlite_version()", [], |r| r.get(0))
            .map_err(map_sql)
    }
    fn scope(&self, scope: &TaskScope) -> Result<(), StoreError> {
        scope.validate()?;
        if scope.authority_id != self.identity.authority_id {
            Err(StoreError::IdentityMismatch)
        } else {
            Ok(())
        }
    }
    // All four counts belong to one read snapshot, including concurrent commits.
    pub fn counts(&self) -> Result<FixtureCounts, StoreError> {
        let snapshot = self.read_snapshot()?;
        snapshot.query_row("SELECT (SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM task_versions), (SELECT COUNT(*) FROM audit_events), (SELECT COUNT(*) FROM operation_results)", [], |row| Ok(FixtureCounts { tasks: row.get(0)?, versions: row.get(1)?, events: row.get(2)?, results: row.get(3)? })).map_err(map_sql)
    }
    fn read_snapshot(&self) -> Result<rusqlite::Transaction<'_>, StoreError> {
        let transaction = self.connection.unchecked_transaction().map_err(map_sql)?;
        verify_identity(&transaction, &self.identity)?;
        Ok(transaction)
    }
    pub fn audit_event(
        &self,
        scope: &TaskScope,
        key: &str,
    ) -> Result<Option<AuditEvent>, StoreError> {
        self.scope(scope)?;
        validate_identity(key, "operation_key")?;
        let snapshot = self.read_snapshot()?;
        let row=snapshot.query_row("SELECT task_id,version,sequence,input_digest,body_digest FROM audit_events WHERE workspace_id=?1 AND project_id=?2 AND action=?3 AND operation_key=?4",params![scope.workspace_id,scope.project_id,CREATE_TASK_ACTION,key],receipt_row).optional().map_err(map_sql)?;
        row.map(|(task_id, version, event_sequence, input, body)| {
            Ok(AuditEvent {
                receipt: CommandReceipt {
                    reference: TaskReference {
                        scope: scope.clone(),
                        task_id,
                    },
                    operation_key: key.into(),
                    version,
                    event_sequence,
                    input_digest: decode_digest(input)?,
                    body_digest: decode_digest(body)?,
                },
                action: CREATE_TASK_ACTION.into(),
            })
        })
        .transpose()
    }
    /// SQLite's online backup includes uncheckpointed WAL transactions. A failed
    /// backup leaves its newly reserved destination preserved, never called valid.
    pub fn backup_to(&self, path: &Path, budget: OperationBudget) -> Result<(), StoreError> {
        let deadline = Deadline::new(budget)?;
        deadline.check()?;
        let normalized = normalized_path(path)?;
        let path = normalized.as_path();
        reserve_file(path)?;
        let mut destination = connect(path, false)?;
        let snapshot = self.read_snapshot()?;
        {
            let backup = Backup::new(&snapshot, &mut destination).map_err(map_sql)?;
            loop {
                deadline.check()?;
                match backup.step(32).map_err(|e| deadline.map(e))? {
                    StepResult::Done => break,
                    StepResult::More => {}
                    StepResult::Busy | StepResult::Locked => return Err(StoreError::Busy),
                    _ => return Err(StoreError::Storage(StorageFailure::Other)),
                }
            }
        }
        verify_identity(&destination, &self.identity)?;
        let integrity: String = destination
            .query_row("PRAGMA integrity_check", [], |r| r.get(0))
            .map_err(map_sql)?;
        if integrity != "ok" {
            return Err(StoreError::CorruptData);
        }
        Ok(())
    }
    fn create_inner(
        &mut self,
        command: &CreateTask,
        deadline: &Deadline,
        #[cfg(feature = "qualification")] pause: Option<&QualificationPause>,
    ) -> Result<CreateTaskResult, StoreError> {
        if self.uncertain {
            return Err(StoreError::NeedsReconciliation);
        }
        command.validate()?;
        self.scope(&command.scope)?;
        let input_digest = input_digest(command);
        let body_digest = hash_bytes(&command.body);
        let mut refs = command.artifact_references.clone();
        refs.sort();
        let references = pack_refs(&refs);
        deadline.configure(&self.connection)?;
        let tx = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .map_err(|e| deadline.map(e))?;
        verify_identity(&tx, &self.identity)?;
        // Key lookup is inside the same write transaction as preconditions and writes.
        if let Some(stored) = receipt(&tx, &command.scope, &command.operation_key)? {
            if stored.input_digest != input_digest {
                return Err(StoreError::OperationConflict);
            }
            verify_receipt(&tx, &stored)?;
            return Ok(CreateTaskResult {
                receipt: stored,
                replayed: true,
            });
        }
        deadline.check()?;
        let exists:bool=tx.query_row("SELECT EXISTS(SELECT 1 FROM tasks WHERE workspace_id=?1 AND project_id=?2 AND task_id=?3)",params![command.scope.workspace_id,command.scope.project_id,command.task_id],|r|r.get(0)).map_err(|e|deadline.map(e))?;
        if exists {
            return Err(StoreError::TaskAlreadyExists);
        }
        let count: i64 = tx
            .query_row("SELECT COUNT(*) FROM tasks", [], |r| r.get(0))
            .map_err(map_sql)?;
        if count >= MAXIMUM_FIXTURE_TASKS {
            return Err(StoreError::InvalidInput("fixture_capacity"));
        }
        tx.execute("INSERT INTO audit_events(workspace_id,project_id,action,operation_key,task_id,version,input_digest,body_digest) VALUES (?1,?2,?3,?4,?5,1,?6,?7)",params![command.scope.workspace_id,command.scope.project_id,CREATE_TASK_ACTION,command.operation_key,command.task_id,input_digest.0.as_slice(),body_digest.0.as_slice()]).map_err(|e|deadline.map(e))?;
        let event_sequence = tx.last_insert_rowid();
        tx.execute(
            "INSERT INTO task_versions VALUES (?1,?2,?3,1,?4,?5,?6,?7,?8)",
            params![
                command.scope.workspace_id,
                command.scope.project_id,
                command.task_id,
                command.title,
                command.body,
                body_digest.0.as_slice(),
                references,
                event_sequence
            ],
        )
        .map_err(|e| deadline.map(e))?;
        tx.execute(
            "INSERT INTO tasks VALUES (?1,?2,?3,1)",
            params![
                command.scope.workspace_id,
                command.scope.project_id,
                command.task_id
            ],
        )
        .map_err(|e| deadline.map(e))?;
        tx.execute(
            "INSERT INTO operation_results VALUES (?1,?2,?3,?4,?5,1,?6,?7,?8)",
            params![
                command.scope.workspace_id,
                command.scope.project_id,
                CREATE_TASK_ACTION,
                command.operation_key,
                command.task_id,
                input_digest.0.as_slice(),
                body_digest.0.as_slice(),
                event_sequence
            ],
        )
        .map_err(|e| deadline.map(e))?;
        tx.execute(
            "INSERT INTO task_query VALUES (?1,?2,?3,1,?4)",
            params![
                command.scope.workspace_id,
                command.scope.project_id,
                command.task_id,
                body_digest.0.as_slice()
            ],
        )
        .map_err(|e| deadline.map(e))?;
        deadline.check()?;
        #[cfg(feature = "qualification")]
        if let Some(pause) = pause {
            pause.at(CommitCheckpoint::BeforeCommit)?;
        }
        // No acknowledgement exists until SQLite has returned a successful COMMIT.
        if tx.commit().is_err() {
            self.uncertain = true;
            return Err(StoreError::UnknownCommitOutcome);
        }
        #[cfg(feature = "qualification")]
        if let Some(pause) = pause {
            if pause.at(CommitCheckpoint::AfterCommit).is_err() {
                self.uncertain = true;
                return Err(StoreError::UnknownCommitOutcome);
            }
        }
        Ok(CreateTaskResult {
            receipt: CommandReceipt {
                reference: TaskReference {
                    scope: command.scope.clone(),
                    task_id: command.task_id.clone(),
                },
                operation_key: command.operation_key.clone(),
                version: 1,
                event_sequence: u64::try_from(event_sequence)
                    .map_err(|_| StoreError::CorruptData)?,
                input_digest,
                body_digest,
            },
            replayed: false,
        })
    }
}
impl TaskStore for SqliteFixtureStore {
    fn create_task(
        &mut self,
        command: &CreateTask,
        budget: OperationBudget,
    ) -> Result<CreateTaskResult, StoreError> {
        let deadline = Deadline::new(budget)?;
        self.create_inner(
            command,
            &deadline,
            #[cfg(feature = "qualification")]
            None,
        )
    }
    fn read_task(
        &self,
        reference: &TaskReference,
        expected: Option<u64>,
    ) -> Result<Option<TaskRecord>, StoreError> {
        self.scope(&reference.scope)?;
        reference.validate()?;
        let snapshot = self.read_snapshot()?;
        read_task(&snapshot, reference, expected)
    }
    fn query_tasks(&self, scope: &TaskScope, limit: usize) -> Result<Vec<TaskSummary>, StoreError> {
        self.scope(scope)?;
        if limit == 0 || limit > MAXIMUM_QUERY_ROWS {
            return Err(StoreError::InvalidInput("query_limit"));
        }
        let snapshot = self.read_snapshot()?;
        let mut stmt=snapshot.prepare("SELECT task_id,version,body_digest FROM task_query WHERE workspace_id=?1 AND project_id=?2 ORDER BY task_id COLLATE BINARY LIMIT ?3").map_err(map_sql)?;
        let values = stmt
            .query_map(
                params![scope.workspace_id, scope.project_id, limit as i64],
                |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, u64>(1)?,
                        r.get::<_, Vec<u8>>(2)?,
                    ))
                },
            )
            .map_err(map_sql)?;
        let mut result = Vec::new();
        for row in values {
            let (task_id, version, body) = row.map_err(map_sql)?;
            let reference = TaskReference {
                scope: scope.clone(),
                task_id,
            };
            let digest = decode_digest(body)?;
            let canonical =
                read_task(&snapshot, &reference, Some(version))?.ok_or(StoreError::CorruptData)?;
            if canonical.body_digest != digest {
                return Err(StoreError::CorruptData);
            }
            result.push(TaskSummary {
                reference,
                version,
                body_digest: digest,
            });
        }
        Ok(result)
    }
    fn rebuild_query_index(&mut self, budget: OperationBudget) -> Result<usize, StoreError> {
        if self.uncertain {
            return Err(StoreError::NeedsReconciliation);
        }
        let deadline = Deadline::new(budget)?;
        deadline.configure(&self.connection)?;
        let tx = self
            .connection
            .transaction_with_behavior(TransactionBehavior::Immediate)
            .map_err(|e| deadline.map(e))?;
        verify_identity(&tx, &self.identity)?;
        let mut refs = Vec::new();
        {
            let mut stmt=tx.prepare("SELECT workspace_id,project_id,task_id FROM tasks ORDER BY workspace_id,project_id,task_id LIMIT 1025").map_err(map_sql)?;
            let rows = stmt
                .query_map([], |r| {
                    Ok((
                        r.get::<_, String>(0)?,
                        r.get::<_, String>(1)?,
                        r.get::<_, String>(2)?,
                    ))
                })
                .map_err(map_sql)?;
            for row in rows {
                let (workspace_id, project_id, task_id) = row.map_err(map_sql)?;
                refs.push(TaskReference {
                    scope: TaskScope {
                        authority_id: self.identity.authority_id.clone(),
                        workspace_id,
                        project_id,
                    },
                    task_id,
                });
            }
        }
        if refs.len() > MAXIMUM_FIXTURE_TASKS as usize {
            return Err(StoreError::CorruptData);
        }
        for reference in &refs {
            deadline.check()?;
            read_task(&tx, reference, None)?.ok_or(StoreError::CorruptData)?;
        }
        tx.execute_batch(QUERY_SCHEMA).map_err(map_sql)?;
        tx.execute("DELETE FROM task_query", []).map_err(map_sql)?;
        let count=tx.execute("INSERT INTO task_query SELECT t.workspace_id,t.project_id,t.task_id,t.current_version,v.body_digest FROM tasks t JOIN task_versions v USING(workspace_id,project_id,task_id) WHERE t.current_version=v.version",[]).map_err(|e|deadline.map(e))?;
        deadline.check()?;
        if tx.commit().is_err() {
            self.uncertain = true;
            return Err(StoreError::UnknownCommitOutcome);
        }
        Ok(count)
    }
    fn reconcile_create(
        &self,
        command: &CreateTask,
        budget: OperationBudget,
    ) -> Result<CommandObservation, StoreError> {
        command.validate()?;
        self.scope(&command.scope)?;
        let deadline = Deadline::new(budget)?;
        deadline.check()?;
        let connection = connect(&self.path, true)?;
        deadline.configure(&connection)?;
        let snapshot = connection.unchecked_transaction().map_err(map_sql)?;
        verify_identity(&snapshot, &self.identity)?;
        match receipt(&snapshot, &command.scope, &command.operation_key)? {
            None => Ok(CommandObservation::Absent),
            Some(stored) => {
                if stored.input_digest != input_digest(command) {
                    return Err(StoreError::OperationConflict);
                }
                verify_receipt(&snapshot, &stored)?;
                Ok(CommandObservation::Committed(stored))
            }
        }
    }
}
fn connect(path: &Path, read_only: bool) -> Result<Connection, StoreError> {
    if !path.is_absolute() {
        return Err(StoreError::InvalidInput("database_path"));
    }
    let flags = if read_only {
        OpenFlags::SQLITE_OPEN_READ_ONLY
    } else {
        OpenFlags::SQLITE_OPEN_READ_WRITE
    };
    let connection = Connection::open_with_flags(
        path,
        flags | OpenFlags::SQLITE_OPEN_NO_MUTEX | OpenFlags::SQLITE_OPEN_NOFOLLOW,
    )
    .map_err(map_sql)?;
    connection
        .busy_timeout(Duration::from_secs(1))
        .map_err(map_sql)?;
    Ok(connection)
}
fn configure(connection: &Connection, creating: bool) -> Result<(), StoreError> {
    if creating {
        let mode: String = connection
            .query_row("PRAGMA journal_mode=WAL", [], |r| r.get(0))
            .map_err(map_sql)?;
        if mode != "wal" {
            return Err(StoreError::ConfigurationMismatch);
        }
    }
    connection
        .pragma_update(None, "synchronous", "FULL")
        .map_err(map_sql)?;
    connection
        .pragma_update(None, "foreign_keys", "ON")
        .map_err(map_sql)?;
    connection
        .pragma_update(None, "trusted_schema", "OFF")
        .map_err(map_sql)?;
    let mode: String = connection
        .query_row("PRAGMA journal_mode", [], |r| r.get(0))
        .map_err(map_sql)?;
    let sync: i64 = connection
        .query_row("PRAGMA synchronous", [], |r| r.get(0))
        .map_err(map_sql)?;
    let foreign: i64 = connection
        .query_row("PRAGMA foreign_keys", [], |r| r.get(0))
        .map_err(map_sql)?;
    if mode != "wal" || sync != 2 || foreign != 1 {
        return Err(StoreError::ConfigurationMismatch);
    }
    Ok(())
}
fn verify_identity(connection: &Connection, identity: &StoreIdentity) -> Result<(), StoreError> {
    let app: i64 = connection
        .query_row("PRAGMA application_id", [], |r| r.get(0))
        .map_err(map_sql)?;
    let version: i64 = connection
        .query_row("PRAGMA user_version", [], |r| r.get(0))
        .map_err(map_sql)?;
    if app != APPLICATION_ID || version != SCHEMA_VERSION {
        return Err(StoreError::UnsupportedSchema);
    }
    let header: (String, String) = connection
        .query_row(
            "SELECT authority_id,format FROM store_identity WHERE singleton=1",
            [],
            |r| Ok((r.get(0)?, r.get(1)?)),
        )
        .map_err(|_| StoreError::CorruptData)?;
    if header.0 != identity.authority_id {
        return Err(StoreError::IdentityMismatch);
    }
    if header.1 != FORMAT {
        return Err(StoreError::UnsupportedSchema);
    }
    Ok(())
}
// macOS temp paths commonly traverse /var or /tmp symlinks. Resolve the
// selected parent, while leaving the final file protected by NOFOLLOW.
fn normalized_path(path: &Path) -> Result<PathBuf, StoreError> {
    if !path.is_absolute() {
        return Err(StoreError::InvalidInput("database_path"));
    }
    let name = path
        .file_name()
        .ok_or(StoreError::InvalidInput("database_path"))?;
    let parent = path
        .parent()
        .ok_or(StoreError::InvalidInput("database_path"))?
        .canonicalize()
        .map_err(|_| StoreError::Storage(StorageFailure::CannotOpen))?;
    Ok(parent.join(name))
}
fn reserve_file(path: &Path) -> Result<(), StoreError> {
    if !path.is_absolute() {
        return Err(StoreError::InvalidInput("database_path"));
    }
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600).custom_flags(libc::O_NOFOLLOW);
    }
    options.open(path).map_err(|e| {
        if e.kind() == std::io::ErrorKind::AlreadyExists {
            StoreError::DestinationExists
        } else {
            StoreError::Storage(StorageFailure::CannotOpen)
        }
    })?;
    Ok(())
}
pub(crate) fn hash_bytes(bytes: &[u8]) -> ContentDigest {
    ContentDigest(Sha256::digest(bytes).into())
}
fn input_digest(command: &CreateTask) -> ContentDigest {
    let mut hasher = Sha256::new();
    hasher.update(b"dacli/CreateTask/input/v1\0");
    for field in [
        command.scope.authority_id.as_bytes(),
        command.scope.workspace_id.as_bytes(),
        command.scope.project_id.as_bytes(),
        CREATE_TASK_ACTION.as_bytes(),
        command.task_id.as_bytes(),
        command.title.as_bytes(),
        command.body.as_slice(),
    ] {
        hasher.update((field.len() as u64).to_be_bytes());
        hasher.update(field);
    }
    hasher.update(command.expected_version.to_be_bytes());
    let mut refs = command.artifact_references.clone();
    refs.sort();
    hasher.update((refs.len() as u64).to_be_bytes());
    for digest in refs {
        hasher.update(digest.0);
    }
    ContentDigest(hasher.finalize().into())
}
fn pack_refs(refs: &[ContentDigest]) -> Vec<u8> {
    refs.iter().flat_map(|d| d.0).collect()
}
fn unpack_refs(bytes: Vec<u8>) -> Result<Vec<ContentDigest>, StoreError> {
    if !bytes.len().is_multiple_of(32) || bytes.len() > MAXIMUM_ARTIFACT_REFERENCES * 32 {
        return Err(StoreError::CorruptData);
    }
    let refs = bytes
        .chunks_exact(32)
        .map(|s| ContentDigest(s.try_into().unwrap()))
        .collect::<Vec<_>>();
    if refs.windows(2).any(|p| p[0] >= p[1]) {
        return Err(StoreError::CorruptData);
    }
    Ok(refs)
}
fn decode_digest(bytes: Vec<u8>) -> Result<ContentDigest, StoreError> {
    Ok(ContentDigest(
        bytes.try_into().map_err(|_| StoreError::CorruptData)?,
    ))
}
type ReceiptRow = (String, u64, u64, Vec<u8>, Vec<u8>);
fn receipt_row(row: &rusqlite::Row<'_>) -> rusqlite::Result<ReceiptRow> {
    Ok((
        row.get(0)?,
        row.get(1)?,
        row.get(2)?,
        row.get(3)?,
        row.get(4)?,
    ))
}
fn receipt(
    connection: &Connection,
    scope: &TaskScope,
    key: &str,
) -> Result<Option<CommandReceipt>, StoreError> {
    let row=connection.query_row("SELECT task_id,version,event_sequence,input_digest,body_digest FROM operation_results WHERE workspace_id=?1 AND project_id=?2 AND action=?3 AND operation_key=?4",params![scope.workspace_id,scope.project_id,CREATE_TASK_ACTION,key],receipt_row).optional().map_err(map_sql)?;
    row.map(|(task_id, version, event_sequence, input, body)| {
        Ok(CommandReceipt {
            reference: TaskReference {
                scope: scope.clone(),
                task_id,
            },
            operation_key: key.into(),
            version,
            event_sequence,
            input_digest: decode_digest(input)?,
            body_digest: decode_digest(body)?,
        })
    })
    .transpose()
}
fn read_task(
    connection: &Connection,
    reference: &TaskReference,
    expected: Option<u64>,
) -> Result<Option<TaskRecord>, StoreError> {
    let version=connection.query_row("SELECT current_version FROM tasks WHERE workspace_id=?1 AND project_id=?2 AND task_id=?3",params![reference.scope.workspace_id,reference.scope.project_id,reference.task_id],|r|r.get::<_,u64>(0)).optional().map_err(map_sql)?;
    let Some(version) = version else {
        return Ok(None);
    };
    if let Some(expected) = expected {
        if version != expected {
            return Err(StoreError::StaleVersion {
                expected,
                actual: version,
            });
        }
    }
    let (title,body,digest,refs,event)=connection.query_row("SELECT title,body,body_digest,artifact_references,event_sequence FROM task_versions WHERE workspace_id=?1 AND project_id=?2 AND task_id=?3 AND version=?4",params![reference.scope.workspace_id,reference.scope.project_id,reference.task_id,version],|r|Ok((r.get::<_,String>(0)?,r.get::<_,Vec<u8>>(1)?,r.get::<_,Vec<u8>>(2)?,r.get::<_,Vec<u8>>(3)?,r.get::<_,u64>(4)?))).map_err(|_|StoreError::CorruptData)?;
    let digest = decode_digest(digest)?;
    if hash_bytes(&body) != digest {
        return Err(StoreError::CorruptData);
    }
    let record = TaskRecord {
        reference: reference.clone(),
        version,
        title,
        body,
        body_digest: digest,
        artifact_references: unpack_refs(refs)?,
        event_sequence: event,
    };
    let audit=connection.query_row("SELECT operation_key,input_digest,body_digest FROM audit_events WHERE sequence=?1 AND workspace_id=?2 AND project_id=?3 AND task_id=?4 AND version=?5 AND action=?6",params![event,reference.scope.workspace_id,reference.scope.project_id,reference.task_id,version,CREATE_TASK_ACTION],|r|Ok((r.get::<_,String>(0)?,r.get::<_,Vec<u8>>(1)?,r.get::<_,Vec<u8>>(2)?))).map_err(|_|StoreError::CorruptData)?;
    let original = CreateTask {
        scope: reference.scope.clone(),
        operation_key: audit.0,
        task_id: reference.task_id.clone(),
        expected_version: 0,
        title: record.title.clone(),
        body: record.body.clone(),
        artifact_references: record.artifact_references.clone(),
    };
    if version != 1
        || decode_digest(audit.1)? != input_digest(&original)
        || decode_digest(audit.2)? != digest
    {
        return Err(StoreError::CorruptData);
    }
    Ok(Some(record))
}
fn verify_receipt(connection: &Connection, value: &CommandReceipt) -> Result<(), StoreError> {
    let task = read_task(connection, &value.reference, Some(value.version))?
        .ok_or(StoreError::CorruptData)?;
    if task.body_digest != value.body_digest || task.event_sequence != value.event_sequence {
        return Err(StoreError::CorruptData);
    }
    let matches:bool=connection.query_row("SELECT EXISTS(SELECT 1 FROM audit_events WHERE sequence=?1 AND workspace_id=?2 AND project_id=?3 AND action=?4 AND operation_key=?5 AND task_id=?6 AND version=?7 AND input_digest=?8 AND body_digest=?9)",params![value.event_sequence,value.reference.scope.workspace_id,value.reference.scope.project_id,CREATE_TASK_ACTION,value.operation_key,value.reference.task_id,value.version,value.input_digest.0.as_slice(),value.body_digest.0.as_slice()],|r|r.get(0)).map_err(map_sql)?;
    if !matches {
        return Err(StoreError::CorruptData);
    }
    Ok(())
}
fn map_sql(error: rusqlite::Error) -> StoreError {
    match error {
        rusqlite::Error::SqliteFailure(e, _) => match e.code {
            ErrorCode::DatabaseBusy | ErrorCode::DatabaseLocked => StoreError::Busy,
            ErrorCode::DiskFull => StoreError::Storage(StorageFailure::Full),
            ErrorCode::ReadOnly => StoreError::Storage(StorageFailure::ReadOnly),
            ErrorCode::CannotOpen => StoreError::Storage(StorageFailure::CannotOpen),
            ErrorCode::SystemIoFailure => StoreError::Storage(StorageFailure::Io),
            ErrorCode::DatabaseCorrupt | ErrorCode::NotADatabase => StoreError::CorruptData,
            _ => StoreError::Storage(StorageFailure::Other),
        },
        _ => StoreError::CorruptData,
    }
}

#[cfg(feature = "qualification")]
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum CommitCheckpoint {
    BeforeCommit,
    AfterCommit,
}
#[cfg(feature = "qualification")]
pub struct QualificationPause {
    pub checkpoint: CommitCheckpoint,
    pub ready_file: PathBuf,
}
#[cfg(feature = "qualification")]
impl QualificationPause {
    fn at(&self, point: CommitCheckpoint) -> Result<(), StoreError> {
        if point == self.checkpoint {
            std::fs::write(&self.ready_file, b"checkpoint")
                .map_err(|_| StoreError::Storage(StorageFailure::Io))?;
            loop {
                std::thread::sleep(Duration::from_millis(10));
            }
        }
        Ok(())
    }
}
#[cfg(feature = "qualification")]
impl SqliteFixtureStore {
    pub fn qualification_create(
        &mut self,
        command: &CreateTask,
        budget: OperationBudget,
        pause: &QualificationPause,
    ) -> Result<CreateTaskResult, StoreError> {
        let deadline = Deadline::new(budget)?;
        self.create_inner(command, &deadline, Some(pause))
    }
}

#[cfg(feature = "qualification")]
impl SqliteFixtureStore {
    pub fn qualification_limit_pages(&self) -> Result<(), StoreError> {
        let current: i64 = self
            .connection
            .query_row("PRAGMA page_count", [], |r| r.get(0))
            .map_err(map_sql)?;
        self.connection
            .pragma_update(None, "max_page_count", current + 1)
            .map_err(map_sql)
    }
}

#[cfg(feature = "qualification")]
impl SqliteFixtureStore {
    pub fn qualification_refuse_commit(&mut self) -> Result<(), StoreError> {
        self.connection.commit_hook(Some(|| true)).map_err(map_sql)
    }
}
