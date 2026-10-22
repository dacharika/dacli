package application

import (
	"bytes"
	"errors"
	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"os"
	"testing"
	"time"
)

func TestPolicyObservationCanonicalBindingAndReadOnly(t *testing.T) {
	f, _, d := nativePolicyFixture(t, false)
	s, token, e := f.a.Enroll(f.owner, sessions.Scope{Actions: []string{"read"}, Resources: []string{"workspace:" + f.w.ID, "task:" + f.task.ID}}, time.Now().Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(f.task.Path)
	out, e := ObserveNativePolicy(f.w, f.a, token, s.ID, "p", f.task.ID, d.ID)
	if e != nil || out.State != "allowed" || out.EffectAuthorized || out.Decision == nil {
		t.Fatal("canonical policy read failed", out, e)
	}
	after, _ := os.ReadFile(f.task.Path)
	if !bytes.Equal(before, after) {
		t.Fatal("policy read changed canonical task")
	}
	if _, e = ObserveNativePolicy(f.w, f.a, token, s.ID, "wrong", f.task.ID, d.ID); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("canonical wrong project accepted", e)
	}
	if _, e = ObserveNativePolicy(f.w, f.a, token, s.ID, "p", "001", d.ID); !errors.Is(e, sessions.ErrDenied) {
		t.Fatal("task alias substituted for canonical identity", e)
	}
}
