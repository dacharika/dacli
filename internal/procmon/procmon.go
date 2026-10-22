// Package procmon watches and terminates spawned-agent process TREES. A
// dacli-spawned agent (e.g. `claude -p ...`) forks a whole tree of helper
// subprocesses; left unsupervised, a hung or runaway agent leaks that tree's
// RAM/CPU/GPU long after dacli has moved on. procmon gives every spawn a
// killable process GROUP and a live resource probe, so a *separate* dacli
// invocation (`dacli agents`, `dacli kill`) can see the tree and reap it as a
// unit — never leaving orphaned children behind.
//
// This is a shared entity, not a feature slice: the execution slice writes the
// records at spawn time; the monitoring/kill commands read them back. Liveness
// is NEVER trusted from the file — it is always probed live.
package procmon

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/commandresult"
)

// Record is written to a run's proc.txt at spawn time so a different dacli
// process can find the live tree. PID is the group leader (the runtime binary
// dacli exec'd); PGID equals it, because Setpgid makes that child a new group
// leader. Every subprocess the agent forks inherits PGID unless it detaches.
type Record struct {
	RunID   string
	Child   string
	Task    string
	Role    string
	Runtime string
	PID     int
	PGID    int
	// PIDStart is the OS-reported start time of PID captured at spawn (ps
	// lstart). A PID is recycled by the kernel once the original process exits,
	// so PID alone cannot prove a proc.txt still names OUR agent. Re-reading the
	// live PID's start time and comparing it to PIDStart rejects a recycled PID:
	// an unrelated process that inherited the number started at a different time.
	// Empty on legacy records (best-effort: fall back to a bare liveness probe).
	PIDStart string
	Started  time.Time
	Timeout  time.Duration
	Claims   []string // repo paths this agent declared it will edit (advisory lock)
	Outcome  string   // non-empty once claims have been released at a terminal transition
}

// WriteRecord persists r as key: value lines, matching the run dir's other
// plain-text records (invocation.txt, outcome.md).
func WriteRecord(path string, r Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "run: %s\n", r.RunID)
	fmt.Fprintf(&b, "child: %s\n", r.Child)
	fmt.Fprintf(&b, "task: %s\n", r.Task)
	fmt.Fprintf(&b, "role: %s\n", r.Role)
	fmt.Fprintf(&b, "runtime: %s\n", r.Runtime)
	fmt.Fprintf(&b, "pid: %d\n", r.PID)
	fmt.Fprintf(&b, "pgid: %d\n", r.PGID)
	if r.PIDStart != "" {
		fmt.Fprintf(&b, "pid_start: %s\n", r.PIDStart)
	}
	fmt.Fprintf(&b, "started: %s\n", r.Started.UTC().Format(time.RFC3339Nano))
	if r.Timeout > 0 {
		fmt.Fprintf(&b, "timeout_s: %d\n", int(r.Timeout/time.Second))
	}
	if len(r.Claims) > 0 {
		fmt.Fprintf(&b, "claims: %s\n", strings.Join(r.Claims, ","))
	}
	if r.Outcome != "" {
		fmt.Fprintf(&b, "outcome: %s\n", r.Outcome)
	}
	return writeRecordAtomic(path, []byte(b.String()))
}

// CompleteRecord persists the terminal outcome and releases path claims in one
// rename. Readers therefore see either the live record with its claims or the
// terminal record without them, never a terminal result that still fences a
// follow-up spawn (issue #588).
func CompleteRecord(path string, r Record, outcome string) error {
	r.Claims = nil
	r.Outcome = outcome
	return WriteRecord(path, r)
}

func writeRecordAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".proc-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadRecord preserves the historical permissive parser for diagnostic readers.
// Destructive retained-evidence consumers use ReadRetainedRecord instead.
func ReadRecord(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	return parseRecord(raw, false)
}

// ReadRetainedRecord refuses malformed process evidence rather than converting
// a bad PID to zero and declaring its owner dead (core499). Optional legacy
// fields remain optional; explicit numeric/time fields must parse, and repeated
// fields cannot silently replace the recorded owner or process identity.
func ReadRetainedRecord(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	return parseRecord(raw, true)
}

