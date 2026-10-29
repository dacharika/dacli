//! Pure authorization predicates for the scoped fixture (#1258).
//!
//! All inputs are caller-supplied facts. Success does not authenticate them,
//! establish trusted time, activate catalog availability or execute a command.
//! The selected backend must freshly compare the raw credential and load these
//! canonical rows in the same transaction as replay, reads and writes. Its port
//! accepts raw credentials and commands, never a result from this crate.
//!
//! There is no cached/serialized admission permit: success is only `()`.
#![no_std]
#![forbid(unsafe_code)]

use core_kernel::ActionId;
use gateway_contracts::{
    ActorKind, AuthorityFacts, CommandInput, CredentialEvidence, ExactScope, PolicyRefusal,
    PreparedCommand, RequestedGrant, SessionRecord, TimestampMillis, MAXIMUM_LINEAGE,
};

/// Canonical target and its complete ancestors, immediate parent first.
/// Construction bounds the borrow; `admit` checks coherence and authorization.
/// These public facts cannot prove that a target actually exists in storage.
#[derive(Clone, Copy)]
pub struct TargetLineage<'a> {
    target: &'a SessionRecord,
    ancestors: &'a [SessionRecord],
}
impl<'a> TargetLineage<'a> {
    pub fn new(
        target: &'a SessionRecord,
        ancestors: &'a [SessionRecord],
    ) -> Result<Self, PolicyRefusal> {
        if ancestors.len() > MAXIMUM_LINEAGE {
            return Err(PolicyRefusal::InvalidLineage);
        }
        Ok(Self { target, ancestors })
    }
    pub fn target(self) -> &'a SessionRecord {
        self.target
    }
    pub fn ancestors(self) -> &'a [SessionRecord] {
        self.ancestors
    }
}

/// Additional canonical data is required exactly for revocation. Unknown target
/// data can never produce an apparently complete revocation authorization.
#[derive(Clone, Copy)]
pub enum ResourceFacts<'a> {
    None,
    RevocationTarget(TargetLineage<'a>),
}

/// Evaluate a prepared command against current canonical facts.
///
/// Registry availability deliberately remains a dispatch concern. A positive
/// predicate result does not make any unavailable v2 catalog entry executable.
/// `InspectProtocol` cannot be a PreparedCommand or use this scoped path.
///
/// Caller and all caller ancestors must be active. Revocation target records
/// need structural coherence, but may be expired, revoked or not yet valid:
/// removing access does not require a live target. Replay, no-op behavior and
/// events belong to the atomic backend. Root Owner shape is only
/// a consistency check; the backend establishes actual canonical enrollment.
pub fn admit(
    facts: &AuthorityFacts,
    evidence: &CredentialEvidence,
    command: &PreparedCommand,
    resources: ResourceFacts<'_>,
    now: TimestampMillis,
) -> Result<(), PolicyRefusal> {
    check_caller(facts, evidence, now)?;
    command
        .validate()
        .map_err(|_| PolicyRefusal::ActionUnavailable)?;
    check_scope(facts.session().scope(), command.scope())?;
    require_permission(facts.session(), command.action())?;
    match (command.input(), resources) {
        (CommandInput::DelegateSession { requested, .. }, ResourceFacts::None) => {
            check_attenuation(facts, requested, ActorKind::Agent, now)
        }
        (CommandInput::RevokeSession { target, .. }, ResourceFacts::RevocationTarget(lineage)) => {
            if target != lineage.target.id() {
                return Err(PolicyRefusal::SessionMismatch);
            }
            check_revocation_target(facts, lineage)
        }
        (CommandInput::RevokeSession { .. }, ResourceFacts::None)
        | (_, ResourceFacts::RevocationTarget(_)) => Err(PolicyRefusal::InvalidLineage),
        (_, ResourceFacts::None) => Ok(()),
    }
}

