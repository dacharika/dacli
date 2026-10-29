//! Checked deterministic v2 framing; hashing/verifier implementations are absent.
//! Private task bytes can be present: this frame deliberately has no Debug.
use crate::{
    AssignedCredential, CommandInput, ExactScope, OperationIdentity, PreparedCommand,
    ScopedRevocationReceipt, ScopedSessionReceipt, ScopedTaskReceipt, SessionId, ValidationError,
};
use alloc::vec::Vec;
use core_kernel::ActionId;

pub const OPERATION_INPUT_DOMAIN_V2: &[u8] = b"dacli/scoped/operation-input/v2\0";
pub const BODY_INPUT_DOMAIN_V2: &[u8] = b"dacli/scoped/task-body/v2\0";
pub const RECEIPT_INPUT_DOMAIN_V2: &[u8] = b"dacli/scoped/receipt/v2\0";
pub const PREPARATION_INPUT_DOMAIN_V2: &[u8] = b"dacli/scoped/preparation/v2\0";

pub struct CanonicalInput(Vec<u8>);
impl CanonicalInput {
    pub fn as_bytes(&self) -> &[u8] {
        &self.0
    }
}
struct Frame(Vec<u8>);
impl Frame {
    fn new(domain: &[u8]) -> Result<Self, ValidationError> {
        let mut frame = Self(Vec::new());
        frame.field(domain)?;
        frame.append(&crate::SCOPED_SCHEMA_VERSION.to_be_bytes())?;
        Ok(frame)
    }
    fn append(&mut self, bytes: &[u8]) -> Result<(), ValidationError> {
        let length = self
            .0
            .len()
            .checked_add(bytes.len())
            .ok_or(ValidationError::FrameSize)?;
        if length > crate::MAXIMUM_REQUEST_BYTES {
            return Err(ValidationError::FrameSize);
        }
        self.0.extend_from_slice(bytes);
        Ok(())
    }
    fn field(&mut self, bytes: &[u8]) -> Result<(), ValidationError> {
        let size = u32::try_from(bytes.len()).map_err(|_| ValidationError::FrameSize)?;
        // Check both prefix and data before either allocation/append.
        let length = self
            .0
            .len()
            .checked_add(4)
            .and_then(|n| n.checked_add(bytes.len()))
            .ok_or(ValidationError::FrameSize)?;
        if length > crate::MAXIMUM_REQUEST_BYTES {
            return Err(ValidationError::FrameSize);
        }
        self.append(&size.to_be_bytes())?;
        self.append(bytes)
    }
    fn number(&mut self, value: u64) -> Result<(), ValidationError> {
        self.append(&value.to_be_bytes())
    }
    fn scope(&mut self, scope: &ExactScope) -> Result<(), ValidationError> {
        self.field(scope.authority_id().as_bytes())?;
        self.field(scope.workspace_id().as_bytes())?;
        self.field(scope.project_id().as_bytes())
    }
    fn operation(&mut self, identity: &OperationIdentity) -> Result<(), ValidationError> {
        self.scope(identity.scope())?;
        self.field(identity.actor().as_str().as_bytes())?;
        self.field(identity.action().name().as_bytes())?;
        self.field(identity.key().as_str().as_bytes())
    }
    fn stamp(&mut self, stamp: &crate::AdmissionStamp) -> Result<(), ValidationError> {
        self.field(stamp.session_id().as_str().as_bytes())?;
        self.number(stamp.epoch().get())?;
        self.append(&stamp.policy_version().to_be_bytes())
    }
    fn finish(self) -> CanonicalInput {
        CanonicalInput(self.0)
    }
}

