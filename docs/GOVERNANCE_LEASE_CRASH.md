# Native lease interruption and replay

The private authority uses exclusive lock-file creation and durable atomic
replacement of its state file. Each operation reloads authority under the lock.
If a writer crashes while holding the lock, its lock remains. Operations wait
for at most approximately five seconds, then refuse with `ErrStorage`. Process
absence and elapsed time never authorize automatic lock removal or takeover.
An owner must reconcile the exact operation and writer before explicitly
recovering a production lock. These tests remove only their own temporary lock,
after terminating and reaping the exact child they created.

`TestLeaseCrashPreservesLockAndAuthority` observes entry into the real
transaction callback, kills that child, and verifies bounded refusal, identical
persisted authority bytes and the same retained lock inode. Explicit fixture
recovery then acquires fence 1, proving refusal did not allocate a fence.

`TestLeaseLostReplyReplayAcrossProcesses` commits acquisition in a child while
suppressing its reply. After release and a newer acquisition at fence 2, a
fresh child replays the original operation key and payload. It recovers the
exact original receipt, without changing state bytes or allocating a fence.
That historical receipt cannot renew the released lease. Changed replay payloads
are denied. Test credentials travel through child stdin rather than argv.

Temporary Go overlays demonstrate both guards matter: automatic stale-lock
removal makes the crash test fail with "crashed lock must refuse, got <nil>";
disabling receipt retry makes the lost-reply test fail with "lost reply replay
failed". Production guards are never edited for these mutations.

This proves cooperative local persistence and replay behavior, not prevention
of arbitrary filesystem writes, autonomous recovery of uncertain operations,
or native acceptance/publication consumption. Recovery is intentionally an
owner decision; an old fence or replayed receipt is never current permission.
