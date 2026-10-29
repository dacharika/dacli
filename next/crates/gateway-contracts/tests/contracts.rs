use core_kernel::ActionId;
use core_types::Effect;
use gateway_contracts::*;
use store_contracts::{
    CommandReceipt, ContentDigest, CreateTask, OperationBudget, TaskRecord, TaskReference,
    TaskScope, TaskSummary,
};

fn scope() -> TaskScope {
    TaskScope {
        authority_id: "authority".into(),
        workspace_id: "workspace".into(),
        project_id: "project".into(),
    }
}
fn exact() -> ExactScope {
    ExactScope::new(scope()).unwrap()
}
fn actor(name: &str) -> ActorId {
    ActorId::new(name.into()).unwrap()
}
fn session(name: &str) -> SessionId {
    SessionId::new(name.into()).unwrap()
}
fn key(name: &str) -> OperationKey {
    OperationKey::new(name.into()).unwrap()
}
fn time(value: u64) -> TimestampMillis {
    TimestampMillis::new(value).unwrap()
}
fn grants(actions: &[ActionId]) -> GrantSet {
    GrantSet::new(
        actions
            .iter()
            .map(|action| Grant::new(*action, action.spec().effect).unwrap())
            .collect(),
    )
    .unwrap()
}
fn task() -> CreateTask {
    CreateTask {
        scope: scope(),
        operation_key: "operation".into(),
        task_id: "task".into(),
        expected_version: 0,
        title: "private-title".into(),
        body: b"private-body".to_vec(),
        artifact_references: vec![],
    }
}
fn identity(who: &str, action: ActionId) -> OperationIdentity {
    OperationIdentity::new(exact(), actor(who), action, key("operation")).unwrap()
}
fn prepared(task: CreateTask) -> PreparedCommand {
    PreparedCommand::new(CommandInput::CreateTask(task)).unwrap()
}
fn task_frame(task: CreateTask, who: &str) -> Vec<u8> {
    let op = OperationIdentity::new(
        ExactScope::new(task.scope.clone()).unwrap(),
        actor(who),
        ActionId::CreateTask,
        key(&task.operation_key),
    )
    .unwrap();
    command_input(&op, &prepared(task), None)
        .unwrap()
        .as_bytes()
        .to_vec()
}
fn stamp(name: &str, epoch: u64) -> AdmissionStamp {
    AdmissionStamp::new(session(name), RevocationEpoch::new(epoch).unwrap())
}
fn legacy_receipt() -> CommandReceipt {
    CommandReceipt {
        reference: TaskReference {
            scope: scope(),
            task_id: "task".into(),
        },
        operation_key: "operation".into(),
        version: 1,
        event_sequence: 1,
        input_digest: ContentDigest([1; 32]),
        body_digest: ContentDigest([2; 32]),
    }
}
fn task_receipt(who: &str, stamp: AdmissionStamp) -> ScopedTaskReceipt {
    ScopedTaskReceipt::new(identity(who, ActionId::CreateTask), stamp, legacy_receipt()).unwrap()
}
fn assigned(n: &str) -> AssignedCredential {
    AssignedCredential::new(
        actor(n),
        session(n),
        CredentialId::new(n.into()).unwrap(),
        PreparationId::new(n.into()).unwrap(),
        time(1000),
    )
}
fn issued() -> ScopedSessionReceipt {
    ScopedSessionReceipt::new(
        identity("parent", ActionId::DelegateSession),
        stamp("parent-session", 0),
        assigned("child"),
        1,
        ContentDigest([3; 32]),
    )
    .unwrap()
}

#[test]
fn identifiers_scopes_times_and_counters_are_bounded_without_normalizing_identity() {
    for value in ["", "has space", "has\ncontrol"] {
        assert!(ActorId::new(value.into()).is_err());
    }
    assert!(ActorId::new("x".repeat(128)).is_ok());
    assert_eq!(
        ActorId::new("x".repeat(129)),
        Err(ValidationError::Identifier)
    );
    assert!(ActorId::new("🦊".repeat(32)).is_ok());
    assert!(ActorId::new("🦊".repeat(33)).is_err());
    assert_ne!(actor("é"), actor("e\u{301}"));
    assert_ne!(actor("OWNER"), actor("owner"));
    let mut invalid = scope();
    invalid.workspace_id = "bad workspace".into();
    assert!(ExactScope::new(invalid).is_err());
    assert!(TimestampMillis::new(0).is_err());
    assert!(TimestampMillis::new(u64::MAX).is_err());
    assert!(CredentialGeneration::new(0).is_err());
    assert_eq!(
        RevocationEpoch::new(i64::MAX as u64).unwrap().next(),
        Err(ValidationError::CounterOverflow)
    );
    assert!(RevocationEpoch::new(u64::MAX).is_err());
    assert!(DelegationDepth::new(9).is_err());
    assert!(SessionLifetime::new(time(1), time(1)).is_err());
    assert!(SessionLifetime::new(time(1), time(3_600_002)).is_err());
    assert!(SessionLifetime::new(time(1), time(3_600_001)).is_ok());
}