func parseRecord(raw []byte, strict bool) (Record, error) {
	r := Record{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			if strict && strings.TrimSpace(line) != "" {
				return Record{}, fmt.Errorf("proc.txt has malformed line")
			}
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if strict && seen[k] {
			return Record{}, fmt.Errorf("proc.txt repeats field %s", k)
		}
		seen[k] = true
		switch k {
		case "run":
			r.RunID = v
		case "child":
			r.Child = v
		case "task":
			r.Task = v
		case "role":
			r.Role = v
		case "runtime":
			r.Runtime = v
		case "pid":
			n, err := strconv.Atoi(v)
			if strict && (err != nil || n < 0) {
				return Record{}, fmt.Errorf("proc.txt has invalid pid")
			}
			r.PID = n
		case "pgid":
			n, err := strconv.Atoi(v)
			if strict && (err != nil || n < 0) {
				return Record{}, fmt.Errorf("proc.txt has invalid pgid")
			}
			r.PGID = n
		case "pid_start":
			r.PIDStart = v
		case "started":
			at, err := time.Parse(time.RFC3339, v)
			if strict && err != nil {
				return Record{}, fmt.Errorf("proc.txt has invalid started time")
			}
			r.Started = at
		case "timeout_s":
			seconds, err := strconv.Atoi(v)
			if strict && (err != nil || seconds <= 0 || int64(seconds) > int64((time.Duration(1<<63-1))/time.Second)) {
				return Record{}, fmt.Errorf("proc.txt has invalid timeout_s")
			}
			if err == nil && seconds > 0 {
				r.Timeout = time.Duration(seconds) * time.Second
			}
		case "claims":
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					r.Claims = append(r.Claims, p)
				}
			}
		case "outcome":
			r.Outcome = v
		}
	}
	if strict && r.Child == "" {
		return Record{}, fmt.Errorf("proc.txt has no child")
	}
	if strict && !seen["pid"] {
		return Record{}, fmt.Errorf("proc.txt has no pid")
	}
	// Zero denotes no identifiable leader, not evidence that an active writer
	// exited. Explicit terminal records retain their historical zero-PID form.
	if strict && r.PID == 0 && r.Outcome == "" {
		return Record{}, fmt.Errorf("proc.txt has active zero pid; process evidence is unknown")
	}
	return r, nil
}

// PathsOverlap reports whether any path in a claims the same tree as any path
// in b — i.e. one is the other, or a path-segment prefix of the other
// (internal/store vs internal/store/roles.go overlap; internal/store vs
// internal/storefront do NOT). A terminal /** is the claim syntax for the same
// recursive tree and is removed before comparison. No other wildcard syntax is
// interpreted: broadening embedded or single-star forms here would make a
// misspelled claim reserve and authorize paths the agent did not name. Used to
// refuse two live agents editing the same files in parallel and to enforce the
// same boundary at commit time (issue #641).
func PathsOverlap(a, b []string) (string, string, bool) {
	clean := func(p string) string {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if base, ok := strings.CutSuffix(p, "/**"); ok && base != "" && !strings.ContainsAny(base, "*?[") {
			return base
		}
		return p
	}
	prefix := func(p, q string) bool {
		p, q = clean(p), clean(q)
		return p == q || strings.HasPrefix(q, p+"/")
	}
	for _, x := range a {
		for _, y := range b {
			if prefix(x, y) || prefix(y, x) {
				return x, y, true
			}
		}
	}
	return "", "", false
}

// ProcStart returns pid's OS start time as reported by `ps -o lstart=` (an
// absolute wall-clock stamp, stable for the life of the process and identical
// on macOS and Linux). ok=false when the process is gone or ps cannot read it.
// This is the identity fingerprint used to detect a recycled PID.
func ProcStart(pid int) (string, bool) {
	start, ok, _ := ProcStartChecked(pid)
	return start, ok
}

// ProcStartChecked is ProcStart with observable probe failure. Invalid PIDs
// remain ordinary negative evidence (err=nil); an OS probe failure carries a
// typed diagnostic. ProcStart intentionally discards that diagnostic to
// preserve its conservative best-effort contract.
func ProcStartChecked(pid int) (string, bool, error) {
	if pid <= 0 {
		return "", false, nil
	}
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	out, err := commandresult.Output(cmd, commandresult.RunOptions{Operation: "probe process start"})
	if err != nil {
		return "", false, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", false, nil
	}
	return s, true, nil
}

// AliveIdentity reports whether pid is alive AND is still the same process that
// was recorded with start time wantStart. A live PID whose start time no longer
// matches wantStart was recycled by the kernel onto an unrelated process, so it
// is NOT our agent — treat it as gone. This is what stops a stale proc.txt from
// resurfacing a dead run as live or steering KillTree onto someone else's group.
// A record with no recorded start time (wantStart == "", e.g. a legacy proc.txt)
// falls back to a bare liveness probe, preserving prior behavior.
func AliveIdentity(pid int, wantStart string) bool {
	if !Alive(pid) {
		return false
	}
	if wantStart == "" {
		return true
	}
	got, ok := ProcStart(pid)
	if !ok {
		// The PID is live (signal-0 succeeded) but we cannot read its start time
		// to confirm identity. Refuse to vouch for it rather than risk sampling
		// or killing a recycled PID.
		return false
	}
	return got == wantStart
}

