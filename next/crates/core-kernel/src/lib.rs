//! One pure decoder/policy/dispatch path, replacing per-handler guard convention
//! rather than reproducing it (#1256). There are no host or storage ports yet.
#![no_std]
#![forbid(unsafe_code)]

extern crate alloc;
use core_types::{
    ActionSpec, Authentication, Availability, CredentialContext, Effect, Outcome,
    ProtocolDescription, Refusal, Request, PROTOCOL_VERSION,
};

#[derive(Clone, Copy)]
pub(crate) enum Implementation {
    Query(fn() -> Outcome),
    Unavailable,
}
mod registry;
pub use registry::{
    resolve_scoped, scoped_action_specs, ActionId, PayloadKind, RegistryError, ScopedActionSpec,
    SCOPED_PROTOCOL_VERSION,
};

pub fn action_specs() -> impl ExactSizeIterator<Item = &'static ActionSpec> {
    registry::LegacySpecs::new()
}

/// The request cannot invoke a handler directly or turn a claimed read effect
/// into authority. All outcomes use this path, including catalog inspection.
pub fn execute(request: Request<'_>) -> Outcome {
    let result = decode_and_admit(request);
    match result {
        Err(refusal) => Outcome::Refused(refusal),
        Ok(registered) => match registered.implementation {
            Implementation::Query(handler) => handler(),
            Implementation::Unavailable => Outcome::Refused(Refusal::ActionUnavailable),
        },
    }
}

fn decode_and_admit(request: Request<'_>) -> Result<&'static registry::RegisteredAction, Refusal> {
    if request.protocol_version != PROTOCOL_VERSION {
        return Err(Refusal::UnsupportedProtocol);
    }
    let registered = registry::REGISTRY
        .iter()
        .find(|registered| {
            registered
                .legacy
                .as_ref()
                .is_some_and(|spec| spec.name == request.action)
        })
        .ok_or(Refusal::UnregisteredAction)?;
    let effect = Effect::decode(request.effect).ok_or(Refusal::UnknownEffect)?;
    let spec = registered
        .legacy
        .as_ref()
        .ok_or(Refusal::UnregisteredAction)?;
    if effect != spec.effect {
        return Err(Refusal::EffectMismatch);
    }
    admit_context(spec, request.context)?;
    Ok(registered)
}

fn admit_context(spec: &ActionSpec, context: CredentialContext<'_>) -> Result<(), Refusal> {
    match context {
        CredentialContext::Missing => Err(Refusal::EmptyCredential),
        CredentialContext::Credential(token) if token.trim().is_empty() => {
            Err(Refusal::EmptyCredential)
        }
        // Never confuse a nonempty bearer string with a verified identity. The
        // real credential adapter and scoped gateway belong to issue #1258.
        CredentialContext::Credential(_) => Err(Refusal::CredentialVerificationUnavailable),
        CredentialContext::PublicProtocol => {
            if spec.authentication == Authentication::PublicProtocol
                && spec.effect == Effect::ReadOnly
                && spec.availability == Availability::Available
            {
                Ok(())
            } else {
                Err(Refusal::PublicContextNotAllowed)
            }
        }
    }
}

fn inspect_protocol() -> Outcome {
    Outcome::Protocol(ProtocolDescription {
        protocol_version: PROTOCOL_VERSION,
        actions: action_specs().copied().collect(),
    })
}