#[test]
fn grants_refuse_missing_duplicates_public_actions_and_forged_effects() {
    assert_eq!(GrantSet::new(vec![]), Err(ValidationError::GrantEmpty));
    let create = Grant::new(ActionId::CreateTask, Effect::Mutation).unwrap();
    assert_eq!(
        GrantSet::new(vec![create, create]),
        Err(ValidationError::GrantDuplicate)
    );
    assert_eq!(
        Grant::new(ActionId::CreateTask, Effect::ReadOnly),
        Err(ValidationError::GrantEffect)
    );
    assert_eq!(
        Grant::new(ActionId::InspectProtocol, Effect::ReadOnly),
        Err(ValidationError::GrantPublic)
    );
    let a = grants(&[ActionId::QueryTasks, ActionId::CreateTask]);
    let b = grants(&[ActionId::CreateTask, ActionId::QueryTasks]);
    assert_eq!(a, b);
    assert!(a.contains(ActionId::CreateTask));
    assert!(!a.contains(ActionId::ReadTask));
    assert_eq!(
        GrantSet::new(vec![create; 7]),
        Err(ValidationError::GrantCount)
    );
}

#[test]
fn prepared_syntax_cannot_forge_effect_payload_public_discovery_or_task_bounds() {
    assert!(matches!(
        PreparedCommand::for_action(
            ActionId::ReadTask,
            Effect::ReadOnly,
            CommandInput::CreateTask(task())
        ),
        Err(ValidationError::Payload)
    ));
    assert!(matches!(
        PreparedCommand::for_action(
            ActionId::InspectProtocol,
            Effect::ReadOnly,
            CommandInput::CreateTask(task())
        ),
        Err(ValidationError::Payload)
    ));
    assert!(matches!(
        PreparedCommand::for_action(
            ActionId::CreateTask,
            Effect::ReadOnly,
            CommandInput::CreateTask(task())
        ),
        Err(ValidationError::GrantEffect)
    ));
    let mut large = task();
    large.body = vec![0; store_contracts::MAXIMUM_BODY_BYTES + 1];
    assert!(PreparedCommand::new(CommandInput::CreateTask(large)).is_err());
    let mut repeated = task();
    repeated.artifact_references = vec![ContentDigest([1; 32]); 2];
    assert!(PreparedCommand::new(CommandInput::CreateTask(repeated)).is_err());
    for limit in [0, 1025] {
        assert!(PreparedCommand::new(CommandInput::QueryTasks {
            scope: scope(),
            limit
        })
        .is_err());
    }
    assert!(PreparedCommand::new(CommandInput::ReadTask {
        reference: legacy_receipt().reference,
        expected_version: Some(0)
    })
    .is_err());
    assert!(
        OperationIdentity::new(exact(), actor("reader"), ActionId::ReadTask, key("key")).is_err()
    );
}