/// Check a requested child against issuing-parent facts.
/// This standalone entry point also requires matching credential facts, an
/// active chain and explicit DelegateSession; it is not a shortcut around
/// `admit`. The fixture only delegates Agent actors.
pub fn attenuate(
    facts: &AuthorityFacts,
    evidence: &CredentialEvidence,
    requested: &RequestedGrant,
    child_kind: ActorKind,
    now: TimestampMillis,
) -> Result<(), PolicyRefusal> {
    check_caller(facts, evidence, now)?;
    require_permission(facts.session(), ActionId::DelegateSession)?;
    check_attenuation(facts, requested, child_kind, now)
}

fn check_caller(
    facts: &AuthorityFacts,
    evidence: &CredentialEvidence,
    now: TimestampMillis,
) -> Result<(), PolicyRefusal> {
    // The trusted adapter produces Matched only after fresh verifier comparison.
    // Public constructors deliberately cannot make that comparison true.
    let matched = match evidence {
        CredentialEvidence::Missing => return Err(PolicyRefusal::EmptyCredential),
        CredentialEvidence::Invalid => return Err(PolicyRefusal::InvalidCredential),
        CredentialEvidence::Matched(matched) => matched,
    };
    let credential = facts.credential();
    if credential.authority_id() != facts.authority_id()
        || facts.session().scope().authority_id() != facts.authority_id().as_str()
    {
        return Err(PolicyRefusal::AuthorityMismatch);
    }
    if matched.credential_id() != credential.credential_id() {
        return Err(PolicyRefusal::InvalidCredential);
    }
    if matched.session_id() != credential.session_id()
        || credential.session_id() != facts.session().id()
    {
        return Err(PolicyRefusal::SessionMismatch);
    }
    if matched.generation() != credential.generation() {
        return Err(PolicyRefusal::GenerationMismatch);
    }
    if facts.actor().id() != facts.session().actor_id() {
        return Err(PolicyRefusal::ActorMismatch);
    }
    if facts.actor().kind() == ActorKind::Owner && facts.session().parent().is_some() {
        return Err(PolicyRefusal::OwnerKindNotDelegable);
    }
    for record in core::iter::once(facts.session()).chain(facts.ancestors()) {
        check_active(record, now)?;
    }
    check_chain(facts.session(), facts.ancestors())
}

fn check_active(record: &SessionRecord, now: TimestampMillis) -> Result<(), PolicyRefusal> {
    if record.state().revoked() {
        return Err(PolicyRefusal::Revoked);
    }
    // Future issuance is incoherent canonical chronology. Use the existing
    // InvalidLineage cause; Expired is reserved for the upper expiry boundary.
    if record.lifetime().issued_at() > now {
        return Err(PolicyRefusal::InvalidLineage);
    }
    if now >= record.lifetime().expires_at() {
        return Err(PolicyRefusal::Expired);
    }
    Ok(())
}

fn check_scope(
    permitted: &ExactScope,
    requested: &store_contracts::TaskScope,
) -> Result<(), PolicyRefusal> {
    if permitted.authority_id() != requested.authority_id {
        return Err(PolicyRefusal::AuthorityMismatch);
    }
    if permitted.as_task_scope() != requested {
        return Err(PolicyRefusal::ScopeDenied);
    }
    Ok(())
}

fn require_permission(record: &SessionRecord, action: ActionId) -> Result<(), PolicyRefusal> {
    let required = action
        .spec()
        .required_permission
        .ok_or(PolicyRefusal::ActionUnavailable)?;
    if !record.grants().contains(required) {
        return Err(PolicyRefusal::MissingPermission);
    }
    Ok(())
}

