#[cfg(unix)]
use crate::store::hash_bytes;
use std::path::Path;
#[cfg(unix)]
use std::{fs::OpenOptions, io::Read};
use store_contracts::*;
/// Report real content-addressed file availability after DB reads have finished.
/// This verifies selected local fixture files; it is not a full backup product.
pub fn verify_artifact_files(record: &TaskRecord, root: &Path) -> Vec<ArtifactProblem> {
    #[cfg(not(unix))]
    {
        let _ = root;
        return record
            .artifact_references
            .iter()
            .map(|digest| ArtifactProblem {
                digest: *digest,
                state: ArtifactState::Unreadable,
            })
            .collect();
    }
    #[cfg(unix)]
    record
        .artifact_references
        .iter()
        .filter_map(|digest| {
            let path = root.join(digest.hex());
            let mut options = OpenOptions::new();
            options.read(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                options.custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK);
            }
            let state = match options.open(&path) {
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                    Some(ArtifactState::Missing)
                }
                Err(_) => Some(ArtifactState::Unreadable),
                Ok(file) => match file.metadata() {
                    Ok(meta) if meta.is_file() && meta.len() <= MAXIMUM_BODY_BYTES as u64 => {
                        let mut data = Vec::new();
                        match file
                            .take((MAXIMUM_BODY_BYTES + 1) as u64)
                            .read_to_end(&mut data)
                        {
                            Ok(_)
                                if data.len() <= MAXIMUM_BODY_BYTES
                                    && hash_bytes(&data) == *digest =>
                            {
                                None
                            }
                            Ok(_) => Some(ArtifactState::Corrupt),
                            Err(_) => Some(ArtifactState::Unreadable),
                        }
                    }
                    _ => Some(ArtifactState::Unreadable),
                },
            };
            state.map(|state| ArtifactProblem {
                digest: *digest,
                state,
            })
        })
        .collect()
}
