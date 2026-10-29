//! Qualification executable, compiled only with the qualification feature.
//! Inputs are synthetic fixture data; it supplies no production authority.
use std::{
    path::{Path, PathBuf},
    process::ExitCode,
};
use store_contracts::*;
use store_sqlite::{CommitCheckpoint, QualificationPause, SqliteFixtureStore};
fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.as_slice() == ["version"] {
        println!(
            "SQLite {} ({}); rusqlite qualification executable",
            rusqlite::version(),
            rusqlite::version_number()
        );
        return ExitCode::SUCCESS;
    }

    if args.len() < 5 {
        return ExitCode::from(2);
    }
    let mode = &args[0];
    let path = Path::new(&args[1]);
    let command = CreateTask {
        scope: TaskScope {
            authority_id: "fixture-authority".into(),
            workspace_id: "fixture-workspace".into(),
            project_id: "fixture-project".into(),
        },
        operation_key: args[2].clone(),
        task_id: args[3].clone(),
        expected_version: 0,
        title: "Fixture task".into(),
        body: args[4].as_bytes().to_vec(),
        artifact_references: Vec::new(),
    };
    if (mode == "race" || mode == "batch") && args.len() == 7 {
        if std::fs::write(&args[5], b"ready").is_err() {
            return ExitCode::from(1);
        }
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(20);
        while !Path::new(&args[6]).exists() {
            if std::time::Instant::now() >= deadline {
                return ExitCode::from(1);
            }
            std::thread::sleep(std::time::Duration::from_millis(5));
        }
    }
    if mode == "batch" {
        let mut store = match SqliteFixtureStore::open_existing(
            path,
            StoreIdentity {
                authority_id: "fixture-authority".into(),
            },
        ) {
            Ok(store) => store,
            Err(_) => return ExitCode::from(1),
        };
        for index in 0..128 {
            let mut item = command.clone();
            item.operation_key = format!("batch-key-{index}");
            item.task_id = format!("batch-task-{index}");
            if store
                .create_task(&item, OperationBudget::default())
                .is_err()
            {
                return ExitCode::from(1);
            }
            std::thread::sleep(std::time::Duration::from_micros(200));
        }
        println!("batch-complete");
        return ExitCode::SUCCESS;
    }
    let result = SqliteFixtureStore::open_existing(
        path,
        StoreIdentity {
            authority_id: "fixture-authority".into(),
        },
    )
    .and_then(|mut store| match mode.as_str() {
        "create" | "race" => store.create_task(&command, OperationBudget::default()),
        "before" | "after" if args.len() == 6 => store.qualification_create(
            &command,
            OperationBudget::default(),
            &QualificationPause {
                checkpoint: if mode == "before" {
                    CommitCheckpoint::BeforeCommit
                } else {
                    CommitCheckpoint::AfterCommit
                },
                ready_file: PathBuf::from(&args[5]),
            },
        ),
        _ => Err(StoreError::InvalidInput("fixture_mode")),
    });
    match result {
        Ok(result) => {
            let r = result.receipt;
            println!(
                "ok|{}|{}|{}|{}|{}|{}",
                if result.replayed {
                    "replayed"
                } else {
                    "created"
                },
                r.reference.task_id,
                r.version,
                r.event_sequence,
                r.input_digest.hex(),
                r.body_digest.hex()
            );
            ExitCode::SUCCESS
        }
        Err(error) => {
            println!("err|{error:?}");
            ExitCode::from(3)
        }
    }
}
