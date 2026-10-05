package procmon_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mlnomadpy/dacli/internal/procmon"
)

func TestReadRetainedRecordRejectsMalformedIdentity(t *testing.T) {
	for name, body := range map[string]string{
		"active-zero": "child: a-owner\npid: 0\n", "missing-child": "pid: 123\n", "missing-pid": "child: a-owner\n",
		"bad-pid":          "child: a-owner\npid: not-a-pid\n",
		"negative-pid":     "child: a-owner\npid: -1\n",
		"bad-pgid":         "child: a-owner\npid: 123\npgid: invalid\n",
		"bad-started":      "child: a-owner\npid: 123\nstarted: invalid\n",
		"bad-timeout":      "child: a-owner\npid: 123\ntimeout_s: invalid\n",
		"overflow-timeout": "child: a-owner\npid: 123\ntimeout_s: 9223372036854775807\n",
		"duplicate-pid":    "child: a-owner\npid: 123\npid: 0\n",
		"duplicate-owner":  "child: a-owner\nchild: a-other\npid: 123\n",
		"junk":             "child: a-owner\npid: 123\nbroken line\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "proc.txt")
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := procmon.ReadRetainedRecord(path)
			if err == nil || !reflect.DeepEqual(got, procmon.Record{}) {
				t.Fatalf("malformed retained identity must be an error with no record: %+v, %v", got, err)
			}
			if _, err := procmon.ReadRecord(path); err != nil {
				t.Fatalf("strict reader changed permissive diagnostic behavior: %v", err)
			}
		})
	}
}

func TestReadRetainedRecordPreservesLegacyAndWrittenRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proc.txt")
	legacy := "child: a-owner\npid: 123\nunknown_future_field: supported\n"
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := procmon.ReadRetainedRecord(path)
	if err != nil || got.Child != "a-owner" || got.PID != 123 || got.PIDStart != "" {
		t.Fatalf("valid legacy record refused: %+v %v", got, err)
	}
	for _, want := range []procmon.Record{
		{RunID: "retained", Child: "a-owner", Task: "t-task", PID: 123, PGID: 123, PIDStart: "Mon Aug  3 09:15:00 2026", Started: time.Now().UTC().Truncate(time.Second), Timeout: 10 * time.Second, Claims: []string{"src"}},
		{RunID: "finished", Child: "a-owner", Outcome: "completed"},
	} {
		if err := procmon.WriteRecord(path, want); err != nil {
			t.Fatal(err)
		}
		got, err = procmon.ReadRetainedRecord(path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("valid written record changed: %+v want %+v error %v", got, want, err)
		}
	}
	if _, err := procmon.ReadRetainedRecord(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Fatalf("missing optional proc should retain not-found classification: %v", err)
	}
	if _, err := procmon.ReadRetainedRecord(t.TempDir()); err == nil {
		t.Fatal("non-readable proc directory accepted")
	}
}
