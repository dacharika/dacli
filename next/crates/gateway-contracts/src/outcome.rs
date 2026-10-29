//! Public records contain no credential material; private delivery is separate.
//! All receipts here are bounded attribution facts, not authenticated decisions.
use crate::{
    ActorId, CredentialId, ExactScope, IssuedSecretMaterial, OperationIdentity, PreparationId,
    PreparedCommand, RawCredential, RevocationEpoch, SessionId, TimestampMillis, ValidationError,
};
use alloc::vec::Vec;
use core_kernel::ActionId;
use store_contracts::{CommandReceipt, ContentDigest, OperationBudget, TaskRecord, TaskSummary};

pub const SCOPED_POLICY_VERSION: u16 = 2;
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AdmissionStamp {
    session_id: SessionId,
    epoch: RevocationEpoch,
}
impl AdmissionStamp {
    pub fn new(session_id: SessionId, epoch: RevocationEpoch) -> Self {
        Self { session_id, epoch }
    }
    pub fn session_id(&self) -> &SessionId {
        &self.session_id
    }
    pub const fn epoch(&self) -> RevocationEpoch {
        self.epoch
    }
    pub const fn policy_version(&self) -> u16 {
        SCOPED_POLICY_VERSION
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ScopedTaskReceipt {
    operation: OperationIdentity,
    admission: AdmissionStamp,
    task: CommandReceipt,
}
impl ScopedTaskReceipt {
    pub fn new(
        operation: OperationIdentity,
        admission: AdmissionStamp,
        task: CommandReceipt,
    ) -> Result<Self, ValidationError> {
        task.reference
            .validate()
            .map_err(|_| ValidationError::Receipt)?;
        if operation.action() != ActionId::CreateTask
            || &task.reference.scope != operation.scope().as_task_scope()
            || task.operation_key != operation.key().as_str()
            || task.version == 0
            || task.version > i64::MAX as u64
            || task.event_sequence == 0
            || task.event_sequence > i64::MAX as u64
        {
            return Err(ValidationError::Receipt);
        }
        Ok(Self {
            operation,
            admission,
            task,
        })
    }
    pub fn operation(&self) -> &OperationIdentity {
        &self.operation
    }
    pub fn admission(&self) -> &AdmissionStamp {
        &self.admission
    }
    /// The v1-shaped record is embedded only with explicit v2 attribution; it is
    /// not interpreted or authenticated as a v2 receipt by itself.
    pub fn task(&self) -> &CommandReceipt {
        &self.task
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AssignedCredential {
    actor_id: ActorId,
    session_id: SessionId,
    credential_id: CredentialId,
    preparation_id: PreparationId,
    expires_at: TimestampMillis,
}
impl AssignedCredential {
    pub fn new(
        actor_id: ActorId,
        session_id: SessionId,
        credential_id: CredentialId,
        preparation_id: PreparationId,
        expires_at: TimestampMillis,
    ) -> Self {
        Self {
            actor_id,
            session_id,
            credential_id,
            preparation_id,
            expires_at,
        }
    }
    pub fn actor_id(&self) -> &ActorId {
        &self.actor_id
    }
    pub fn session_id(&self) -> &SessionId {
        &self.session_id
    }
    pub fn credential_id(&self) -> &CredentialId {
        &self.credential_id
    }
    pub fn preparation_id(&self) -> &PreparationId {
        &self.preparation_id
    }
    pub const fn expires_at(&self) -> TimestampMillis {
        self.expires_at
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ScopedSessionReceipt {
    operation: OperationIdentity,
    admission: AdmissionStamp,
    assigned: AssignedCredential,
    event_sequence: u64,
    input_digest: ContentDigest,
}
impl ScopedSessionReceipt {
    pub fn new(
        operation: OperationIdentity,
        admission: AdmissionStamp,
        assigned: AssignedCredential,
        event_sequence: u64,
        input_digest: ContentDigest,
    ) -> Result<Self, ValidationError> {
        if operation.action() != ActionId::DelegateSession
            || event_sequence == 0
            || event_sequence > i64::MAX as u64
        {
            return Err(ValidationError::Receipt);
        }
        Ok(Self {
            operation,
            admission,
            assigned,
            event_sequence,
            input_digest,
        })
    }
    pub fn operation(&self) -> &OperationIdentity {
        &self.operation
    }
    pub fn admission(&self) -> &AdmissionStamp {
        &self.admission
    }
    pub fn assigned(&self) -> &AssignedCredential {
        &self.assigned
    }
    pub const fn event_sequence(&self) -> u64 {
        self.event_sequence
    }
    pub const fn input_digest(&self) -> ContentDigest {
        self.input_digest
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ScopedRevocationReceipt {
    operation: OperationIdentity,
    admission: AdmissionStamp,
    target: SessionId,
    new_epoch: RevocationEpoch,
    event_sequence: u64,
    input_digest: ContentDigest,
}
impl ScopedRevocationReceipt {
    pub fn new(
        operation: OperationIdentity,
        admission: AdmissionStamp,
        target: SessionId,
        new_epoch: RevocationEpoch,
        event_sequence: u64,
        input_digest: ContentDigest,
    ) -> Result<Self, ValidationError> {
        if operation.action() != ActionId::RevokeSession
            || event_sequence == 0
            || event_sequence > i64::MAX as u64
            || new_epoch != admission.epoch().next()?
        {
            return Err(ValidationError::Receipt);
        }
        Ok(Self {
            operation,
            admission,
            target,
            new_epoch,
            event_sequence,
            input_digest,
        })
    }
    pub fn operation(&self) -> &OperationIdentity {
        &self.operation
    }
    pub fn admission(&self) -> &AdmissionStamp {
        &self.admission
    }
    pub fn target(&self) -> &SessionId {
        &self.target
    }
    pub const fn new_epoch(&self) -> RevocationEpoch {
        self.new_epoch
    }
    pub const fn event_sequence(&self) -> u64 {
        self.event_sequence
    }
    pub const fn input_digest(&self) -> ContentDigest {
        self.input_digest
    }
}

/// Bounds/scope are checked here; hash integrity must still be verified by the
/// canonical adapter. No Debug implementation can accidentally log task bodies.
pub struct TaskReadResult {
    task: TaskRecord,
}
impl TaskReadResult {
    pub fn new(scope: &ExactScope, task: TaskRecord) -> Result<Self, ValidationError> {
        task.reference
            .validate()
            .map_err(|_| ValidationError::Command)?;
        if task.artifact_references.len() > store_contracts::MAXIMUM_ARTIFACT_REFERENCES
            || task.body.len() > store_contracts::MAXIMUM_BODY_BYTES
        {
            return Err(ValidationError::Command);
        }
        let mut refs = task.artifact_references.clone();
        refs.sort();
        if &task.reference.scope != scope.as_task_scope()
            || task.version == 0
            || task.version > i64::MAX as u64
            || task.event_sequence == 0
            || task.event_sequence > i64::MAX as u64
            || task.title.trim().is_empty()
            || task.title.len() > 1024
            || task.title.chars().any(char::is_control)
            || task.body.len() > store_contracts::MAXIMUM_BODY_BYTES
            || refs.len() > store_contracts::MAXIMUM_ARTIFACT_REFERENCES
            || refs.windows(2).any(|pair| pair[0] == pair[1])
        {
            return Err(ValidationError::Command);
        }
        Ok(Self { task })
    }
    pub fn record(&self) -> &TaskRecord {
        &self.task
    }
}
pub struct TaskQueryResult {
    rows: Vec<TaskSummary>,
}
impl TaskQueryResult {
    pub fn new(scope: &ExactScope, rows: Vec<TaskSummary>) -> Result<Self, ValidationError> {
        if rows.len() > store_contracts::MAXIMUM_QUERY_ROWS {
            return Err(ValidationError::QueryLimit);
        }
        for row in &rows {
            row.reference
                .validate()
                .map_err(|_| ValidationError::Command)?;
            if &row.reference.scope != scope.as_task_scope()
                || row.version == 0
                || row.version > i64::MAX as u64
            {
                return Err(ValidationError::Command);
            }
        }
        if rows
            .windows(2)
            .any(|pair| pair[0].reference.task_id >= pair[1].reference.task_id)
        {
            return Err(ValidationError::Command);
        }
        Ok(Self { rows })
    }
    pub fn rows(&self) -> &[TaskSummary] {
        &self.rows
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum PolicyRefusal {
    EmptyCredential,
    InvalidCredential,
    AuthorityMismatch,
    ScopeDenied,
    ActorMismatch,
    SessionMismatch,
    GenerationMismatch,
    Expired,
    Revoked,
    MissingPermission,
    InvalidLineage,
    DelegationAmplification,
    OwnerKindNotDelegable,
    ActionUnavailable,
    OperationConflict,
    TaskAlreadyExists,
    StaleVersion,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum GatewayFailure {
    Busy,
    Deadline,
    StorageFull,
    StorageIo,
    InvalidSchema,
    IdentityMismatch,
    CorruptAuthority,
    ClockUnavailable,
    EntropyUnavailable,
    UnknownCommitOutcome,
    NeedsReconciliation,
}

/// No Debug/Serialize implementation; transports choose the allowed public
/// fields deliberately. Successful scoped reads may return authorized task data.
pub enum PublicOutcome {
    TaskCreated {
        receipt: ScopedTaskReceipt,
        replayed: bool,
    },
    TaskObserved {
        receipt: ScopedTaskReceipt,
    },
    OperationAbsent,
    TaskRead(TaskReadResult),
    TaskQuery(TaskQueryResult),
    SessionIssued {
        receipt: ScopedSessionReceipt,
        replayed: bool,
    },
    SessionRevoked {
        receipt: ScopedRevocationReceipt,
        replayed: bool,
    },
    CommittedDeliveryUnavailable {
        receipt: ScopedSessionReceipt,
    },
    Refused(PolicyRefusal),
    Failed(GatewayFailure),
    NotFound,
}
impl PublicOutcome {
    pub const fn exit_code(&self) -> u8 {
        match self {
            Self::Refused(_) => 3,
            Self::Failed(_) | Self::CommittedDeliveryUnavailable { .. } => 1,
            Self::NotFound => 4,
            _ => 0,
        }
    }
}

/// A trusted backend must compare retained material to canonical verifier/IDs
/// before construction. This wrapper bounds handoff, not cryptographic matching.
pub struct PrivateCredentialDelivery<S: IssuedSecretMaterial> {
    assigned: AssignedCredential,
    material: S,
}
impl<S: IssuedSecretMaterial> PrivateCredentialDelivery<S> {
    pub fn assigned(&self) -> &AssignedCredential {
        &self.assigned
    }
    pub fn material(&self) -> &S {
        &self.material
    }
    pub fn into_material(self) -> S {
        self.material
    }
}
pub struct GatewayOutcome<S: IssuedSecretMaterial> {
    public: PublicOutcome,
    delivery: Option<PrivateCredentialDelivery<S>>,
}
impl<S: IssuedSecretMaterial> GatewayOutcome<S> {
    pub fn public_only(public: PublicOutcome) -> Result<Self, ValidationError> {
        if matches!(public, PublicOutcome::SessionIssued { .. }) {
            return Err(ValidationError::Receipt);
        }
        Ok(Self {
            public,
            delivery: None,
        })
    }
    pub fn issued(
        receipt: ScopedSessionReceipt,
        replayed: bool,
        material: S,
    ) -> Result<Self, ValidationError> {
        material.borrow_secret()?;
        let assigned = receipt.assigned().clone();
        Ok(Self {
            public: PublicOutcome::SessionIssued { receipt, replayed },
            delivery: Some(PrivateCredentialDelivery { assigned, material }),
        })
    }
    pub fn delivery_unavailable(receipt: ScopedSessionReceipt) -> Self {
        Self {
            public: PublicOutcome::CommittedDeliveryUnavailable { receipt },
            delivery: None,
        }
    }
    pub fn public(&self) -> &PublicOutcome {
        &self.public
    }
    pub fn into_parts(self) -> (PublicOutcome, Option<PrivateCredentialDelivery<S>>) {
        (self.public, self.delivery)
    }
}

/// Trusted composition port, not an OS/authentication boundary. The selected
/// adapter must revalidate command syntax and current canonical credential,
/// scope, grants, ancestry, expiry and revocation in the SAME transaction as
/// replay/data access/mutation. No supplied facts or policy decisions are inputs.
/// No default implementation or authenticated write is supplied by this crate.
pub trait AtomicScopedBackend {
    type IssuedSecret: IssuedSecretMaterial;
    fn execute_scoped(
        &mut self,
        command: &PreparedCommand,
        credential: RawCredential<'_>,
        budget: OperationBudget,
    ) -> GatewayOutcome<Self::IssuedSecret>;
}
