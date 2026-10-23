package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mlnomadpy/dacli/internal/workspace"
)

type NativeStorageTask struct {
	ID, Project, Status, Digest string
	Body                        []byte
}
type NativeStorageSource struct {
	WorkspaceID string
	Authority   []byte
	Tasks       []NativeStorageTask
}
type NativeStorageExperiment struct {
	Schema        string            `json:"schema"`
	Mode          string            `json:"mode"`
	Authoritative bool              `json:"authoritative"`
	SourceDigest  string            `json:"source_digest"`
	TaskCount     int               `json:"task_count"`
	TaskIDs       []string          `json:"task_ids"`
	FileDigests   map[string]string `json:"file_digests"`
	Reconciled    bool              `json:"reconciled"`
}

func nativeStorageDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func NativeStorageSourceDigest(s NativeStorageSource) string {
	b, _ := json.Marshal(s)
	return nativeStorageDigest(b)
}
func ReadNativeStorageSource(w *workspace.Workspace, authority []byte) (NativeStorageSource, error) {
	out := NativeStorageSource{WorkspaceID: w.ID, Authority: append([]byte(nil), authority...), Tasks: []NativeStorageTask{}}
	graph, e := ReadCheckpointCanonical(w)
	if e != nil {
		return out, e
	}
	for _, t := range graph.Tasks {
		info, e := os.Lstat(t.Path)
		if e != nil || !info.Mode().IsRegular() || info.Size() > checkpointMaxFile {
			return out, Refusedf("storage source unavailable")
		}
		f, e := os.Open(t.Path)
		if e != nil {
			return out, e
		}
		body, e := io.ReadAll(io.LimitReader(f, checkpointMaxFile+1))
		_ = f.Close()
		if e != nil || len(body) > checkpointMaxFile || nativeStorageDigest(body) != graph.Digests[t.ID] {
			return out, Refusedf("storage source changed")
		}
		out.Tasks = append(out.Tasks, NativeStorageTask{t.ID, t.Project, string(t.Status), graph.Digests[t.ID], body})
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].ID < out.Tasks[j].ID })
	return out, nil
}

