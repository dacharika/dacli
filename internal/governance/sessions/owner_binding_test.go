package sessions

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestOwnerTaskBindingRequiresOwnerAndPreservesAdmission(t *testing.T) {
	a, owner := setup(t)
	want := TaskBinding{TaskID: "task", ProjectID: "project", Repository: t.TempDir(), AdmissionEnabled: false}
	if err := a.BindTask(owner, want); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"", strings.Repeat("0", 64)} {
		got, err := a.OwnerTaskBinding(credential, want.TaskID)
		if !errors.Is(err, ErrDenied) || got != (TaskBinding{}) {
			t.Fatalf("non-owner read binding: %+v %v", got, err)
		}
	}
	if got, err := a.OwnerTaskBinding(owner, "missing"); !errors.Is(err, ErrDenied) || got != (TaskBinding{}) {
		t.Fatalf("missing binding returned: %+v %v", got, err)
	}
	got, err := a.OwnerTaskBinding(owner, want.TaskID)
	if err != nil || got != want {
		t.Fatalf("owner observation changed disabled admission: %+v %v", got, err)
	}
	after, err := os.ReadFile(a.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("binding observation rewrote authority", err)
	}
	if err := a.BindTask(owner, TaskBinding{TaskID: want.TaskID, ProjectID: want.ProjectID, Repository: want.Repository, AdmissionEnabled: true}); err != nil {
		t.Fatal(err)
	}
	got, err = a.OwnerTaskBinding(owner, want.TaskID)
	if err != nil || !got.AdmissionEnabled {
		t.Fatalf("owner observation cached old admission: %+v %v", got, err)
	}
}
