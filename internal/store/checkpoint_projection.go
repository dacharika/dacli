package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/mlnomadpy/dacli/internal/eventdisp"
	"github.com/mlnomadpy/dacli/internal/mdstore"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

const checkpointMaxEntries = 20000
const checkpointMaxFile = 262144
const checkpointMaxBytes = 33554432
const checkpointMaxDepth = 8

type checkpointReader struct{ entries, bytes int }

func (r *checkpointReader) directory(f *os.File) ([]os.DirEntry, error) {
	info, e := f.Stat()
	if e != nil || !info.IsDir() {
		return nil, fmt.Errorf("checkpoint directory unavailable")
	}
	entries, e := f.ReadDir(checkpointMaxEntries - r.entries + 1)
	if e != nil && e != io.EOF {
		return nil, e
	}
	r.entries += len(entries)
	if r.entries > checkpointMaxEntries {
		return nil, fmt.Errorf("checkpoint entry limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}
func (r *checkpointReader) read(f *os.File) ([]byte, error) {
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > checkpointMaxFile {
		return nil, fmt.Errorf("checkpoint requires a bounded regular file")
	}
	b, e := io.ReadAll(io.LimitReader(f, checkpointMaxFile+1))
	r.bytes += len(b)
	if e != nil || len(b) > checkpointMaxFile || r.bytes > checkpointMaxBytes {
		return nil, fmt.Errorf("checkpoint byte limit exceeded")
	}
	after, e := f.Stat()
	if e != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, fmt.Errorf("checkpoint file changed during observation")
	}
	return b, nil
}
func (r *checkpointReader) childBytes(parent *os.File, name string) ([]byte, error) {
	f, e := checkpointChild(parent, name)
	if e != nil {
		return nil, e
	}
	defer func() { _ = f.Close() }()
	return r.read(f)
}
func checkpointDirectory(parent *os.File, name string) (*os.File, error) {
	f, e := checkpointChild(parent, name)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil || !info.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("checkpoint requires directory")
	}
	return f, nil
}

// CheckpointCanonical is a finite complete graph snapshot. Unlike ListTasks,
// malformed or duplicate records are errors, never skipped or deduplicated.
// Digests use the exact bounded bytes that were parsed, with no second read.
type CheckpointCanonical struct {
	Tasks    []*Task
	Digests  map[string]string
	Projects map[string]*Project
}

func ReadCheckpointCanonical(w *workspace.Workspace) (CheckpointCanonical, error) {
	out := CheckpointCanonical{Digests: map[string]string{}, Projects: map[string]*Project{}}
	r := checkpointReader{}
	root, e := checkpointOpen(w.ProjectsDir())
	if e != nil {
		return out, e
	}
	defer func() { _ = root.Close() }()
	projects, e := r.directory(root)
	if e != nil {
		return out, e
	}
	seen := map[string]bool{}
	for _, entry := range projects {
		if strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		dir, e := checkpointDirectory(root, entry.Name())
		if e != nil {
			return out, e
		}
		e = func() error {
			defer func() { _ = dir.Close() }()
			raw, e := r.childBytes(dir, "project.md")
			if e != nil {
				return e
			}
			doc, e := mdstore.Parse(string(raw))
			if e != nil {
				return e
			}
			mode, _ := doc.Front.Get("landing.mode")
			base, set := doc.Front.Get("landing.base")
			if set && strings.TrimSpace(base) == "" {
				return fmt.Errorf("invalid checkpoint landing base")
			}
			policy := model.LandingPolicy{Mode: model.LandingMode(mode), Base: base}
			if _, _, e := model.ResolveLanding(policy, model.LandingOverride{}); e != nil {
				return e
			}
			out.Projects[entry.Name()] = &Project{Slug: entry.Name(), Doc: doc, Path: w.ProjectPath(entry.Name()), Landing: policy}
			taskDir, e := checkpointDirectory(dir, "tasks")
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			defer func() { _ = taskDir.Close() }()
			for _, status := range model.AllStatuses {
				statusDir, e := checkpointDirectory(taskDir, string(status))
				if os.IsNotExist(e) {
					continue
				}
				if e != nil {
					return e
				}
				e = func() error {
					defer func() { _ = statusDir.Close() }()
					files, e := r.directory(statusDir)
					if e != nil {
						return e
					}
					for _, file := range files {
						if !strings.HasSuffix(file.Name(), ".md") {
							continue
						}
						raw, e := r.childBytes(statusDir, file.Name())
						if e != nil {
							return e
						}
						doc, e := mdstore.Parse(string(raw))
						if e != nil {
							return e
						}
						id, _ := doc.Front.Get("id")
						kind, _ := doc.Front.Get("kind")
						if id == "" || seen[id] || kind != "task" {
							return fmt.Errorf("invalid or duplicate checkpoint task")
						}
						seen[id] = true
						name := strings.TrimSuffix(file.Name(), ".md")
						seq, slug, _ := strings.Cut(name, "-")
						n, e := strconv.Atoi(seq)
						if e != nil || n < 1 || slug == "" {
							return fmt.Errorf("invalid checkpoint task filename")
						}
						task := &Task{ID: id, Seq: n, Slug: slug, Project: entry.Name(), Status: status, Doc: doc, Path: filepath.Join(w.TasksDir(entry.Name(), status), file.Name())}
						for _, s := range doc.Sections {
							if s.Level == 1 {
								task.Title = s.Title
								break
							}
						}
						hash := sha256.Sum256(raw)
						out.Digests[id] = hex.EncodeToString(hash[:])
						out.Tasks = append(out.Tasks, task)
					}
					return nil
				}()
				if e != nil {
					return e
				}
			}
			return nil
		}()
		if e != nil {
			return out, e
		}
	}
	sort.Slice(out.Tasks, func(i, j int) bool { return out.Tasks[i].ID < out.Tasks[j].ID })
	return out, nil
}

type checkpointEvent struct {
	id, about, kind, applied string
	doc                      *mdstore.Doc
	path                     string
}

func checkpointEventPayload(d *mdstore.Doc) eventdisp.Payload {
	get := func(k string) string { v, _ := d.Front.Get(k); return v }
	version, _ := strconv.Atoi(get("schema_version"))
	body := ""
	for _, s := range d.Sections {
		body += s.Content
	}
	return eventdisp.Payload{SchemaVersion: version, ID: get("id"), DocumentKind: model.Kind(get("kind")), Kind: model.EventKind(get("event_kind")), Created: get("created"), Actor: get("created_by"), About: strings.TrimSuffix(strings.TrimPrefix(get("about"), "[["), "]]"), Origin: get("origin"), Against: get("against"), Body: strings.TrimSpace(body)}
}
func (r *checkpointReader) events(dir *os.File, depth int, out *[]checkpointEvent) error {
	if depth > checkpointMaxDepth {
		return fmt.Errorf("checkpoint directory depth exceeded")
	}
	entries, e := r.directory(dir)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		child, e := checkpointChild(dir, entry.Name())
		if e != nil {
			return e
		}
		e = func() error {
			defer func() { _ = child.Close() }()
			info, e := child.Stat()
			if e != nil {
				return e
			}
			if info.IsDir() {
				return r.events(child, depth+1, out)
			}
			if !strings.HasSuffix(entry.Name(), ".md") {
				return nil
			}
			raw, e := r.read(child)
			if e != nil {
				return e
			}
			doc, e := mdstore.Parse(string(raw))
			if e != nil {
				return e
			}
			payload := checkpointEventPayload(doc)
			version, has := doc.Front.Get("schema_version")
			checksum, _ := doc.Front.Get("checksum")
			if has || checksum != "" {
				if version != "1" || checksum != eventdisp.Checksum(payload) {
					return fmt.Errorf("checkpoint event integrity unavailable")
				}
			}
			if payload.ID == "" || payload.DocumentKind != model.KindEvent {
				return fmt.Errorf("invalid checkpoint event")
			}
			applied, _ := doc.Front.Get("applied")
			*out = append(*out, checkpointEvent{payload.ID, payload.About, string(payload.Kind), applied, doc, entry.Name()})
			return nil
		}()
		if e != nil {
			return e
		}
	}
	return nil
}

