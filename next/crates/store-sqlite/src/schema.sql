CREATE TABLE store_identity (singleton INTEGER PRIMARY KEY CHECK(singleton=1), authority_id TEXT NOT NULL, format TEXT NOT NULL);
CREATE TABLE audit_events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 workspace_id TEXT NOT NULL, project_id TEXT NOT NULL, action TEXT NOT NULL,
 operation_key TEXT NOT NULL, task_id TEXT NOT NULL, version INTEGER NOT NULL CHECK(version>0),
 input_digest BLOB NOT NULL CHECK(length(input_digest)=32), body_digest BLOB NOT NULL CHECK(length(body_digest)=32),
 UNIQUE(workspace_id,project_id,action,operation_key)
);
CREATE TABLE task_versions (
 workspace_id TEXT NOT NULL, project_id TEXT NOT NULL, task_id TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version>0), title TEXT NOT NULL, body BLOB NOT NULL,
 body_digest BLOB NOT NULL CHECK(length(body_digest)=32), artifact_references BLOB NOT NULL,
 event_sequence INTEGER NOT NULL UNIQUE REFERENCES audit_events(sequence),
 PRIMARY KEY(workspace_id,project_id,task_id,version)
);
CREATE TABLE tasks (
 workspace_id TEXT NOT NULL, project_id TEXT NOT NULL, task_id TEXT NOT NULL,
 current_version INTEGER NOT NULL CHECK(current_version>0),
 PRIMARY KEY(workspace_id,project_id,task_id),
 FOREIGN KEY(workspace_id,project_id,task_id,current_version) REFERENCES task_versions(workspace_id,project_id,task_id,version)
);
CREATE TABLE operation_results (
 workspace_id TEXT NOT NULL, project_id TEXT NOT NULL, action TEXT NOT NULL, operation_key TEXT NOT NULL,
 task_id TEXT NOT NULL, version INTEGER NOT NULL,
 input_digest BLOB NOT NULL CHECK(length(input_digest)=32), body_digest BLOB NOT NULL CHECK(length(body_digest)=32),
 event_sequence INTEGER NOT NULL UNIQUE REFERENCES audit_events(sequence),
 PRIMARY KEY(workspace_id,project_id,action,operation_key),
 FOREIGN KEY(workspace_id,project_id,task_id,version) REFERENCES task_versions(workspace_id,project_id,task_id,version)
);
CREATE TABLE task_query (workspace_id TEXT NOT NULL, project_id TEXT NOT NULL, task_id TEXT NOT NULL, version INTEGER NOT NULL, body_digest BLOB NOT NULL, PRIMARY KEY(workspace_id,project_id,task_id));
CREATE TRIGGER immutable_events_update BEFORE UPDATE ON audit_events BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER immutable_events_delete BEFORE DELETE ON audit_events BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER immutable_versions_update BEFORE UPDATE ON task_versions BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER immutable_versions_delete BEFORE DELETE ON task_versions BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER immutable_results_update BEFORE UPDATE ON operation_results BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER immutable_results_delete BEFORE DELETE ON operation_results BEGIN SELECT RAISE(ABORT,'immutable'); END;