#[test]
fn canonical_task_framing_binds_actor_scope_key_and_each_semantic_field() {
    assert_eq!(
        OPERATION_INPUT_DOMAIN_V2,
        b"dacli/scoped/operation-input/v2\0"
    );
    assert_eq!(BODY_INPUT_DOMAIN_V2, b"dacli/scoped/task-body/v2\0");
    assert_eq!(RECEIPT_INPUT_DOMAIN_V2, b"dacli/scoped/receipt/v2\0");
    assert_eq!(
        PREPARATION_INPUT_DOMAIN_V2,
        b"dacli/scoped/preparation/v2\0"
    );
    let original = task_frame(task(), "actor");
    assert_ne!(original, task_frame(task(), "other-actor"));
    let mut changed = task();
    changed.scope.authority_id = "other".into();
    assert_ne!(original, task_frame(changed, "actor"));
    let mut changed = task();
    changed.scope.workspace_id = "other".into();
    assert_ne!(original, task_frame(changed, "actor"));
    let mut changed = task();
    changed.scope.project_id = "other".into();
    assert_ne!(original, task_frame(changed, "actor"));
    let mut changed = task();
    changed.operation_key = "other-key".into();
    assert_ne!(original, task_frame(changed, "actor"));
    let mut changed = task();
    changed.task_id = "other-task".into();
    assert_ne!(original, task_frame(changed, "actor"));
    let mut changed = task();
    changed.title = "other-title".into();
    assert_ne!(original, task_frame(changed, "actor"));
    let mut changed = task();
    changed.body = b"other-body".to_vec();
    assert_ne!(original, task_frame(changed, "actor"));
    let mut changed = task();
    changed.artifact_references = vec![ContentDigest([9; 32])];
    assert_ne!(original, task_frame(changed, "actor"));
    let mut a = task();
    a.title = "ab".into();
    a.body = b"c".to_vec();
    let mut b = task();
    b.title = "a".into();
    b.body = b"bc".to_vec();
    assert_ne!(task_frame(a, "actor"), task_frame(b, "actor"));
    let mut a = task();
    a.artifact_references = vec![ContentDigest([1; 32]), ContentDigest([2; 32])];
    let mut b = a.clone();
    b.artifact_references.reverse();
    assert_eq!(task_frame(a, "actor"), task_frame(b, "actor"));
    let mut prefix = (OPERATION_INPUT_DOMAIN_V2.len() as u32)
        .to_be_bytes()
        .to_vec();
    prefix.extend(OPERATION_INPUT_DOMAIN_V2);
    prefix.extend(2u16.to_be_bytes());
    assert!(original.starts_with(&prefix));
    assert_ne!(body_input(b"same").unwrap().as_bytes(), b"same");
    assert!(body_input(&vec![0; store_contracts::MAXIMUM_BODY_BYTES + 1]).is_err());
    let mismatch =
        OperationIdentity::new(exact(), actor("actor"), ActionId::CreateTask, key("other"))
            .unwrap();
    assert!(command_input(&mismatch, &prepared(task()), None).is_err());
}

#[test]
fn observation_frames_only_original_create_and_delegation_requires_canonical_parent() {
    let create = prepared(task());
    let observed = PreparedCommand::new(CommandInput::ObserveCreateTask(task())).unwrap();
    let op = identity("actor", ActionId::CreateTask);
    assert_eq!(
        command_input(&op, &create, None).unwrap().as_bytes(),
        command_input(&op, &observed, None).unwrap().as_bytes()
    );
    assert!(command_input(&op, &create, Some(&session("claimed-parent"))).is_err());
    let make = |expiry, depth| {
        PreparedCommand::new(CommandInput::DelegateSession {
            operation_key: key("operation"),
            requested: RequestedGrant::new(
                exact(),
                grants(&[ActionId::ReadTask]),
                time(expiry),
                DelegationDepth::new(depth).unwrap(),
            ),
        })
        .unwrap()
    };
    let delegate = make(1000, 1);
    let op = identity("actor", ActionId::DelegateSession);
    assert!(command_input(&op, &delegate, None).is_err());
    let a = command_input(&op, &delegate, Some(&session("parent-a"))).unwrap();
    assert_ne!(
        a.as_bytes(),
        command_input(&op, &delegate, Some(&session("parent-b")))
            .unwrap()
            .as_bytes()
    );
    assert_ne!(
        a.as_bytes(),
        command_input(&op, &make(1001, 1), Some(&session("parent-a")))
            .unwrap()
            .as_bytes()
    );
    assert_ne!(
        a.as_bytes(),
        command_input(&op, &make(1000, 2), Some(&session("parent-a")))
            .unwrap()
            .as_bytes()
    );
    assert_ne!(
        preparation_input(&exact(), &assigned("one"))
            .unwrap()
            .as_bytes(),
        preparation_input(&exact(), &assigned("two"))
            .unwrap()
            .as_bytes()
    );
}

