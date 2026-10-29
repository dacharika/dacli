//! Opaque names are bounded identifiers, never identity proofs.
use crate::ValidationError;
use alloc::string::String;
use store_contracts::TaskScope;

macro_rules! identifier {
    ($($name:ident),+ $(,)?) => { $(
        #[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd)]
        pub struct $name(String);
        impl $name {
            pub fn new(value: String) -> Result<Self, ValidationError> {
                store_contracts::validate_identity(&value, "identifier")
                    .map_err(|_| ValidationError::Identifier)?;
                Ok(Self(value))
            }
            pub fn as_str(&self) -> &str { &self.0 }
        }
    )+ };
}
identifier!(
    AuthorityId,
    ActorId,
    SessionId,
    CredentialId,
    PreparationId,
    OperationKey
);

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ExactScope(TaskScope);
impl ExactScope {
    pub fn new(scope: TaskScope) -> Result<Self, ValidationError> {
        scope.validate().map_err(|_| ValidationError::Scope)?;
        Ok(Self(scope))
    }
    pub fn as_task_scope(&self) -> &TaskScope {
        &self.0
    }
    pub fn authority_id(&self) -> &str {
        &self.0.authority_id
    }
    pub fn workspace_id(&self) -> &str {
        &self.0.workspace_id
    }
    pub fn project_id(&self) -> &str {
        &self.0.project_id
    }
}

#[derive(Clone, Copy, Debug, Eq, Ord, PartialEq, PartialOrd)]
pub struct TimestampMillis(u64);
impl TimestampMillis {
    pub fn new(value: u64) -> Result<Self, ValidationError> {
        if value == 0 || value > i64::MAX as u64 {
            return Err(ValidationError::Time);
        }
        Ok(Self(value))
    }
    pub const fn get(self) -> u64 {
        self.0
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct RevocationEpoch(u64);
impl RevocationEpoch {
    pub fn new(value: u64) -> Result<Self, ValidationError> {
        if value > i64::MAX as u64 {
            return Err(ValidationError::Epoch);
        }
        Ok(Self(value))
    }
    pub const fn get(self) -> u64 {
        self.0
    }
    pub fn next(self) -> Result<Self, ValidationError> {
        let next = self
            .0
            .checked_add(1)
            .ok_or(ValidationError::CounterOverflow)?;
        Self::new(next).map_err(|_| ValidationError::CounterOverflow)
    }
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct CredentialGeneration(u64);
impl CredentialGeneration {
    pub fn new(value: u64) -> Result<Self, ValidationError> {
        if value == 0 || value > i64::MAX as u64 {
            return Err(ValidationError::Generation);
        }
        Ok(Self(value))
    }
    pub const fn get(self) -> u64 {
        self.0
    }
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct DelegationDepth(u8);
impl DelegationDepth {
    pub fn new(value: u8) -> Result<Self, ValidationError> {
        if usize::from(value) > crate::MAXIMUM_LINEAGE {
            return Err(ValidationError::Depth);
        }
        Ok(Self(value))
    }
    pub const fn get(self) -> u8 {
        self.0
    }
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct SessionLifetime {
    issued_at: TimestampMillis,
    expires_at: TimestampMillis,
}
impl SessionLifetime {
    pub fn new(
        issued_at: TimestampMillis,
        expires_at: TimestampMillis,
    ) -> Result<Self, ValidationError> {
        let elapsed = expires_at
            .get()
            .checked_sub(issued_at.get())
            .ok_or(ValidationError::Lifetime)?;
        if elapsed == 0 || elapsed > crate::MAXIMUM_FIXTURE_LIFETIME_MILLIS {
            return Err(ValidationError::Lifetime);
        }
        Ok(Self {
            issued_at,
            expires_at,
        })
    }
    pub const fn issued_at(self) -> TimestampMillis {
        self.issued_at
    }
    pub const fn expires_at(self) -> TimestampMillis {
        self.expires_at
    }
}