// NativeStorageProjection imports only into a private disposable projection.
// No authoritative loader, activation route, migration or deletion exists.
func NativeStorageProjection(w *workspace.Workspace, s NativeStorageSource, destination, mode string) (NativeStorageExperiment, error) {
	return nativeStorageProjection(w, s, destination, mode, nil)
}
func nativeStorageProjection(w *workspace.Workspace, s NativeStorageSource, destination, mode string, after func(string) error) (NativeStorageExperiment, error) {
	out := NativeStorageExperiment{Schema: "native-storage-experiment/v1", Mode: "projection-only", TaskCount: len(s.Tasks), TaskIDs: []string{}, FileDigests: map[string]string{}}
	if mode != "projection-only" || s.WorkspaceID != w.ID || len(s.Authority) == 0 || len(s.Authority) > 33554432 {
		return out, Refusedf("storage experiment requires non-authoritative projection")
	}
	root, e := filepath.EvalSymlinks(w.Root)
	if e != nil {
		return out, e
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination || destination == root || strings.HasPrefix(destination, root+string(filepath.Separator)) {
		return out, Refusedf("storage destination must be separate from live workspace")
	}
	parent, e := filepath.EvalSymlinks(filepath.Dir(destination))
	if e != nil {
		return out, Refusedf("storage destination unavailable")
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if destination == root || strings.HasPrefix(destination, root+string(filepath.Separator)) {
		return out, Refusedf("storage destination must be separate from live workspace")
	}
	if e = os.Mkdir(destination, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return out, e
	}
	info, e := os.Lstat(destination)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return out, Refusedf("private storage destination required")
	}
	seen := map[string]bool{}
	for _, t := range s.Tasks {
		if t.ID == "" || seen[t.ID] || nativeStorageDigest(t.Body) != t.Digest {
			return out, Refusedf("storage task identity or digest mismatch")
		}
		seen[t.ID] = true
		out.TaskIDs = append(out.TaskIDs, t.ID)
	}
	out.SourceDigest = NativeStorageSourceDigest(s)
	tasks, e := json.Marshal(s.Tasks)
	if e != nil {
		return out, e
	}
	var authority map[string]json.RawMessage
	if json.Unmarshal(s.Authority, &authority) != nil || len(authority) == 0 {
		return out, Refusedf("storage authority snapshot invalid")
	}
	// All private histories remain byte-for-byte inside a non-loadable snapshot envelope; the
	// imported task projection carries exact IDs/status/body digests.
	projection, e := json.Marshal(struct {
		Schema, SourceDigest string
		Authoritative        bool
		Tasks                []NativeStorageTask
	}{"native-storage-projection/v1", out.SourceDigest, false, s.Tasks})
	if e != nil {
		return out, e
	}
	envelope, e := json.Marshal(struct {
		Schema        string
		Authoritative bool
		Source        []byte
	}{"native-authority-snapshot/v1", false, s.Authority})
	if e != nil {
		return out, e
	}
	files := []struct {
		name string
		body []byte
	}{{"authority.snapshot", envelope}, {"tasks.snapshot", tasks}, {"projection.json", projection}}
	for _, file := range files {
		if e = writeNativeStorageImmutable(destination, file.name, file.body); e != nil {
			return out, e
		}
		out.FileDigests[file.name] = nativeStorageDigest(file.body)
		if after != nil {
			if e = after(file.name); e != nil {
				return out, e
			}
		}
	}
	// Reparse the actual imported files instead of declaring the in-memory
	// input reconciled. Interrupted imports resume only against identical bytes.
	actualFiles := make(map[string][]byte, len(files))
	for _, file := range files {
		actual, readErr := readNativeStoragePrivateFile(destination, file.name, len(file.body))
		if readErr != nil || nativeStorageDigest(actual) != out.FileDigests[file.name] || !bytes.Equal(actual, file.body) {
			return out, Refusedf("storage imported file mismatch")
		}
		actualFiles[file.name] = actual
	}
	imported := actualFiles["tasks.snapshot"]
	var restored []NativeStorageTask
	if json.Unmarshal(imported, &restored) != nil || len(restored) != len(s.Tasks) {
		return out, Refusedf("storage imported task count mismatch")
	}
	for n, t := range restored {
		want := s.Tasks[n]
		if t.ID != want.ID || t.Project != want.Project || t.Status != want.Status || t.Digest != want.Digest || !bytes.Equal(t.Body, want.Body) || nativeStorageDigest(t.Body) != t.Digest {
			return out, Refusedf("storage imported task identity mismatch")
		}
	}
	var recovered struct {
		Schema        string
		Authoritative bool
		Source        []byte
	}
	if json.Unmarshal(actualFiles["authority.snapshot"], &recovered) != nil || recovered.Schema != "native-authority-snapshot/v1" || recovered.Authoritative || !bytes.Equal(recovered.Source, s.Authority) {
		return out, Refusedf("storage imported authority mismatch")
	}
	out.Reconciled = true
	receipt, e := json.Marshal(out)
	if e != nil {
		return out, e
	}
	if e = writeNativeStorageImmutable(destination, "manifest.json", receipt); e != nil {
		return out, e
	}
	if after != nil {
		if e = after("manifest.json"); e != nil {
			return out, e
		}
	}
	return out, nil
}

func readNativeStoragePrivateFile(dir, name string, maxBytes int) ([]byte, error) {
	path := filepath.Join(dir, name)
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > int64(maxBytes) {
		return nil, Refusedf("private bounded storage file required")
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	body, readErr := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	after, statErr := f.Stat()
	closeErr := f.Close()
	current, pathErr := os.Lstat(path)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil || len(body) > maxBytes || !os.SameFile(info, after) || !os.SameFile(info, current) || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || len(body) != int(info.Size()) {
		return nil, Refusedf("private bounded storage file changed during observation")
	}
	return body, nil
}

func writeNativeStorageImmutable(dir, name string, body []byte) error {
	target := filepath.Join(dir, name)
	if info, e := os.Lstat(target); e == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return Refusedf("private immutable storage file required")
		}
		old, e := readNativeStoragePrivateFile(dir, name, len(body))
		if e != nil || !bytes.Equal(old, body) {
			return Refusedf("storage projection source or file changed")
		}
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	// Link is an atomic no-replace publication. Crash leftovers are never
	// deleted automatically and cannot activate the disposable projection.
	f, e := os.CreateTemp(dir, ".projection-")
	if e != nil {
		return e
	}
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(body)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Link(f.Name(), target); e != nil {
		if errors.Is(e, os.ErrExist) {
			return writeNativeStorageImmutable(dir, name, body)
		}
		return e
	}
	directory, e := os.Open(dir)
	if e != nil {
		return e
	}
	e = directory.Sync()
	closeErr = directory.Close()
	if e != nil {
		return e
	}
	return closeErr
}
