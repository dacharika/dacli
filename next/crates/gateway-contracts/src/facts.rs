//! Bounded canonical-fact data. Constructors do not admit or authenticate actors.
use crate::CredentialRecord;
use crate::{
    ActorId, AuthorityId, DelegationDepth, ExactScope, RevocationEpoch, SessionId, SessionLifetime,
    TimestampMillis, ValidationError,
};
use alloc::vec::Vec;
use core_kernel::ActionId;
use core_types::Effect;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct Grant {
    action: ActionId,
}
impl Grant {
    pub fn new(action: ActionId, claimed_effect: Effect) -> Result<Self, ValidationError> {
        let spec = action.spec();
        if spec.required_permission.is_none() {
            return Err(ValidationError::GrantPublic);
        }
        if claimed_effect != spec.effect {
            return Err(ValidationError::GrantEffect);
        }
        Ok(Self { action })
    }
    pub const fn action(self) -> ActionId {
        self.action
    }
    pub fn effect(self) -> Effect {
        self.action.spec().effect
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GrantSet(Vec<Grant>);
impl GrantSet {
    pub fn new(mut grants: Vec<Grant>) -> Result<Self, ValidationError> {
        if grants.is_empty() {
            return Err(ValidationError::GrantEmpty);
        }
        if grants.len() > crate::MAXIMUM_GRANTS {
            return Err(ValidationError::GrantCount);
        }
        grants.sort_unstable_by(|left, right| {
            left.action
                .name()
                .as_bytes()
                .cmp(right.action.name().as_bytes())
        });
        if grants
            .windows(2)
            .any(|pair| pair[0].action == pair[1].action)
        {
            return Err(ValidationError::GrantDuplicate);
        }
        Ok(Self(grants))
    }
    pub fn as_slice(&self) -> &[Grant] {
        &self.0
    }
    pub fn contains(&self, action: ActionId) -> bool {
        self.0.iter().any(|grant| grant.action == action)
    }
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ActorKind {
    Owner,
    Agent,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ActorRecord {
    id: ActorId,
    kind: ActorKind,
}
impl ActorRecord {
    pub fn new(id: ActorId, kind: ActorKind) -> Self {
        Self { id, kind }
    }
    pub fn id(&self) -> &ActorId {
        &self.id
    }
    pub const fn kind(&self) -> ActorKind {
        self.kind
    }
}
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct SessionState {
    revoked: bool,
    remaining_depth: DelegationDepth,
}
impl SessionState {
    pub fn new(revoked: bool, remaining_depth: DelegationDepth) -> Self {
        Self {
            revoked,
            remaining_depth,
        }
    }
    pub const fn revoked(self) -> bool {
        self.revoked
    }
    pub const fn remaining_depth(self) -> DelegationDepth {
        self.remaining_depth
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SessionRecord {
    id: SessionId,
    actor_id: ActorId,
    parent: Option<SessionId>,
    scope: ExactScope,
    grants: GrantSet,
    lifetime: SessionLifetime,
    state: SessionState,
}
impl SessionRecord {
    pub fn new(
        id: SessionId,
        actor_id: ActorId,
        parent: Option<SessionId>,
        scope: ExactScope,
        grants: GrantSet,
        lifetime: SessionLifetime,
        state: SessionState,
    ) -> Self {
        Self {
            id,
            actor_id,
            parent,
            scope,
            grants,
            lifetime,
            state,
        }
    }
    pub fn id(&self) -> &SessionId {
        &self.id
    }
    pub fn actor_id(&self) -> &ActorId {
        &self.actor_id
    }
    pub fn parent(&self) -> Option<&SessionId> {
        self.parent.as_ref()
    }
    pub fn scope(&self) -> &ExactScope {
        &self.scope
    }
    pub fn grants(&self) -> &GrantSet {
        &self.grants
    }
    pub const fn lifetime(&self) -> SessionLifetime {
        self.lifetime
    }
    pub const fn state(&self) -> SessionState {
        self.state
    }
}

/// Structural bounds only. Policy must check coherence, lineage, current grants,
/// expiry and revocation against the real canonical transaction, not this name.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorityFacts {
    authority_id: AuthorityId,
    epoch: RevocationEpoch,
    actor: ActorRecord,
    session: SessionRecord,
    credential: CredentialRecord,
    ancestors: Vec<SessionRecord>,
}
impl AuthorityFacts {
    pub fn new(
        authority_id: AuthorityId,
        epoch: RevocationEpoch,
        actor: ActorRecord,
        session: SessionRecord,
        credential: CredentialRecord,
        ancestors: Vec<SessionRecord>,
    ) -> Result<Self, ValidationError> {
        if ancestors.len() > crate::MAXIMUM_LINEAGE {
            return Err(ValidationError::Depth);
        }
        Ok(Self {
            authority_id,
            epoch,
            actor,
            session,
            credential,
            ancestors,
        })
    }
    pub fn authority_id(&self) -> &AuthorityId {
        &self.authority_id
    }
    pub const fn epoch(&self) -> RevocationEpoch {
        self.epoch
    }
    pub fn actor(&self) -> &ActorRecord {
        &self.actor
    }
    pub fn session(&self) -> &SessionRecord {
        &self.session
    }
    pub fn credential(&self) -> &CredentialRecord {
        &self.credential
    }
    pub fn ancestors(&self) -> &[SessionRecord] {
        &self.ancestors
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RequestedGrant {
    scope: ExactScope,
    grants: GrantSet,
    expires_at: TimestampMillis,
    remaining_depth: DelegationDepth,
}
impl RequestedGrant {
    pub fn new(
        scope: ExactScope,
        grants: GrantSet,
        expires_at: TimestampMillis,
        remaining_depth: DelegationDepth,
    ) -> Self {
        Self {
            scope,
            grants,
            expires_at,
            remaining_depth,
        }
    }
    pub fn scope(&self) -> &ExactScope {
        &self.scope
    }
    pub fn grants(&self) -> &GrantSet {
        &self.grants
    }
    pub const fn expires_at(&self) -> TimestampMillis {
        self.expires_at
    }
    pub const fn remaining_depth(&self) -> DelegationDepth {
        self.remaining_depth
    }
}
