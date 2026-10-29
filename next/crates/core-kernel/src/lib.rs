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
enum Implementation {
    Query(fn() -> Outcome),
    Unavailable,
}
struct RegisteredAction {
    spec: ActionSpec,
    implementation: Implementation,
}

// This is the only action registration list. The CLI does not keep route names,
// effect metadata or help text separately. An unavailable mutation is visible
// static protocol metadata, never a task write or a credential verifier.
static ACTIONS: [RegisteredAction; 2] = [
    RegisteredAction {
        spec: ActionSpec {
            name: "InspectProtocol",
            summary: "Inspect this static protocol and action catalog.",
            effect: Effect::ReadOnly,
            authentication: Authentication::PublicProtocol,
            availability: Availability::Available,
            request_schema_version: PROTOCOL_VERSION,
        },
        implementation: Implementation::Query(inspect_protocol),
    },
    RegisteredAction {
        spec: ActionSpec {
            name: "CreateTask",
            summary: "Unavailable: production task transport and scoped authentication are not implemented.",
            effect: Effect::Mutation,
            authentication: Authentication::VerifiedCapability("tasks.create"),
            availability: Availability::Unavailable,
            request_schema_version: PROTOCOL_VERSION,
        },
        implementation: Implementation::Unavailable,
    },
];

pub fn action_specs() -> impl ExactSizeIterator<Item = &'static ActionSpec> {
    ACTIONS.iter().map(|registered| &registered.spec)
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

fn decode_and_admit(request: Request<'_>) -> Result<&'static RegisteredAction, Refusal> {
    if request.protocol_version != PROTOCOL_VERSION {
        return Err(Refusal::UnsupportedProtocol);
    }
    let registered = ACTIONS
        .iter()
        .find(|registered| registered.spec.name == request.action)
        .ok_or(Refusal::UnregisteredAction)?;
    let effect = Effect::decode(request.effect).ok_or(Refusal::UnknownEffect)?;
    if effect != registered.spec.effect {
        return Err(Refusal::EffectMismatch);
    }
    admit_context(&registered.spec, request.context)?;
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