#[test]
fn v2_receipts_require_explicit_actor_attribution_and_bind_admission_metadata() {
    let receipt = task_receipt("actor", stamp("session", 0));
    let bytes = task_receipt_input(&receipt).unwrap();
    assert_ne!(
        bytes.as_bytes(),
        task_receipt_input(&task_receipt("another", stamp("session", 0)))
            .unwrap()
            .as_bytes()
    );
    assert_ne!(
        bytes.as_bytes(),
        task_receipt_input(&task_receipt("actor", stamp("another", 0)))
            .unwrap()
            .as_bytes()
    );
    assert_ne!(
        bytes.as_bytes(),
        task_receipt_input(&task_receipt("actor", stamp("session", 1)))
            .unwrap()
            .as_bytes()
    );
    let mut bad = legacy_receipt();
    bad.operation_key = "wrong".into();
    assert!(ScopedTaskReceipt::new(
        identity("actor", ActionId::CreateTask),
        stamp("session", 0),
        bad
    )
    .is_err());
    let mut bad = legacy_receipt();
    bad.reference.scope.project_id = "wrong".into();
    assert!(ScopedTaskReceipt::new(
        identity("actor", ActionId::CreateTask),
        stamp("session", 0),
        bad
    )
    .is_err());
    let mut bad = legacy_receipt();
    bad.version = 0;
    assert!(ScopedTaskReceipt::new(
        identity("actor", ActionId::CreateTask),
        stamp("session", 0),
        bad
    )
    .is_err());
    assert!(ScopedTaskReceipt::new(
        identity("actor", ActionId::DelegateSession),
        stamp("session", 0),
        legacy_receipt()
    )
    .is_err());
    assert!(ScopedRevocationReceipt::new(
        identity("actor", ActionId::RevokeSession),
        stamp("s", 2),
        session("target"),
        RevocationEpoch::new(2).unwrap(),
        1,
        ContentDigest([1; 32])
    )
    .is_err());
    let revoked = ScopedRevocationReceipt::new(
        identity("actor", ActionId::RevokeSession),
        stamp("s", 2),
        session("target"),
        RevocationEpoch::new(3).unwrap(),
        1,
        ContentDigest([1; 32]),
    )
    .unwrap();
    assert_ne!(
        revocation_receipt_input(&revoked).unwrap().as_bytes(),
        session_receipt_input(&issued()).unwrap().as_bytes()
    );
}

struct SyntheticSecret(&'static [u8]);
impl IssuedSecretMaterial for SyntheticSecret {
    fn borrow_secret(&self) -> Result<BorrowedSecret<'_>, ValidationError> {
        BorrowedSecret::new(self.0)
    }
}
#[test]
fn public_receipts_cannot_claim_secret_delivery_without_a_private_handoff() {
    assert!(matches!(
        BorrowedSecret::new(&[0; 129]),
        Err(ValidationError::CredentialSize)
    ));
    assert!(matches!(
        RawCredential::from_bytes(b" \t"),
        Ok(RawCredential::Empty)
    ));
    assert!(matches!(
        RawCredential::from_bytes(b"synthetic"),
        Ok(RawCredential::Presented(_))
    ));
    assert!(
        GatewayOutcome::<SyntheticSecret>::public_only(PublicOutcome::SessionIssued {
            receipt: issued(),
            replayed: false
        })
        .is_err()
    );
    assert!(GatewayOutcome::issued(issued(), false, SyntheticSecret(b" ")).is_err());
    let delivered =
        GatewayOutcome::issued(issued(), false, SyntheticSecret(b"synthetic-private")).unwrap();
    assert_eq!(delivered.public().exit_code(), 0);
    let (public, private) = delivered.into_parts();
    assert!(matches!(public, PublicOutcome::SessionIssued { .. }));
    let private = private.unwrap();
    assert_eq!(private.assigned(), issued().assigned());
    assert_eq!(
        private.material().borrow_secret().unwrap().expose_bytes(),
        b"synthetic-private"
    );
    let lost = GatewayOutcome::<SyntheticSecret>::delivery_unavailable(issued());
    assert_eq!(lost.public().exit_code(), 1);
    let (public, private) = lost.into_parts();
    assert!(matches!(
        public,
        PublicOutcome::CommittedDeliveryUnavailable { .. }
    ));
    assert!(private.is_none());
    assert_eq!(
        PublicOutcome::Refused(PolicyRefusal::Revoked).exit_code(),
        3
    );
    assert_eq!(
        PublicOutcome::Failed(GatewayFailure::UnknownCommitOutcome).exit_code(),
        1
    );
    assert_eq!(PublicOutcome::NotFound.exit_code(), 4);
}

