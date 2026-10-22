package application

import (
	"github.com/mlnomadpy/dacli/internal/governance/nativeconfig"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/store"
)

// NativeStorageOwner selects an immutable explicit private owner profile;
// neither ambient legacy grants nor enrolled worker credentials substitute.
type NativeStorageOwner struct{ owner *NativeRecoveryAccess }

func OpenNativeStorageOwner(path string) (*NativeStorageOwner, error) {
	p, e := OpenNativeRecovery(path)
	if e != nil {
		return nil, e
	}
	return &NativeStorageOwner{p}, nil
}
func (p *NativeStorageOwner) Observe() (sessions.StorageSnapshot, error) {
	a, o, e := p.owner.authenticate()
	if e != nil {
		return sessions.StorageSnapshot{}, e
	}
	return a.ObserveStorage(o)
}
func (p *NativeStorageOwner) Experiment(destination, mode string) (store.NativeStorageExperiment, error) {
	var out store.NativeStorageExperiment
	before, e := p.Observe()
	if e != nil {
		return out, e
	}
	w, e := nativeconfig.ResolveWorkspace(p.owner.config.Workspace, "")
	if e != nil {
		return out, sessions.ErrDenied
	}
	source, e := store.ReadNativeStorageSource(w, before.PrivateBytes)
	if e != nil {
		return out, e
	}
	out, e = store.NativeStorageProjection(w, source, destination, mode)
	if e != nil {
		return out, e
	}
	after, e := p.Observe()
	if e != nil {
		return out, e
	}
	fresh, e := store.ReadNativeStorageSource(w, after.PrivateBytes)
	if e != nil {
		return out, e
	}
	if before.Digest != after.Digest || store.NativeStorageSourceDigest(source) != store.NativeStorageSourceDigest(fresh) {
		return out, sessions.ErrReconcile
	}
	return out, nil
}
func (p *NativeStorageOwner) Retention(project string) (store.NativeRetentionPlan, error) {
	var out store.NativeRetentionPlan
	before, e := p.Observe()
	if e != nil {
		return out, e
	}
	w, e := nativeconfig.ResolveWorkspace(p.owner.config.Workspace, "")
	if e != nil {
		return out, sessions.ErrDenied
	}
	native := store.NativeRetentionAuthority{Complete: true, Digest: before.Digest}
	for _, ref := range before.References {
		native.References = append(native.References, store.NativeRetentionReference{TaskID: ref.TaskID, Digest: ref.Digest, Kind: ref.Kind})
	}
	for _, hold := range before.Holds {
		native.Holds = append(native.Holds, store.NativeRetentionHold{TaskID: hold.TaskID, Reason: hold.Reason})
	}
	out, e = store.PlanNativeRetention(w, project, native)
	if e != nil {
		return out, e
	}
	after, e := p.Observe()
	if e != nil {
		return out, e
	}
	if before.Digest != after.Digest {
		return out, sessions.ErrReconcile
	}
	return out, nil
}
