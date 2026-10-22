package envelopeworker

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/mlnomadpy/dacli/cloud/internal/tenant"
	"github.com/mlnomadpy/dacli/internal/cloudsync"
)

func expectWorkerTx(mock sqlmock.Sqlmock, scope tenant.Scope) {
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config`).WithArgs(scope.Organization).WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestInboxClaimCompleteAndFenceBoundaries(t *testing.T) {
	repository, mock, closeDB := testRepository(t)
	defer closeDB()
	scope, _ := tenant.NewScope("tenant-a")
	_, _, private := testIdentityAndKeys(t)
	envelope := testEnvelope(t, private)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()

	if _, err := repository.ClaimInbox(context.Background(), scope, time.Time{}, time.Time{}, 0); err == nil {
		t.Fatal("invalid inbox claim accepted")
	}
	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`WITH due AS`).WithArgs(scope.Organization, now.UnixMilli(), 10, now.Add(time.Minute).UnixMilli()).WillReturnRows(
		sqlmock.NewRows([]string{"project_id", "event_id", "attempts", "fence"}).AddRow("project-a", "event-a", 2, 7))
	mock.ExpectQuery(`SELECT envelope FROM controlplane_envelope_inbox_payloads`).WithArgs(scope.Organization, "project-a", "event-a").WillReturnRows(
		sqlmock.NewRows([]string{"envelope"}).AddRow(raw))
	mock.ExpectCommit()
	claimed, err := repository.ClaimInbox(context.Background(), scope, now, now.Add(time.Minute), 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 || claimed[0].Fence != 7 || claimed[0].Envelope.EventID != envelope.EventID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}

	stale := claimed[0]
	stale.Fence--
	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`SELECT fence,attempts,state,lease_until_unix_milli`).WithArgs(scope.Organization, stale.Project, stale.EventID).WillReturnRows(
		sqlmock.NewRows([]string{"fence", "attempts", "state", "deadline"}).AddRow(7, 2, "processing", now.Add(time.Hour).UnixMilli()))
	mock.ExpectRollback()
	if err := repository.CompleteInbox(context.Background(), scope, stale, now); err == nil || !strings.Contains(err.Error(), "lease no longer current") {
		t.Fatalf("stale fence completion = %v", err)
	}

	changed := claimed[0]
	changed.Envelope.ProjectID = "project-other"
	if err := repository.CompleteInbox(context.Background(), scope, changed, now); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong project/event route = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInboxCompletionPersistsOnlyBoundPayloadProposal(t *testing.T) {
	repository, mock, closeDB := testRepository(t)
	defer closeDB()
	scope, _ := tenant.NewScope("tenant-a")
	_, _, private := testIdentityAndKeys(t)
	envelope := testEnvelope(t, private)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	row := InboxJob{Project: "project-a", EventID: envelope.EventID, Attempts: 1, Fence: 9, Envelope: envelope}
	now := time.Unix(200, 0).UTC()
	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`SELECT fence,attempts,state,lease_until_unix_milli`).WithArgs(scope.Organization, row.Project, row.EventID).WillReturnRows(
		sqlmock.NewRows([]string{"fence", "attempts", "state", "deadline"}).AddRow(row.Fence, row.Attempts, "processing", now.Add(time.Minute).UnixMilli()))
	mock.ExpectQuery(`SELECT envelope FROM controlplane_envelope_inbox_payloads`).WithArgs(scope.Organization, row.Project, row.EventID).WillReturnRows(
		sqlmock.NewRows([]string{"envelope"}).AddRow(raw))
	mock.ExpectExec(`INSERT INTO controlplane_envelope_proposals`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT envelope_digest FROM controlplane_envelope_proposals`).WillReturnRows(
		sqlmock.NewRows([]string{"envelope_digest"}).AddRow(digest[:]))
	mock.ExpectExec(`UPDATE controlplane_envelope_inbox_jobs SET state='processed'`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repository.CompleteInbox(context.Background(), scope, row, now); err != nil {
		t.Fatalf("complete valid inbox job: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInboxFailureStatusAndAttemptLimit(t *testing.T) {
	repository, mock, closeDB := testRepository(t)
	defer closeDB()
	scope, _ := tenant.NewScope("tenant-a")
	now := time.Unix(300, 0).UTC()
	row := InboxJob{Project: "project-a", EventID: "event-a", Attempts: 3, Fence: 8}
	if err := repository.FailInbox(context.Background(), scope, row, now, 0, "offline"); err == nil {
		t.Fatal("invalid retry ceiling accepted")
	}
	expectWorkerTx(mock, scope)
	mock.ExpectExec(`UPDATE controlplane_envelope_inbox_jobs SET state=\$4`).WithArgs(scope.Organization, row.Project, row.EventID, "dead-letter", now.UnixMilli(), "offline", row.Fence, row.Attempts).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repository.FailInbox(context.Background(), scope, row, now, 3, "offline"); err != nil {
		t.Fatalf("dead-letter exhausted inbox job: %v", err)
	}
	if _, err := repository.InboxStatus(context.Background(), scope, "", 10); !errors.Is(err, ErrDenied) {
		t.Fatalf("empty project status query = %v", err)
	}
	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`SELECT project_id,event_id,state,attempts,fence,last_error_code,next_attempt_unix_milli`).WithArgs(scope.Organization, "project-a", 10).WillReturnRows(
		sqlmock.NewRows([]string{"project", "event", "state", "attempts", "fence", "error", "next"}).AddRow("project-a", "event-a", "dead-letter", 3, 8, "offline", now.UnixMilli()))
	mock.ExpectCommit()
	rows, err := repository.InboxStatus(context.Background(), scope, "project-a", 10)
	if err != nil || len(rows) != 1 || rows[0].State != "dead-letter" || rows[0].LastError != "offline" {
		t.Fatalf("status=%+v err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryScopedDeliveryAttemptLimitFiltersExhaustedWork(t *testing.T) {
	repository, mock, closeDB := testRepository(t)
	defer closeDB()
	if _, err := repository.WithDeliveryAttemptLimit(0); err == nil {
		t.Fatal("zero attempt limit accepted")
	}
	if _, err := repository.WithDeliveryAttemptLimit(101); err == nil {
		t.Fatal("unbounded attempt limit accepted")
	}
	limited, err := repository.WithDeliveryAttemptLimit(3)
	if err != nil || limited.deliveryAttemptLimit != 3 || repository.deliveryAttemptLimit != 0 {
		t.Fatalf("limited repository=%+v original=%+v err=%v", limited, repository, err)
	}
	scope, _ := tenant.NewScope("tenant-a")
	now := time.Unix(400, 0).UTC()
	expectWorkerTx(mock, scope)
	mock.ExpectExec(`UPDATE controlplane_envelope_outbox SET state='dead-letter'`).WithArgs(scope.Organization, 3, now.UnixMilli()).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectQuery(`WITH due AS`).WithArgs(scope.Organization, now.UnixMilli(), 5, now.Add(time.Minute).UnixMilli(), 3).WillReturnRows(
		sqlmock.NewRows([]string{"tenant", "project", "id", "envelope", "attempts"}))
	mock.ExpectCommit()
	rows, err := limited.ClaimDue(context.Background(), scope, now, now.Add(time.Minute), 5)
	if err != nil || len(rows) != 0 {
		t.Fatalf("limited claim rows=%+v err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecordDecisionValidatesAndPersistsAudit(t *testing.T) {
	repository, mock, closeDB := testRepository(t)
	defer closeDB()
	identity, _, _ := testIdentityAndKeys(t)
	invalid := testAudit(identity, "operation-a")
	invalid.EventIdentity = "short"
	expectWorkerTx(mock, identity.Scope)
	mock.ExpectRollback()
	if err := repository.RecordDecision(context.Background(), invalid); err == nil {
		t.Fatal("malformed event identity entered audit log")
	}

	audit := testAudit(identity, "operation-a")
	audit.Decision, audit.Reason = cloudsync.OutcomeTampered, "signature"
	expectWorkerTx(mock, identity.Scope)
	mock.ExpectExec(`INSERT INTO controlplane_envelope_audit_events`).WithArgs(identity.Scope.Organization, audit.CorrelationID, identity.Account, identity.Device, audit.Project, audit.EventIdentity, audit.Decision, audit.Reason, audit.OccurredAt.UnixMilli()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := repository.RecordDecision(context.Background(), audit); err != nil {
		t.Fatalf("persist verified refusal audit: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectInboxClaim(mock sqlmock.Sqlmock, scope tenant.Scope, envelopeRaw []byte, attempts int, fence uint64) {
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config`).WithArgs(scope.Organization).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`WITH due AS`).WithArgs(scope.Organization, sqlmock.AnyArg(), 25, sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"project_id", "event_id", "attempts", "fence"}).AddRow("project-a", "event-a", attempts, fence))
	mock.ExpectQuery(`SELECT envelope FROM controlplane_envelope_inbox_payloads`).WithArgs(scope.Organization, "project-a", "event-a").WillReturnRows(
		sqlmock.NewRows([]string{"envelope"}).AddRow(envelopeRaw))
	mock.ExpectCommit()
}

func expectInboxRetry(mock sqlmock.Sqlmock, scope tenant.Scope, attempts, max int) {
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT set_config`).WithArgs(scope.Organization).WillReturnResult(sqlmock.NewResult(0, 1))
	state := "pending"
	if attempts >= max {
		state = "dead-letter"
	}
	mock.ExpectExec(`UPDATE controlplane_envelope_inbox_jobs SET state=\$4`).WithArgs(scope.Organization, "project-a", "event-a", state, sqlmock.AnyArg(), "inbox_processing_failed", uint64(4), attempts).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestProcessInboxAcceptsOnlyCurrentSignedPayload(t *testing.T) {
	scope, _ := tenant.NewScope("tenant-a")
	_, public, private := testIdentityAndKeys(t)
	envelope := testEnvelope(t, private)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("valid signature persists proposal", func(t *testing.T) {
		repository, mock, closeDB := testRepository(t)
		defer closeDB()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT set_config`).WithArgs(scope.Organization).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE controlplane_envelope_inbox_jobs SET state='dead-letter'`).WithArgs(scope.Organization, 3, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		expectInboxClaim(mock, scope, raw, 1, 4)
		now := time.Now().UTC()
		proposalDigest := sha256.Sum256(raw)
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT set_config`).WithArgs(scope.Organization).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT fence,attempts,state,lease_until_unix_milli`).WithArgs(scope.Organization, "project-a", "event-a").WillReturnRows(
			sqlmock.NewRows([]string{"fence", "attempts", "state", "deadline"}).AddRow(4, 1, "processing", now.Add(time.Hour).UnixMilli()))
		mock.ExpectQuery(`SELECT envelope FROM controlplane_envelope_inbox_payloads`).WithArgs(scope.Organization, "project-a", "event-a").WillReturnRows(
			sqlmock.NewRows([]string{"envelope"}).AddRow(raw))
		mock.ExpectExec(`INSERT INTO controlplane_envelope_proposals`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT envelope_digest FROM controlplane_envelope_proposals`).WillReturnRows(
			sqlmock.NewRows([]string{"envelope_digest"}).AddRow(proposalDigest[:]))
		mock.ExpectExec(`UPDATE controlplane_envelope_inbox_jobs SET state='processed'`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		if err := repository.ProcessInbox(context.Background(), scope, testKeys{keys: map[string]ed25519.PublicKey{"key-a": public}}, 3); err != nil {
			t.Fatalf("process valid signed inbox: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("untrusted key schedules retry", func(t *testing.T) {
		repository, mock, closeDB := testRepository(t)
		defer closeDB()
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT set_config`).WithArgs(scope.Organization).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE controlplane_envelope_inbox_jobs SET state='dead-letter'`).WithArgs(scope.Organization, 3, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
		expectInboxClaim(mock, scope, raw, 1, 4)
		expectInboxRetry(mock, scope, 1, 3)
		if err := repository.ProcessInbox(context.Background(), scope, testKeys{}, 3); err != nil {
			t.Fatalf("record retry for untrusted signing key: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPullSnapshotsPagesAndConfirmsExactProducerIdentity(t *testing.T) {
	repository, mock, closeDB := testRepository(t)
	defer closeDB()
	scope, _ := tenant.NewScope("tenant-a")
	_, _, private := testIdentityAndKeys(t)
	envelope := testEnvelope(t, private)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`SELECT COALESCE\(MAX\(delivery_sequence\),0\)`).WithArgs(scope.Organization, "project-a").WillReturnRows(
		sqlmock.NewRows([]string{"snapshot"}).AddRow(101))
	pageRows := sqlmock.NewRows([]string{"sequence", "envelope"})
	for i := 1; i <= 101; i++ {
		pageRows.AddRow(i, raw)
	}
	mock.ExpectQuery(`SELECT delivery_sequence,envelope FROM controlplane_envelope_outbox`).WithArgs(scope.Organization, "project-a", uint64(0), uint64(101), sqlmock.AnyArg()).WillReturnRows(pageRows)
	mock.ExpectCommit()
	page, err := repository.Pull(context.Background(), scope, "project-a", 0, 0, []string{"key-a"})
	if err != nil || len(page.Envelopes) != 100 || page.After != 100 || page.Snapshot != 101 || !page.More || page.Envelopes[0].EventID != envelope.EventID {
		t.Fatalf("pull page=%+v err=%v", page, err)
	}

	expectWorkerTx(mock, scope)
	mock.ExpectRollback()
	if _, err := repository.Pull(context.Background(), scope, "project-a", 2, 1, []string{"key-a"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("after beyond snapshot = %v", err)
	}

	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM controlplane_envelope_inbox_identities`).WithArgs(scope.Organization, envelope.ProjectID, envelope.EventID, envelope.IdempotencyKey, envelope.Integrity.KeyID, envelope.ProducerSequence).WillReturnRows(
		sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()
	confirmed, err := repository.Confirmed(context.Background(), scope, envelope)
	if err != nil || !confirmed {
		t.Fatalf("confirmed=%t err=%v", confirmed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPullRefusesMismatchedStoredRouteAndOversizedRows(t *testing.T) {
	repository, mock, closeDB := testRepository(t)
	defer closeDB()
	scope, _ := tenant.NewScope("tenant-a")
	_, _, private := testIdentityAndKeys(t)
	envelope := testEnvelope(t, private)
	envelope.ProjectID = "project-other"
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`SELECT delivery_sequence,envelope FROM controlplane_envelope_outbox`).WillReturnRows(
		sqlmock.NewRows([]string{"sequence", "envelope"}).AddRow(1, raw))
	mock.ExpectRollback()
	if _, err := repository.Pull(context.Background(), scope, "project-a", 0, 1, []string{"key-a"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("mismatched stored route = %v", err)
	}

	expectWorkerTx(mock, scope)
	mock.ExpectQuery(`SELECT delivery_sequence,envelope FROM controlplane_envelope_outbox`).WillReturnRows(
		sqlmock.NewRows([]string{"sequence", "envelope"}).AddRow(1, make([]byte, (512<<10)+1)))
	mock.ExpectRollback()
	if _, err := repository.Pull(context.Background(), scope, "project-a", 0, 1, []string{"key-a"}); !errors.Is(err, ErrPullTooLarge) {
		t.Fatalf("oversized stored row = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitErrorIsTyped(t *testing.T) {
	err := RateLimitError{RetryAfter: time.Second}
	if err.Error() != ErrRateLimited.Error() || !errors.Is(err, ErrRateLimited) || err.RetryAfter != time.Second {
		t.Fatalf("rate limit error contract changed: %#v", err)
	}
}
