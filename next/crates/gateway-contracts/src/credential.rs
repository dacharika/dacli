//! Raw credential material is bounded and deliberately cannot be Debug/Clone.
//! No type in this module verifies a credential or creates an owner context.
use crate::{AuthorityId, CredentialGeneration, CredentialId, SessionId, ValidationError};

/// A bounded raw borrow, not a parsed/verified credential or permission.
/// ```compile_fail
/// use gateway_contracts::BorrowedSecret;
/// let secret = BorrowedSecret::new(b"synthetic").unwrap();
/// let _ = format!("{secret:?}");
/// ```
/// ```compile_fail
/// use gateway_contracts::BorrowedSecret;
/// let secret = BorrowedSecret::new(b"synthetic").unwrap();
/// let _ = secret.clone();
/// ```
pub struct BorrowedSecret<'a>(&'a [u8]);
impl<'a> BorrowedSecret<'a> {
    pub fn new(bytes: &'a [u8]) -> Result<Self, ValidationError> {
        if bytes.len() > crate::MAXIMUM_CREDENTIAL_BYTES {
            return Err(ValidationError::CredentialSize);
        }
        if bytes.is_empty() || bytes.iter().all(u8::is_ascii_whitespace) {
            return Err(ValidationError::CredentialEmpty);
        }
        Ok(Self(bytes))
    }
    /// Explicit exposure for the trusted verifier/private secret sink only.
    pub fn expose_bytes(&self) -> &[u8] {
        self.0
    }
}

/// Scoped ports have no PublicProtocol variant and no ambient fallback.
/// ```compile_fail
/// use gateway_contracts::RawCredential;
/// let _ = RawCredential::PublicProtocol;
/// ```
/// ```compile_fail
/// use gateway_contracts::RawCredential;
/// let credential = RawCredential::Missing;
/// let _ = format!("{credential:?}");
/// ```
pub enum RawCredential<'a> {
    Missing,
    Empty,
    Presented(BorrowedSecret<'a>),
}
impl<'a> RawCredential<'a> {
    pub fn from_bytes(bytes: &'a [u8]) -> Result<Self, ValidationError> {
        match BorrowedSecret::new(bytes) {
            Ok(secret) => Ok(Self::Presented(secret)),
            Err(ValidationError::CredentialEmpty) => Ok(Self::Empty),
            Err(error) => Err(error),
        }
    }
}

/// The fixture credential implementation owns retention/erasure policy. This
/// lower port carries no crypto/vault implementation and makes no erasure claim.
pub trait IssuedSecretMaterial {
    fn borrow_secret(&self) -> Result<BorrowedSecret<'_>, ValidationError>;
}

/// Current canonical credential row data (without verifier/secret bytes).
/// Constructing it does not authenticate it. Policy must compare its authority,
/// ID, session and generation to the snapshot and fresh CredentialMatch; only
/// the selected transaction adapter can establish freshness and secret match.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CredentialRecord {
    authority_id: AuthorityId,
    credential_id: CredentialId,
    session_id: SessionId,
    generation: CredentialGeneration,
}
impl CredentialRecord {
    pub fn new(
        authority_id: AuthorityId,
        credential_id: CredentialId,
        session_id: SessionId,
        generation: CredentialGeneration,
    ) -> Self {
        Self {
            authority_id,
            credential_id,
            session_id,
            generation,
        }
    }
    pub fn authority_id(&self) -> &AuthorityId {
        &self.authority_id
    }
    pub fn credential_id(&self) -> &CredentialId {
        &self.credential_id
    }
    pub fn session_id(&self) -> &SessionId {
        &self.session_id
    }
    pub const fn generation(&self) -> CredentialGeneration {
        self.generation
    }
}

/// Canonical verifier comparison fact supplied inside a selected transaction.
/// Public construction validates shape only: it is NOT authentication evidence
/// accepted by AtomicScopedBackend, nor a serializable bearer grant.
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CredentialMatch {
    credential_id: CredentialId,
    session_id: SessionId,
    generation: CredentialGeneration,
}
impl CredentialMatch {
    pub fn new(
        credential_id: CredentialId,
        session_id: SessionId,
        generation: CredentialGeneration,
    ) -> Self {
        Self {
            credential_id,
            session_id,
            generation,
        }
    }
    pub fn credential_id(&self) -> &CredentialId {
        &self.credential_id
    }
    pub fn session_id(&self) -> &SessionId {
        &self.session_id
    }
    pub const fn generation(&self) -> CredentialGeneration {
        self.generation
    }
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum CredentialEvidence {
    Missing,
    Invalid,
    Matched(CredentialMatch),
}