/// The selected adapter supplies canonical actor/parent facts; this function
/// authenticates none of them and cannot be used as an authorization permit.
/// ObserveCreateTask frames the original CreateTask input, never a new write.
pub fn command_input(
    identity: &OperationIdentity,
    command: &PreparedCommand,
    canonical_parent: Option<&SessionId>,
) -> Result<CanonicalInput, ValidationError> {
    command.validate()?;
    let expected = if command.action() == ActionId::ObserveCreateTask {
        ActionId::CreateTask
    } else {
        command.action()
    };
    if identity.action() != expected
        || identity.scope().as_task_scope() != command.scope()
        || command.operation_key() != Some(identity.key().as_str())
    {
        return Err(ValidationError::Command);
    }
    let is_delegate = command.action() == ActionId::DelegateSession;
    if is_delegate != canonical_parent.is_some() {
        return Err(ValidationError::Command);
    }
    let mut frame = Frame::new(OPERATION_INPUT_DOMAIN_V2)?;
    frame.operation(identity)?;
    match command.input() {
        CommandInput::CreateTask(task) | CommandInput::ObserveCreateTask(task) => {
            frame.field(task.task_id.as_bytes())?;
            frame.field(task.title.as_bytes())?;
            frame.field(&task.body)?;
            frame.number(task.expected_version)?;
            let mut refs = task.artifact_references.clone();
            refs.sort();
            frame.number(u64::try_from(refs.len()).map_err(|_| ValidationError::FrameSize)?)?;
            for digest in refs {
                frame.field(&digest.0)?;
            }
        }
        CommandInput::DelegateSession { requested, .. } => {
            let parent = canonical_parent.ok_or(ValidationError::Command)?;
            frame.field(parent.as_str().as_bytes())?;
            frame.number(
                u64::try_from(requested.grants().as_slice().len())
                    .map_err(|_| ValidationError::FrameSize)?,
            )?;
            for grant in requested.grants().as_slice() {
                frame.field(grant.action().name().as_bytes())?;
                frame.field(grant.effect().name().as_bytes())?;
            }
            frame.number(requested.expires_at().get())?;
            frame.append(&[requested.remaining_depth().get()])?;
        }
        CommandInput::RevokeSession { target, .. } => frame.field(target.as_str().as_bytes())?,
        CommandInput::ReadTask { .. } | CommandInput::QueryTasks { .. } => {
            return Err(ValidationError::Command)
        }
    }
    Ok(frame.finish())
}

pub fn body_input(body: &[u8]) -> Result<CanonicalInput, ValidationError> {
    if body.len() > store_contracts::MAXIMUM_BODY_BYTES {
        return Err(ValidationError::Command);
    }
    let mut frame = Frame::new(BODY_INPUT_DOMAIN_V2)?;
    frame.field(body)?;
    Ok(frame.finish())
}
pub fn task_receipt_input(receipt: &ScopedTaskReceipt) -> Result<CanonicalInput, ValidationError> {
    let mut frame = Frame::new(RECEIPT_INPUT_DOMAIN_V2)?;
    frame.field(b"task")?;
    frame.operation(receipt.operation())?;
    frame.stamp(receipt.admission())?;
    frame.field(receipt.task().reference.task_id.as_bytes())?;
    frame.number(receipt.task().version)?;
    frame.number(receipt.task().event_sequence)?;
    frame.field(&receipt.task().input_digest.0)?;
    frame.field(&receipt.task().body_digest.0)?;
    Ok(frame.finish())
}
pub fn session_receipt_input(
    receipt: &ScopedSessionReceipt,
) -> Result<CanonicalInput, ValidationError> {
    let mut frame = Frame::new(RECEIPT_INPUT_DOMAIN_V2)?;
    frame.field(b"session")?;
    frame.operation(receipt.operation())?;
    frame.stamp(receipt.admission())?;
    assigned(&mut frame, receipt.assigned())?;
    frame.number(receipt.event_sequence())?;
    frame.field(&receipt.input_digest().0)?;
    Ok(frame.finish())
}
pub fn revocation_receipt_input(
    receipt: &ScopedRevocationReceipt,
) -> Result<CanonicalInput, ValidationError> {
    let mut frame = Frame::new(RECEIPT_INPUT_DOMAIN_V2)?;
    frame.field(b"revocation")?;
    frame.operation(receipt.operation())?;
    frame.stamp(receipt.admission())?;
    frame.field(receipt.target().as_str().as_bytes())?;
    frame.number(receipt.new_epoch().get())?;
    frame.number(receipt.event_sequence())?;
    frame.field(&receipt.input_digest().0)?;
    Ok(frame.finish())
}
/// Nonsecret assigned-ID binding only. Matching this does NOT compare a secret
/// to its canonical verifier; the trusted adapter must perform that check.
pub fn preparation_input(
    scope: &ExactScope,
    credential: &AssignedCredential,
) -> Result<CanonicalInput, ValidationError> {
    let mut frame = Frame::new(PREPARATION_INPUT_DOMAIN_V2)?;
    frame.scope(scope)?;
    assigned(&mut frame, credential)?;
    Ok(frame.finish())
}
fn assigned(frame: &mut Frame, credential: &AssignedCredential) -> Result<(), ValidationError> {
    frame.field(credential.actor_id().as_str().as_bytes())?;
    frame.field(credential.session_id().as_str().as_bytes())?;
    frame.field(credential.credential_id().as_str().as_bytes())?;
    frame.field(credential.preparation_id().as_str().as_bytes())?;
    frame.number(credential.expires_at().get())
}