// CheckpointLocalProjection observes only scoped task/run/event facts. Legacy
// process records never prove stopped writers or native host identity. No ps,
// Git, network, state writes, or legacy unbounded readers are called here.
func CheckpointLocalProjection(w *workspace.Workspace, project string, tasks []*Task, scope []string) (DeliveryProjection, error) {
	p := DeliveryProjection{Schema: DeliverySchemaVersion, Version: 1, Project: project, Reconciled: true, Findings: []DeliveryFinding{}}
	allowed := map[string]*Task{}
	for _, t := range tasks {
		if t.Project == project {
			for _, id := range scope {
				if id == t.ID {
					allowed[id] = t
				}
			}
		}
	}
	r := checkpointReader{}
	events := []checkpointEvent{}
	for _, path := range []string{w.EventsDir(), filepath.Join(w.Root, workspace.Dir, "events-archive")} {
		root, e := checkpointOpen(path)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return p, e
		}
		e = r.events(root, 0, &events)
		_ = root.Close()
		if e != nil {
			return p, e
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].path > events[j].path })
	dismissed := map[string]bool{}
	for _, event := range events {
		payload := checkpointEventPayload(event.doc)
		checksum, _ := event.doc.Front.Get("checksum")
		if payload.SchemaVersion == 1 && event.kind == string(model.EventDismissal) && event.applied == "true" && event.about != "" && checksum == eventdisp.Checksum(payload) {
			dismissed[event.about] = true
		}
		task := allowed[event.about]
		if task == nil || event.applied != "false" || dismissed[event.id] {
			continue
		}
		class, action := "pending-event", "owner reviews and applies the scoped pending event"
		if task.Status == model.StatusDone {
			class = "terminal-task-event"
			action = "owner reviews and dismisses the event or reopens the exact task"
		}
		add(&p, class, event.id, "bounded/event-log", "major", DeliveryKnown, "scoped durable pending event", action)
		p.Findings[len(p.Findings)-1].RelatedRefs = []DeliveryRef{{Kind: "task", ID: task.ID}}
	}
	root, e := checkpointOpen(w.RunsDir())
	if os.IsNotExist(e) {
		return p, nil
	}
	if e != nil {
		return p, e
	}
	defer func() { _ = root.Close() }()
	runs, e := r.directory(root)
	if e != nil {
		return p, e
	}
	for _, run := range runs {
		dir, e := checkpointDirectory(root, run.Name())
		if e != nil {
			return p, e
		}
		e = func() error {
			defer func() { _ = dir.Close() }()
			raw, e := r.childBytes(dir, "proc.txt")
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			task := field(string(raw), "task")
			if allowed[task] == nil {
				return nil
			}
			if field(string(raw), "outcome") == "" {
				add(&p, "legacy-run-outcome-unknown", run.Name(), "bounded/run-record", "major", DeliveryUnknown, "legacy process record does not prove native execution or stopped writers", "observe the exact host outcome and use audited owner recovery for retained protection")
				p.Findings[len(p.Findings)-1].RelatedRefs = []DeliveryRef{{Kind: "task", ID: task}}
			}
			return nil
		}()
		if e != nil {
			return p, e
		}
	}
	sort.Slice(p.Findings, func(i, j int) bool { return p.Findings[i].ID < p.Findings[j].ID })
	return p, nil
}