#[test]
fn query_and_read_result_bounds_refuse_foreign_scope_and_unsorted_rows() {
    let row = TaskSummary {
        reference: legacy_receipt().reference,
        version: 1,
        body_digest: ContentDigest([1; 32]),
    };
    assert!(TaskQueryResult::new(&exact(), vec![row.clone()]).is_ok());
    let mut foreign = row.clone();
    foreign.reference.scope.project_id = "foreign".into();
    assert!(TaskQueryResult::new(&exact(), vec![foreign]).is_err());
    assert!(TaskQueryResult::new(&exact(), vec![row.clone(); 1025]).is_err());
    assert!(TaskQueryResult::new(&exact(), vec![row.clone(), row.clone()]).is_err());
    let mut later = row.clone();
    later.reference.task_id = "zzz".into();
    assert!(TaskQueryResult::new(&exact(), vec![later, row.clone()]).is_err());
    let record = TaskRecord {
        reference: row.reference,
        version: 1,
        title: "private".into(),
        body: b"private".to_vec(),
        body_digest: ContentDigest([1; 32]),
        artifact_references: vec![],
        event_sequence: 1,
    };
    assert!(TaskReadResult::new(&exact(), record.clone()).is_ok());
    let mut foreign = record.clone();
    foreign.reference.scope.project_id = "foreign".into();
    assert!(TaskReadResult::new(&exact(), foreign).is_err());
    let mut large = record;
    large.artifact_references = vec![ContentDigest([1; 32]); 17];
    assert!(TaskReadResult::new(&exact(), large).is_err());
}

#[test]
fn caller_created_owner_facts_are_bounded_data_not_an_admission_permit() {
    let owner = ActorRecord::new(actor("owner"), ActorKind::Owner);
    let session = SessionRecord::new(
        session("session"),
        actor("different-actor"),
        Some(session("missing-parent")),
        exact(),
        grants(&[ActionId::ReadTask]),
        SessionLifetime::new(time(1), time(1000)).unwrap(),
        SessionState::new(false, DelegationDepth::new(1).unwrap()),
    );
    let facts = AuthorityFacts::new(
        AuthorityId::new("authority".into()).unwrap(),
        RevocationEpoch::new(0).unwrap(),
        owner,
        session.clone(),
        CredentialRecord::new(
            AuthorityId::new("authority".into()).unwrap(),
            CredentialId::new("credential".into()).unwrap(),
            session.id().clone(),
            CredentialGeneration::new(1).unwrap(),
        ),
        vec![],
    )
    .unwrap();
    assert_ne!(facts.actor().id(), facts.session().actor_id()); // coherence belongs to real policy
    assert!(AuthorityFacts::new(
        AuthorityId::new("authority".into()).unwrap(),
        RevocationEpoch::new(0).unwrap(),
        facts.actor().clone(),
        session.clone(),
        facts.credential().clone(),
        vec![session; 9]
    )
    .is_err());
    struct Unavailable;
    impl AtomicScopedBackend for Unavailable {
        type IssuedSecret = SyntheticSecret;
        fn execute_scoped(
            &mut self,
            _: &PreparedCommand,
            _: RawCredential<'_>,
            _: OperationBudget,
        ) -> GatewayOutcome<Self::IssuedSecret> {
            GatewayOutcome::public_only(PublicOutcome::Refused(PolicyRefusal::ActionUnavailable))
                .unwrap()
        }
    }
    let result = Unavailable.execute_scoped(
        &prepared(task()),
        RawCredential::from_bytes(b"owner-is-only-a-label").unwrap(),
        OperationBudget::default(),
    );
    assert!(matches!(
        result.public(),
        PublicOutcome::Refused(PolicyRefusal::ActionUnavailable)
    ));
}

#[test]
fn durable_grant_order_is_action_name_order_not_registry_declaration_order() {
    let all: Vec<_> = core_kernel::scoped_action_specs()
        .filter(|spec| spec.required_permission.is_some())
        .map(|spec| Grant::new(spec.id, spec.effect).unwrap())
        .collect();
    let actual = GrantSet::new(all).unwrap();
    let names: Vec<_> = actual
        .as_slice()
        .iter()
        .map(|grant| grant.action().name())
        .collect();
    assert_eq!(
        names,
        [
            "CreateTask",
            "DelegateSession",
            "ObserveCreateTask",
            "QueryTasks",
            "ReadTask",
            "RevokeSession"
        ]
    );
    let mut reversed = actual.as_slice().to_vec();
    reversed.reverse();
    let reversed = GrantSet::new(reversed).unwrap();
    let make = |set| {
        PreparedCommand::new(CommandInput::DelegateSession {
            operation_key: key("operation"),
            requested: RequestedGrant::new(
                exact(),
                set,
                time(1000),
                DelegationDepth::new(1).unwrap(),
            ),
        })
        .unwrap()
    };
    let original = make(actual);
    let permuted = make(reversed);
    let operation = identity("actor", ActionId::DelegateSession);
    let parent = session("parent");
    assert_eq!(
        command_input(&operation, &original, Some(&parent))
            .unwrap()
            .as_bytes(),
        command_input(&operation, &permuted, Some(&parent))
            .unwrap()
            .as_bytes()
    );
}