// AliveRecord is AliveIdentity applied to a run's Record — the identity-checked
// liveness test every reader (agents/kill/wait/logs) should use in place of a
// bare Alive(rec.PID).
func AliveRecord(r Record) bool { return AliveIdentity(r.PID, r.PIDStart) }

// ReconcileRun decides whether an on-disk run record still identifies a live
// dacli process tree. The recorded leader's identity is the authority: while
// that leader is alive, its process group still covers every descendant it
// spawned. Once the leader is gone, however, a bare numeric PGID carries no
// identity and may already name an unrelated, newly-created group. Treating
// GroupAlive(r.PGID) as proof in that state was the residual left by task 285;
// it could both pin a completed run and steer KillTree at strangers.
func ReconcileRun(r Record) bool { return AliveRecord(r) }

// Usage is a resource snapshot of one process group.
type Usage struct {
	Procs  int     // members of the group currently alive
	RSSKB  int     // summed resident memory (KB)
	CPUPct float64 // summed ps %cpu — a per-process LIFETIME AVERAGE, not current load
	GPUMiB int     // summed GPU memory (nvidia only); -1 when unmeasurable
}

// SampleGroup sums RSS and %CPU over every process whose group id is pgid, by
// parsing a single `ps` snapshot (BSD/GNU compatible keywords). GPU is layered
// on best-effort; on a machine with no nvidia-smi it stays -1 (reported as
// n/a, never faked).
//
// Exited-but-unreaped members (zombies) are excluded (dacli 217): a corpse
// holds no RAM and burns no CPU, and counting it would report a tree as larger
// than it is — the same phantom that `dacli agents` shows when liveness
// trusts a bare signal-0 probe.
func SampleGroup(pgid int) Usage {
	u, _ := SampleGroupChecked(pgid)
	return u
}

// SampleGroupChecked exposes a failed process-table probe as a typed
// diagnostic. Optional GPU absence is not a group-sampling failure: callers
// still receive the CPU/RSS snapshot with GPUMiB=-1, exactly as before.
func SampleGroupChecked(pgid int) (Usage, error) {
	u := Usage{GPUMiB: -1}
	if pgid <= 0 {
		return u, nil
	}
	cmd := exec.Command("ps", "-A", "-o", "pgid=,pid=,rss=,%cpu=,state=")
	out, err := commandresult.Output(cmd, commandresult.RunOptions{Operation: "sample process group"})
	if err != nil {
		return u, err
	}
	var pids []int
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		pg, _ := strconv.Atoi(fields[0])
		if pg != pgid {
			continue
		}
		if strings.HasPrefix(fields[4], "Z") {
			continue
		}
		pid, _ := strconv.Atoi(fields[1])
		rss, _ := strconv.Atoi(fields[2])
		cpu, _ := strconv.ParseFloat(fields[3], 64)
		u.Procs++
		u.RSSKB += rss
		u.CPUPct += cpu
		pids = append(pids, pid)
	}
	if g, ok := gpuByPID(pids); ok {
		u.GPUMiB = g
	}
	return u, nil
}

// gpuByPID sums GPU memory held by any of pids via nvidia-smi's compute-app
// table. Returns ok=false when nvidia-smi is absent (Apple silicon, CPU box)
// or reports nothing for these pids — the caller then shows n/a.
func gpuByPID(pids []int) (int, bool) {
	total, ok, _ := GPUByPIDChecked(pids)
	return total, ok
}

// GPUByPIDChecked distinguishes optional nvidia-smi absence (no evidence,
// err=nil) from an installed probe that failed (typed diagnostic). Sampling
// remains best-effort because gpuByPID deliberately discards the latter.
func GPUByPIDChecked(pids []int) (int, bool, error) {
	if len(pids) == 0 {
		return 0, false, nil
	}
	if _, err := exec.LookPath("nvidia-smi"); errors.Is(err, exec.ErrNotFound) {
		return 0, false, nil
	} else if err != nil {
		cmd := exec.Command("nvidia-smi")
		return 0, false, commandresult.NewExternalError(cmd, commandresult.RunOptions{Operation: "locate GPU process sampler"}, nil, nil, err, false)
	}
	cmd := exec.Command("nvidia-smi",
		"--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits")
	out, err := commandresult.Output(cmd, commandresult.RunOptions{Operation: "sample GPU processes"})
	if err != nil {
		return 0, false, err
	}
	want := make(map[int]bool, len(pids))
	for _, p := range pids {
		want[p] = true
	}
	total, matched := 0, false
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ",")
		if len(parts) < 2 {
			continue
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		if !want[pid] {
			continue
		}
		mib, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		total += mib
		matched = true
	}
	return total, matched, nil
}