/// Check complete immediate-parent-first ancestry without asserting liveness.
/// The caller checks liveness separately; revocation intentionally does not.
fn check_chain(leaf: &SessionRecord, ancestors: &[SessionRecord]) -> Result<(), PolicyRefusal> {
    if ancestors.len() > MAXIMUM_LINEAGE {
        return Err(PolicyRefusal::InvalidLineage);
    }
    let mut child = leaf;
    for (index, parent) in ancestors.iter().enumerate() {
        if child.parent() != Some(parent.id()) {
            return Err(PolicyRefusal::InvalidLineage);
        }
        // Independent session and actor identities cannot cycle or rebind an
        // existing ancestor actor in this fixture issuance protocol.
        if parent.id() == leaf.id()
            || ancestors[..index]
                .iter()
                .any(|prior| prior.id() == parent.id())
            || parent.actor_id() == leaf.actor_id()
            || ancestors[..index]
                .iter()
                .any(|prior| prior.actor_id() == parent.actor_id())
        {
            return Err(PolicyRefusal::InvalidLineage);
        }
        check_scope(parent.scope(), child.scope().as_task_scope())?;
        require_permission(parent, ActionId::DelegateSession)?;
        if child.lifetime().issued_at() < parent.lifetime().issued_at()
            || child.lifetime().expires_at() > parent.lifetime().expires_at()
            || child.state().remaining_depth().get() >= parent.state().remaining_depth().get()
            || child
                .grants()
                .as_slice()
                .iter()
                .any(|grant| !parent.grants().contains(grant.action()))
        {
            return Err(PolicyRefusal::DelegationAmplification);
        }
        child = parent;
    }
    if child.parent().is_some() {
        return Err(PolicyRefusal::InvalidLineage);
    }
    Ok(())
}

fn check_attenuation(
    facts: &AuthorityFacts,
    requested: &RequestedGrant,
    child_kind: ActorKind,
    now: TimestampMillis,
) -> Result<(), PolicyRefusal> {
    if child_kind == ActorKind::Owner {
        return Err(PolicyRefusal::OwnerKindNotDelegable);
    }
    let parent = facts.session();
    check_scope(parent.scope(), requested.scope().as_task_scope())?;
    if requested.expires_at() <= now {
        return Err(PolicyRefusal::Expired);
    }
    // Parent lifetime is bounded by the contract; a future child admitted at now
    // with no later expiry cannot exceed that fixture lifetime either.
    if requested.expires_at() > parent.lifetime().expires_at()
        || requested.remaining_depth().get() >= parent.state().remaining_depth().get()
        || requested
            .grants()
            .as_slice()
            .iter()
            .any(|grant| !parent.grants().contains(grant.action()))
    {
        return Err(PolicyRefusal::DelegationAmplification);
    }
    Ok(())
}

fn check_revocation_target(
    facts: &AuthorityFacts,
    lineage: TargetLineage<'_>,
) -> Result<(), PolicyRefusal> {
    check_scope(
        facts.session().scope(),
        lineage.target.scope().as_task_scope(),
    )?;
    check_chain(lineage.target, lineage.ancestors)?;
    let caller_chain = core::iter::once(facts.session()).chain(facts.ancestors());
    for (index, target_record) in core::iter::once(lineage.target)
        .chain(lineage.ancestors)
        .enumerate()
    {
        // Every overlapping canonical row and its complete parent suffix must
        // agree, even for an owner revoking an otherwise unrelated target.
        for (caller_index, caller_record) in caller_chain.clone().enumerate() {
            if target_record.id() == caller_record.id()
                && (target_record != caller_record
                    || lineage.ancestors[index..] != facts.ancestors()[caller_index..])
            {
                return Err(PolicyRefusal::InvalidLineage);
            }
        }
    }
    let self_or_descendant = lineage.target.id() == facts.session().id()
        || lineage
            .ancestors
            .iter()
            .any(|ancestor| ancestor.id() == facts.session().id());
    let root_owner = facts.actor().kind() == ActorKind::Owner
        && facts.session().parent().is_none()
        && facts.ancestors().is_empty();
    if !self_or_descendant && !root_owner {
        return Err(PolicyRefusal::ScopeDenied);
    }
    Ok(())
}
