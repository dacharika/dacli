//! Request syntax is privately validated; neither a command nor a grant request
//! contains owner/proof/epoch/time-source claims or public discovery context.
use crate::{ExactScope, OperationKey, RequestedGrant, SessionId, ValidationError};
use core_kernel::{ActionId, PayloadKind};
use core_types::Effect;
use store_contracts::{CreateTask, TaskReference, TaskScope, MAXIMUM_QUERY_ROWS};

/// Untrusted input data. Use PreparedCommand::new before invoking a scoped port.
/// This input deliberately has no Debug implementation (task bodies are private).
pub enum CommandInput {
    CreateTask(CreateTask),
    ReadTask {
        reference: TaskReference,
        expected_version: Option<u64>,
    },
    QueryTasks {
        scope: TaskScope,
        limit: usize,
    },
    ObserveCreateTask(CreateTask),
    DelegateSession {
        operation_key: OperationKey,
        requested: RequestedGrant,
    },
    RevokeSession {
        scope: ExactScope,
        operation_key: OperationKey,
        target: SessionId,
    },
}
impl CommandInput {
    pub fn payload_kind(&self) -> PayloadKind {
        match self {
            Self::CreateTask(_) => PayloadKind::CreateTask,
            Self::ReadTask { .. } => PayloadKind::ReadTask,
            Self::QueryTasks { .. } => PayloadKind::QueryTasks,
            Self::ObserveCreateTask(_) => PayloadKind::ObserveCreateTask,
            Self::DelegateSession { .. } => PayloadKind::DelegateSession,
            Self::RevokeSession { .. } => PayloadKind::RevokeSession,
        }
    }
    pub fn validate(&self) -> Result<(), ValidationError> {
        match self {
            Self::CreateTask(command) | Self::ObserveCreateTask(command) => {
                command.validate().map_err(|_| ValidationError::Command)
            }
            Self::ReadTask {
                reference,
                expected_version,
            } => {
                reference.validate().map_err(|_| ValidationError::Command)?;
                if expected_version.is_some_and(|version| version == 0 || version > i64::MAX as u64)
                {
                    return Err(ValidationError::Version);
                }
                Ok(())
            }
            Self::QueryTasks { scope, limit } => {
                scope.validate().map_err(|_| ValidationError::Scope)?;
                if *limit == 0 || *limit > MAXIMUM_QUERY_ROWS {
                    return Err(ValidationError::QueryLimit);
                }
                Ok(())
            }
            Self::DelegateSession { .. } | Self::RevokeSession { .. } => Ok(()),
        }
    }
    pub fn scope(&self) -> &TaskScope {
        match self {
            Self::CreateTask(command) | Self::ObserveCreateTask(command) => &command.scope,
            Self::ReadTask { reference, .. } => &reference.scope,
            Self::QueryTasks { scope, .. } => scope,
            Self::DelegateSession { requested, .. } => requested.scope().as_task_scope(),
            Self::RevokeSession { scope, .. } => scope.as_task_scope(),
        }
    }
    pub fn operation_key(&self) -> Option<&str> {
        match self {
            Self::CreateTask(command) | Self::ObserveCreateTask(command) => {
                Some(&command.operation_key)
            }
            Self::DelegateSession { operation_key, .. }
            | Self::RevokeSession { operation_key, .. } => Some(operation_key.as_str()),
            Self::ReadTask { .. } | Self::QueryTasks { .. } => None,
        }
    }
}

/// Validated shape only, never authenticated or policy-approved. Private fields
/// prevent a later mutation of legacy public task structs after validation.
pub struct PreparedCommand {
    action: ActionId,
    input: CommandInput,
}
impl PreparedCommand {
    pub fn new(input: CommandInput) -> Result<Self, ValidationError> {
        let action = input.payload_kind().action();
        Self::for_action(action, action.spec().effect, input)
    }
    pub fn for_action(
        action: ActionId,
        claimed_effect: Effect,
        input: CommandInput,
    ) -> Result<Self, ValidationError> {
        input.validate()?;
        let spec = action.spec();
        if spec.required_permission.is_none() || spec.payload != input.payload_kind() {
            return Err(ValidationError::Payload);
        }
        if claimed_effect != spec.effect {
            return Err(ValidationError::GrantEffect);
        }
        Ok(Self { action, input })
    }
    pub const fn action(&self) -> ActionId {
        self.action
    }
    pub fn effect(&self) -> Effect {
        self.action.spec().effect
    }
    pub fn input(&self) -> &CommandInput {
        &self.input
    }
    pub fn scope(&self) -> &TaskScope {
        self.input.scope()
    }
    pub fn operation_key(&self) -> Option<&str> {
        self.input.operation_key()
    }
    pub fn validate(&self) -> Result<(), ValidationError> {
        self.input.validate()?;
        if self.action.spec().payload != self.input.payload_kind()
            || self.action.spec().required_permission.is_none()
        {
            return Err(ValidationError::Payload);
        }
        Ok(())
    }
}

/// A bounded operation namespace is an attribution fact, not permission.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct OperationIdentity {
    scope: ExactScope,
    actor: crate::ActorId,
    action: ActionId,
    key: OperationKey,
}
impl OperationIdentity {
    pub fn new(
        scope: ExactScope,
        actor: crate::ActorId,
        action: ActionId,
        key: OperationKey,
    ) -> Result<Self, ValidationError> {
        if action.spec().effect != Effect::Mutation {
            return Err(ValidationError::Command);
        }
        Ok(Self {
            scope,
            actor,
            action,
            key,
        })
    }
    pub fn scope(&self) -> &ExactScope {
        &self.scope
    }
    pub fn actor(&self) -> &crate::ActorId {
        &self.actor
    }
    pub const fn action(&self) -> ActionId {
        self.action
    }
    pub fn key(&self) -> &OperationKey {
        &self.key
    }
}
