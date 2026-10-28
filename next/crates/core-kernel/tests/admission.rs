use core_kernel::{action_specs, execute};
use core_types::{
    Authentication, Availability, CredentialContext, Effect, Outcome, Refusal, Request,
    PROTOCOL_VERSION,
};

fn inspection(context: CredentialContext<'_>) -> Request<'_> {
    Request {
        protocol_version: PROTOCOL_VERSION,
        action: "InspectProtocol",
        effect: "read_only",
        context,
    }
}

#[test]
fn explicit_public_discovery_returns_only_the_registered_static_catalog() {
    let result = execute(inspection(CredentialContext::PublicProtocol));
    let Outcome::Protocol(description) = result else {
        panic!("explicit public protocol discovery must succeed");
    };
    assert_eq!(description.protocol_version, PROTOCOL_VERSION);
    assert_eq!(
        description.actions,
        action_specs().copied().collect::<Vec<_>>()
    );
    assert!(description.actions.iter().any(|spec| {
        spec.name == "InspectProtocol"
            && spec.effect == Effect::ReadOnly
            && spec.authentication == Authentication::PublicProtocol
            && spec.availability == Availability::Available
    }));
    assert!(description.actions.iter().any(|spec| {
        spec.name == "CreateTask"
            && spec.effect == Effect::Mutation
            && spec.authentication == Authentication::VerifiedCapability("tasks.create")
            && spec.availability == Availability::Unavailable
    }));
}

#[test]
fn claimed_effect_cannot_invoke_a_successful_handler_under_another_classification() {
    let mut request = inspection(CredentialContext::PublicProtocol);
    request.effect = "mutation";
    assert_eq!(execute(request), Outcome::Refused(Refusal::EffectMismatch));
    request.effect = "shell";
    assert_eq!(execute(request), Outcome::Refused(Refusal::UnknownEffect));
}

#[test]
fn missing_empty_and_whitespace_credentials_never_fall_back_to_public_discovery() {
    for context in [
        CredentialContext::Missing,
        CredentialContext::Credential(""),
        CredentialContext::Credential(" \t\n"),
        CredentialContext::Credential("\u{2003}"),
    ] {
        let result = execute(inspection(context));
        assert_eq!(result, Outcome::Refused(Refusal::EmptyCredential));
        assert_eq!(result.exit_code(), 3);
    }
}

#[test]
fn nonempty_tokens_never_become_verified_identity_or_owner_authority() {
    let context = CredentialContext::Credential("not-a-real-credential-CANARY");
    assert_eq!(
        execute(inspection(context)),
        Outcome::Refused(Refusal::CredentialVerificationUnavailable)
    );
    assert_eq!(format!("{context:?}"), "Credential([REDACTED])");
    assert!(!format!("{:?}", inspection(context)).contains("CANARY"));
}

#[test]
fn public_discovery_cannot_admit_the_registered_unavailable_mutation() {
    let request = Request {
        protocol_version: PROTOCOL_VERSION,
        action: "CreateTask",
        effect: "mutation",
        context: CredentialContext::PublicProtocol,
    };
    assert_eq!(
        execute(request),
        Outcome::Refused(Refusal::PublicContextNotAllowed)
    );
    let result = execute(Request {
        context: CredentialContext::Credential(""),
        ..request
    });
    assert_eq!(result, Outcome::Refused(Refusal::EmptyCredential));
    let result = execute(Request {
        context: CredentialContext::Credential("unverified"),
        ..request
    });
    assert_eq!(
        result,
        Outcome::Refused(Refusal::CredentialVerificationUnavailable)
    );
}

#[test]
fn unknown_mutation_and_protocol_version_have_typed_final_refusals() {
    let mut request = inspection(CredentialContext::PublicProtocol);
    request.action = "DeleteEverything";
    request.effect = "mutation";
    assert_eq!(
        execute(request),
        Outcome::Refused(Refusal::UnregisteredAction)
    );
    request.action = "InspectProtocol";
    request.protocol_version += 1;
    assert_eq!(
        execute(request),
        Outcome::Refused(Refusal::UnsupportedProtocol)
    );
}
