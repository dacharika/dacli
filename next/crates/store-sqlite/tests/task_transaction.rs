use rusqlite::Connection;
use sha2::{Digest, Sha256};
use std::{
    fs,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};
use store_contracts::*;
use store_sqlite::{verify_artifact_files, FixtureCounts, SqliteFixtureStore};
static NEXT: AtomicU64 = AtomicU64::new(0);
struct Fixture {
    root: PathBuf,
    path: PathBuf,
}
impl Fixture {
    fn new() -> Self {
        let root = std::env::temp_dir().join(format!(
            "dacli-store-{}-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&root).unwrap();
        let path = root.join("authority.sqlite");
        Self { root, path }
    }
    fn store(&self) -> SqliteFixtureStore {
        SqliteFixtureStore::create_new(&self.path, identity()).unwrap()
    }
    fn reopen(&self) -> SqliteFixtureStore {
        SqliteFixtureStore::open_existing(&self.path, identity()).unwrap()
    }
}
impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}
fn identity() -> StoreIdentity {
    StoreIdentity {
        authority_id: "fixture-authority".into(),
    }
}
fn scope() -> TaskScope {
    TaskScope {
        authority_id: "fixture-authority".into(),
        workspace_id: "fixture-workspace".into(),
        project_id: "fixture-project".into(),
    }
}
fn command(key: &str, id: &str, body: &[u8]) -> CreateTask {
    CreateTask {
        scope: scope(),
        operation_key: key.into(),
        task_id: id.into(),
        expected_version: 0,
        title: "Fixture task".into(),
        body: body.to_vec(),
        artifact_references: Vec::new(),
    }
}
fn counts(n: u64) -> FixtureCounts {
    FixtureCounts {
        tasks: n,
        versions: n,
        events: n,
        results: n,
    }
}
struct Worker(Child);
impl Drop for Worker {
    fn drop(&mut self) {
        if self.0.try_wait().ok().flatten().is_none() {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }
}
fn worker(
    fixture: &Fixture,
    mode: &str,
    key: &str,
    id: &str,
    body: &str,
    extra: &[&Path],
) -> Worker {
    let mut command = Command::new(env!("CARGO_BIN_EXE_store-sqlite-fixture"));
    command
        .arg(mode)
        .arg(&fixture.path)
        .arg(key)
        .arg(id)
        .arg(body);
    for path in extra {
        command.arg(path);
    }
    Worker(
        command
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap(),
    )
}
fn wait_file(path: &Path, child: &mut Worker) {
    let end = Instant::now() + Duration::from_secs(20);
    while !path.exists() {
        assert!(
            child.0.try_wait().unwrap().is_none(),
            "fixture child exited before checkpoint"
        );
        assert!(Instant::now() < end, "checkpoint was not observed");
        std::thread::sleep(Duration::from_millis(5));
    }
}
fn output(mut child: Worker) -> (i32, String) {
    let mut stdout = String::new();
    use std::io::Read;
    child
        .0
        .stdout
        .take()
        .unwrap()
        .read_to_string(&mut stdout)
        .unwrap();
    let status = child.0.wait().unwrap();
    let mut stderr = String::new();
    child
        .0
        .stderr
        .take()
        .unwrap()
        .read_to_string(&mut stderr)
        .unwrap();
    assert!(stderr.is_empty(), "unexpected child diagnostics: {stderr}");
    (status.code().unwrap(), stdout)
}

#[test]
fn two_independent_processes_replay_one_atomic_command_and_changed_input_refuses() {
    let fixture = Fixture::new();
    drop(fixture.store());
    let first_ready = fixture.root.join("first-ready");
    let second_ready = fixture.root.join("second-ready");
    let gate = fixture.root.join("gate");
    let mut first = worker(
        &fixture,
        "race",
        "shared-key",
        "task-1",
        "same-body",
        &[&first_ready, &gate],
    );
    let mut second = worker(
        &fixture,
        "race",
        "shared-key",
        "task-1",
        "same-body",
        &[&second_ready, &gate],
    );
    wait_file(&first_ready, &mut first);
    wait_file(&second_ready, &mut second);
    fs::write(&gate, b"go").unwrap();
    let (status1, one) = output(first);
    let (status2, two) = output(second);
    assert_eq!((status1, status2), (0, 0));
    let first: Vec<_> = one.trim().split('|').collect();
    let second: Vec<_> = two.trim().split('|').collect();
    assert_eq!(&first[2..], &second[2..]);
    assert_ne!(first[1], second[1]);
    let (status, conflict) = output(worker(
        &fixture,
        "create",
        "shared-key",
        "task-1",
        "changed-body",
        &[],
    ));
    assert_eq!(status, 3);
    assert_eq!(conflict.trim(), "err|OperationConflict");
    let store = fixture.reopen();
    assert_eq!(store.counts().unwrap(), counts(1));
    let record = store
        .read_task(
            &TaskReference {
                scope: scope(),
                task_id: "task-1".into(),
            },
            Some(1),
        )
        .unwrap()
        .unwrap();
    assert_eq!(record.body, b"same-body");
    let event = store.audit_event(&scope(), "shared-key").unwrap().unwrap();
    assert_eq!(event.receipt.event_sequence, record.event_sequence);
    assert_eq!(event.receipt.body_digest, record.body_digest);
}

#[test]
fn kill_before_and_after_commit_recovers_all_rows_or_none_and_reuses_the_same_key() {
    for (mode, expected) in [("before", 0), ("after", 1)] {
        let fixture = Fixture::new();
        drop(fixture.store());
        let ready = fixture.root.join("checkpoint");
        let mut child = worker(
            &fixture,
            mode,
            "kill-key",
            "killed-task",
            "durable-body",
            &[&ready],
        );
        wait_file(&ready, &mut child);
        assert_eq!(fixture.reopen().counts().unwrap(), counts(expected));
        child.0.kill().unwrap();
        let status = child.0.wait().unwrap();
        #[cfg(unix)]
        {
            use std::os::unix::process::ExitStatusExt;
            assert_eq!(status.signal(), Some(9));
        }
        let mut store = fixture.reopen();
        assert_eq!(store.counts().unwrap(), counts(expected));
        let cmd = command("kill-key", "killed-task", b"durable-body");
        let observation = store
            .reconcile_create(&cmd, OperationBudget::default())
            .unwrap();
        assert_eq!(
            matches!(observation, CommandObservation::Committed(_)),
            expected == 1
        );
        let result = store.create_task(&cmd, OperationBudget::default()).unwrap();
        assert_eq!(result.replayed, expected == 1);
        assert_eq!(store.counts().unwrap(), counts(1));
        assert_eq!(
            store
                .audit_event(&scope(), "kill-key")
                .unwrap()
                .unwrap()
                .receipt,
            result.receipt
        );
    }
}

#[test]
fn stable_id_version_reads_and_rebuilt_query_match_canonical_records_without_replaying_events() {
    let fixture = Fixture::new();
    let mut store = fixture.store();
    let mut receipts = Vec::new();
    for id in ["z-task", "a-task", "middle-task"] {
        receipts.push(
            store
                .create_task(&command(id, id, id.as_bytes()), OperationBudget::default())
                .unwrap()
                .receipt,
        );
    }
    let before = store.query_tasks(&scope(), 100).unwrap();
    assert_eq!(
        before
            .iter()
            .map(|s| s.reference.task_id.as_str())
            .collect::<Vec<_>>(),
        ["a-task", "middle-task", "z-task"]
    );
    assert_eq!(
        store.read_task(&receipts[0].reference, Some(2)),
        Err(StoreError::StaleVersion {
            expected: 2,
            actual: 1
        })
    );
    Connection::open(&fixture.path)
        .unwrap()
        .execute("DROP TABLE task_query", [])
        .unwrap();
    assert_eq!(
        store
            .rebuild_query_index(OperationBudget::default())
            .unwrap(),
        3
    );
    assert_eq!(store.query_tasks(&scope(), 100).unwrap(), before);
    assert_eq!(store.counts().unwrap(), counts(3));
    for receipt in receipts {
        assert_eq!(
            store
                .audit_event(&scope(), &receipt.operation_key)
                .unwrap()
                .unwrap()
                .receipt,
            receipt
        );
        let row = store
            .read_task(&receipt.reference, Some(1))
            .unwrap()
            .unwrap();
        assert_eq!(
            row.body_digest,
            ContentDigest(Sha256::digest(&row.body).into())
        );
    }
}

#[test]
fn busy_expired_budget_and_real_sqlite_full_do_not_acknowledge_or_leave_partial_rows() {
    let fixture = Fixture::new();
    let mut store = fixture.store();
    let mut lock = Connection::open(&fixture.path).unwrap();
    let tx = lock
        .transaction_with_behavior(rusqlite::TransactionBehavior::Immediate)
        .unwrap();
    assert_eq!(
        store.create_task(
            &command("busy", "task", b"body"),
            OperationBudget {
                deadline_millis: 1000,
                busy_wait_millis: 10
            }
        ),
        Err(StoreError::Busy)
    );
    assert_eq!(store.counts().unwrap(), counts(0));
    drop(tx);
    assert_eq!(
        store.create_task(
            &command("expired", "task", b"body"),
            OperationBudget {
                deadline_millis: 0,
                busy_wait_millis: 0
            }
        ),
        Err(StoreError::DeadlineExceeded)
    );
    assert_eq!(store.counts().unwrap(), counts(0));
    store.qualification_limit_pages().unwrap();
    assert_eq!(
        store.create_task(
            &command("full", "task", &vec![b'x'; 200_000]),
            OperationBudget::default()
        ),
        Err(StoreError::Storage(StorageFailure::Full))
    );
    assert_eq!(store.counts().unwrap(), counts(0));
    drop(store);
    assert_eq!(fixture.reopen().counts().unwrap(), counts(0));
    let unavailable = fixture.root.join("missing-parent").join("db");
    assert!(matches!(
        SqliteFixtureStore::create_new(&unavailable, identity()),
        Err(StoreError::Storage(StorageFailure::CannotOpen))
    ));
    assert!(!unavailable.exists());
}

#[test]
fn uncertain_commit_never_acknowledges_blocks_further_writes_and_reconciles_on_fresh_connection() {
    let fixture = Fixture::new();
    let mut store = fixture.store();
    store.qualification_refuse_commit().unwrap();
    let cmd = command("uncertain", "task", b"body");
    assert_eq!(
        store.create_task(&cmd, OperationBudget::default()),
        Err(StoreError::UnknownCommitOutcome)
    );
    assert_eq!(
        store.create_task(&cmd, OperationBudget::default()),
        Err(StoreError::NeedsReconciliation)
    );
    assert_eq!(
        store
            .reconcile_create(&cmd, OperationBudget::default())
            .unwrap(),
        CommandObservation::Absent
    );
    assert_eq!(fixture.reopen().counts().unwrap(), counts(0));
}

#[test]
fn consistent_online_backup_includes_wal_and_reports_real_missing_or_corrupt_artifact_files() {
    let fixture = Fixture::new();
    let mut store = fixture.store();
    let artifacts = fixture.root.join("artifacts");
    fs::create_dir(&artifacts).unwrap();
    let content = b"actual local artifact";
    let digest = ContentDigest(Sha256::digest(content).into());
    let blob = artifacts.join(digest.hex());
    fs::write(&blob, content).unwrap();
    let mut cmd = command("backup-key", "task", b"task-body");
    cmd.artifact_references.push(digest);
    let receipt = store
        .create_task(&cmd, OperationBudget::default())
        .unwrap()
        .receipt;
    let record = store
        .read_task(&receipt.reference, Some(1))
        .unwrap()
        .unwrap();
    assert!(verify_artifact_files(&record, &artifacts).is_empty());
    assert!(
        fs::metadata(fixture.path.with_extension("sqlite-wal"))
            .unwrap()
            .len()
            > 0
    );
    let target = fixture.root.join("backup.sqlite");
    store
        .backup_to(&target, OperationBudget::default())
        .unwrap();
    let backup = SqliteFixtureStore::open_existing(&target, identity()).unwrap();
    assert_eq!(backup.counts().unwrap(), counts(1));
    assert_eq!(
        backup
            .read_task(&receipt.reference, Some(1))
            .unwrap()
            .unwrap(),
        record
    );
    assert_eq!(
        backup
            .audit_event(&scope(), "backup-key")
            .unwrap()
            .unwrap()
            .receipt,
        receipt
    );
    fs::remove_file(&blob).unwrap();
    assert_eq!(
        verify_artifact_files(&record, &artifacts),
        vec![ArtifactProblem {
            digest,
            state: ArtifactState::Missing
        }]
    );
    fs::write(&blob, b"changed").unwrap();
    assert_eq!(
        verify_artifact_files(&record, &artifacts),
        vec![ArtifactProblem {
            digest,
            state: ArtifactState::Corrupt
        }]
    );
    #[cfg(unix)]
    {
        fs::remove_file(&blob).unwrap();
        let outside = fixture.root.join("private-canary");
        fs::write(&outside, content).unwrap();
        std::os::unix::fs::symlink(outside, &blob).unwrap();
        assert_eq!(
            verify_artifact_files(&record, &artifacts),
            vec![ArtifactProblem {
                digest,
                state: ArtifactState::Unreadable
            }]
        );
    }
    let sentinel = fixture.root.join("existing");
    fs::write(&sentinel, b"retain").unwrap();
    assert_eq!(
        store.backup_to(&sentinel, OperationBudget::default()),
        Err(StoreError::DestinationExists)
    );
    assert_eq!(fs::read(&sentinel).unwrap(), b"retain");
    assert_eq!(
        store.backup_to(&fixture.path, OperationBudget::default()),
        Err(StoreError::DestinationExists)
    );
}

#[test]
fn identity_schema_and_input_digest_preconditions_fail_closed_without_repair() {
    let fixture = Fixture::new();
    let mut store = fixture.store();
    let cmd = command("key", "task", b"body");
    let original = store
        .create_task(&cmd, OperationBudget::default())
        .unwrap()
        .receipt;
    assert!(matches!(
        SqliteFixtureStore::open_existing(
            &fixture.path,
            StoreIdentity {
                authority_id: "foreign".into()
            }
        ),
        Err(StoreError::IdentityMismatch)
    ));
    assert!(matches!(
        SqliteFixtureStore::create_new(&fixture.path, identity()),
        Err(StoreError::DestinationExists)
    ));
    for changed in [
        CreateTask {
            title: "Changed title".into(),
            ..cmd.clone()
        },
        CreateTask {
            task_id: "different-id".into(),
            ..cmd.clone()
        },
        CreateTask {
            artifact_references: vec![ContentDigest([2; 32])],
            ..cmd.clone()
        },
    ] {
        assert_eq!(
            store.create_task(&changed, OperationBudget::default()),
            Err(StoreError::OperationConflict)
        );
    }
    assert_eq!(
        store.create_task(
            &CreateTask {
                operation_key: "another".into(),
                ..cmd.clone()
            },
            OperationBudget::default()
        ),
        Err(StoreError::TaskAlreadyExists)
    );
    assert_eq!(
        store.create_task(
            &CreateTask {
                expected_version: 1,
                ..cmd.clone()
            },
            OperationBudget::default()
        ),
        Err(StoreError::InvalidInput("expected_version"))
    );
    let mut other = cmd.clone();
    other.scope.project_id = "other-project".into();
    let other_result = store
        .create_task(&other, OperationBudget::default())
        .unwrap();
    assert_ne!(other_result.receipt.input_digest, original.input_digest);
    assert_eq!(store.counts().unwrap(), counts(2));
    let connection = Connection::open(&fixture.path).unwrap();
    connection.pragma_update(None, "user_version", 99).unwrap();
    drop(connection);
    assert!(matches!(
        SqliteFixtureStore::open_existing(&fixture.path, identity()),
        Err(StoreError::UnsupportedSchema)
    ));
    let raw = Connection::open(&fixture.path).unwrap();
    assert_eq!(
        raw.query_row::<i64, _, _>("PRAGMA user_version", [], |r| r.get(0))
            .unwrap(),
        99
    );
}

#[test]
fn immutable_audit_and_versions_reject_edits_and_corrupt_body_is_not_silently_rebuilt() {
    let fixture = Fixture::new();
    let mut store = fixture.store();
    let receipt = store
        .create_task(&command("key", "task", b"body"), OperationBudget::default())
        .unwrap()
        .receipt;
    let raw = Connection::open(&fixture.path).unwrap();
    assert!(raw
        .execute("UPDATE audit_events SET task_id='changed'", [])
        .is_err());
    assert!(raw.execute("DELETE FROM operation_results", []).is_err());
    assert!(raw
        .execute("UPDATE task_versions SET body=x'00'", [])
        .is_err());
    raw.execute_batch(
        "DROP TRIGGER immutable_versions_update; UPDATE task_versions SET body=x'00';",
    )
    .unwrap();
    assert_eq!(
        store.read_task(&receipt.reference, Some(1)),
        Err(StoreError::CorruptData)
    );
    assert_eq!(
        store.rebuild_query_index(OperationBudget::default()),
        Err(StoreError::CorruptData)
    );
    assert_eq!(store.counts().unwrap(), counts(1));
    assert_eq!(
        raw.query_row::<Vec<u8>, _, _>("SELECT body FROM task_versions", [], |r| r.get(0))
            .unwrap(),
        vec![0]
    );
}

#[test]
fn failed_qualification_marker_after_commit_reconciles_committed_data_without_false_rollback() {
    let fixture = Fixture::new();
    let mut store = fixture.store();
    let cmd = command("marker-failure", "task", b"body");
    let pause = store_sqlite::QualificationPause {
        checkpoint: store_sqlite::CommitCheckpoint::AfterCommit,
        ready_file: fixture.root.join("missing").join("marker"),
    };
    assert_eq!(
        store.qualification_create(&cmd, OperationBudget::default(), &pause),
        Err(StoreError::UnknownCommitOutcome)
    );
    assert_eq!(
        store.create_task(&cmd, OperationBudget::default()),
        Err(StoreError::NeedsReconciliation)
    );
    assert!(matches!(
        store
            .reconcile_create(&cmd, OperationBudget::default())
            .unwrap(),
        CommandObservation::Committed(_)
    ));
    let mut fresh = fixture.reopen();
    assert_eq!(fresh.counts().unwrap(), counts(1));
    assert!(
        fresh
            .create_task(&cmd, OperationBudget::default())
            .unwrap()
            .replayed
    );
}

#[test]
fn held_handle_rechecks_schema_and_authority_before_replay_or_new_write() {
    for authority_change in [false, true] {
        let fixture = Fixture::new();
        let mut store = fixture.store();
        let cmd = command("original", "task", b"body");
        store.create_task(&cmd, OperationBudget::default()).unwrap();
        let raw = Connection::open(&fixture.path).unwrap();
        if authority_change {
            raw.execute("UPDATE store_identity SET authority_id='new-authority'", [])
                .unwrap();
        } else {
            raw.pragma_update(None, "user_version", 99).unwrap();
        }
        let expected = if authority_change {
            StoreError::IdentityMismatch
        } else {
            StoreError::UnsupportedSchema
        };
        assert_eq!(
            store.create_task(&cmd, OperationBudget::default()),
            Err(expected)
        );
        assert_eq!(
            store.create_task(
                &command("next", "another", b"next"),
                OperationBudget::default()
            ),
            Err(expected)
        );
        assert_eq!(
            store.rebuild_query_index(OperationBudget::default()),
            Err(expected)
        );
        assert_eq!(
            store.read_task(
                &TaskReference {
                    scope: scope(),
                    task_id: "task".into()
                },
                Some(1)
            ),
            Err(expected)
        );
        assert_eq!(store.counts(), Err(expected));
        let actual:(i64,i64,i64,i64)=raw.query_row("SELECT (SELECT COUNT(*) FROM tasks),(SELECT COUNT(*) FROM task_versions),(SELECT COUNT(*) FROM audit_events),(SELECT COUNT(*) FROM operation_results)",[],|r|Ok((r.get(0)?,r.get(1)?,r.get(2)?,r.get(3)?))).unwrap();
        assert_eq!(actual, (1, 1, 1, 1));
    }
}
#[test]
fn counts_observed_during_an_independent_writer_are_one_snapshot() {
    let fixture = Fixture::new();
    let store = fixture.store();
    let ready = fixture.root.join("writer-ready");
    let gate = fixture.root.join("writer-gate");
    let mut child = worker(
        &fixture,
        "batch",
        "unused",
        "unused",
        "body",
        &[&ready, &gate],
    );
    wait_file(&ready, &mut child);
    assert_eq!(store.counts().unwrap(), counts(0));
    fs::write(&gate, b"go").unwrap();
    let deadline = Instant::now() + Duration::from_secs(20);
    let mut intermediate = false;
    let mut observations = 0;
    loop {
        let observed = store.counts().unwrap();
        assert_eq!(
            observed,
            counts(observed.tasks),
            "one command cannot appear half-recorded to a reader"
        );
        intermediate |= observed.tasks > 0 && observed.tasks < 128;
        observations += 1;
        if child.0.try_wait().unwrap().is_some() {
            break;
        }
        assert!(Instant::now() < deadline, "writer did not finish");
    }
    assert!(
        intermediate && observations > 1,
        "actual writer/read observation overlap is required"
    );
    assert_eq!(output(child).0, 0);
    assert_eq!(store.counts().unwrap(), counts(128));
}
