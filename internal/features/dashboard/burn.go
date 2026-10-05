package dashboard

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mlnomadpy/dacli/internal/governance/sessions"
	"github.com/mlnomadpy/dacli/internal/model"
	"github.com/mlnomadpy/dacli/internal/store"
	"github.com/mlnomadpy/dacli/internal/workspace"
)

// AlertFactor is the multiple of the calibrated band expectation at which the
// burn chart stops being a passive line and YELLS (per docs/research: "make the
// chart yell"). A burn rate at or above 1.5× the norm is the overspend signal
// all four discovery segments asked to catch before it becomes a silent,
// expensive failure. Exported so the SPA and the handler test can agree on the
// one threshold instead of hard-coding 1.5 twice.
const AlertFactor = 1.5

// burnView is the /api/burn payload: the honest token/cost burn story assembled
// from data DACLI already records — the per-day run actuals (usage.txt under
// RunsDir, timestamped by the run's ULID name), the calibrated role×model×runtime
// bands (store.CalibrationSamples) that set the expected per-run cost, and the
// live governor window spend (loop/*-governor.txt). Nothing here is invented:
// a workspace with no usage-bearing runs yields an empty series and a zero
// ceiling, and the chart says so rather than fabricating a line.
type burnView struct {
	Native []sessions.DashboardSnapshot `json:"native,omitempty"`
	// Unit names what Series/Ceiling/Rate are measured in — output tokens, the
	// unit calibration prefers over the wall-clock proxy (store.CalibSample).
	Unit string `json:"unit"`
	// Series is the per-day burn, oldest→newest (already sorted; never re-sort
	// client-side). Each point sums every usage-bearing IMPLEMENTER run that
	// started that day — verify-panel seats and review-role runs are excluded so
	// the per-run intensity is measured over the SAME population the Ceiling is
	// calibrated over (see burnSeries).
	Series       []burnPoint `json:"series"`
	HiddenPoints int         `json:"hidden_points"`
	// Bands is the calibrated per-agent expectation the ceiling is drawn from:
	// role×model×runtime → median output tokens per run, with the sample count.
	Bands []burnBand `json:"bands"`
	// Windows is the live governor spend per project that has a persisted loop
	// state — windowSpent charged against the current budget window.
	Windows []burnWindow `json:"windows"`
	// Ceiling is the calibrated per-run norm: the median output tokens across
	// every token-bearing calibration sample. 0 when there is no token history,
	// in which case Alert is always false (nothing to compare against).
	Ceiling float64 `json:"ceiling"`
	// Rate is the current burn intensity: output tokens per IMPLEMENTER run on
	// the most recent day in Series (verify-panel seats and review-role runs
	// excluded, matching the Ceiling's population). 0 when the series is empty.
	Rate float64 `json:"rate"`
	// Ratio is Rate/Ceiling — how many times the calibrated norm the latest day
	// is burning. 0 when Ceiling is 0.
	Ratio float64 `json:"ratio"`
	// Alert is Ratio ≥ AlertFactor (and Ceiling > 0): the chart yells.
	Alert bool `json:"alert"`
	// AlertAt echoes AlertFactor so the client thresholds against the same number
	// the server did rather than duplicating the constant.
	AlertAt float64 `json:"alert_at"`
}

// burnPoint is one day of burn: the output tokens and USD cost summed across
// every usage-bearing IMPLEMENTER run that started that day (verify-panel seats
// and review-role runs excluded), the run count, and the derived per-run
// intensity (tokens/runs, 0-safe).
type burnPoint struct {
	Day    string   `json:"day"` // YYYY-MM-DD, UTC
	Tokens int64    `json:"tokens"`
	Cost   float64  `json:"cost_usd"`
	Runs   int      `json:"runs"`
	RunIDs []string `json:"run_ids"`
	PerRun float64  `json:"per_run"`
}

// burnBand is one calibrated agent band's per-run token expectation. Calibrated
// mirrors the n≥10 gate every other band readout in the store uses before its
// number counts as authoritative.
type burnBand struct {
	Band       string  `json:"band"` // role/model/runtime
	Role       string  `json:"role"`
	Expected   float64 `json:"expected"` // median output tokens per run
	N          int     `json:"n"`
	Calibrated bool    `json:"calibrated"` // n >= 10
}

// burnWindow is one project's live governor spend: tokens charged against the
// current rolling budget window, and when that window began. Read straight from
// the persisted governor snapshot (loop/<project>-governor.txt) — the same file
// a restarted loop reloads. Empty Start means no window has opened yet.
type burnWindow struct {
	Project string `json:"project"`
	Spent   int64  `json:"spent"`
	Start   string `json:"start"` // RFC3339 UTC, or ""
}

// buildBurn assembles the burn view fresh on every call (no cache, the same
// honesty rule buildState follows). It reads three sources DACLI already
// records and never mutates the workspace.
func buildBurn(w *workspace.Workspace) (burnView, error) {
	v := burnView{Unit: "output_tokens", AlertAt: AlertFactor}

	rawSeries := burnSeries(w)
	v.Series, v.HiddenPoints = downsampleBurn(rawSeries, 90)
	if n := len(v.Series); n > 0 {
		v.Rate = v.Series[n-1].PerRun
	}

	samples := store.CalibrationSamples(w)
	v.Bands = burnBands(samples)
	v.Ceiling = calibratedCeiling(samples)
	if v.Ceiling > 0 {
		v.Ratio = v.Rate / v.Ceiling
		v.Alert = v.Ratio >= AlertFactor
	}

	v.Windows = burnWindows(w)
	return v, nil
}

func downsampleBurn(points []burnPoint, limit int) ([]burnPoint, int) {
	if limit < 4 || len(points) <= limit {
		return points, 0
	}
	keep := map[int]bool{0: true, len(points) - 1: true}
	minIndex, maxIndex := 0, 0
	for i := range points {
		if points[i].PerRun < points[minIndex].PerRun {
			minIndex = i
		}
		if points[i].PerRun > points[maxIndex].PerRun {
			maxIndex = i
		}
	}
	keep[minIndex], keep[maxIndex] = true, true
	for step := 1; step < limit-1 && len(keep) < limit; step++ {
		keep[int(float64(step)*float64(len(points)-1)/float64(limit-1))] = true
	}
	for index := 1; index < len(points)-1 && len(keep) < limit; index++ {
		keep[index] = true
	}
	indexes := make([]int, 0, len(keep))
	for index := range keep {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	out := make([]burnPoint, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, points[index])
	}
	return out, len(points) - len(out)
}

// burnSeries walks RunsDir once, reads each run's usage.txt, and buckets the
// output-token and USD actuals by the run's start DAY (decoded from its ULID
// directory name). Runs with no usage — a text runtime, or a run that never
// finished — contribute nothing, the same honest degrade calibration applies.
// The result is sorted chronologically by day.
//
// The population is the completing/IMPLEMENTER runs only — verify-panel seats
// and review-role runs are skipped. This is the fix for the false-negative
// overspend yell: the Ceiling is the median per-run cost across store's
// calibration samples, and those samples are exactly the implementer runs of
// done tasks (store.runRecords drops verify-panel seats, and a reviewer — grant
// ro, "never implements" — does not complete tasks so never becomes a sample).
// If the per-run Rate were instead averaged over EVERY usage-bearing run, a day
// with one genuine over-ceiling implementer run plus several cheap review/verify
// runs would dilute below AlertFactor and silently swallow the overspend. So the
// Rate must be counted over the SAME population the Ceiling is built from.
func burnSeries(w *workspace.Workspace) []burnPoint {
	entries, err := os.ReadDir(w.RunsDir())
	if err != nil {
		return nil
	}
	reviewers := reviewerRoles(w)
	byDay := map[string]*burnPoint{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(w.RunsDir(), e.Name())
		role, isVerify := readRunRole(runDir)
		if isVerify || reviewers[role] {
			continue // a check, not implementer work: not in the ceiling's population
		}
		u, ok := readRunUsage(runDir)
		if !ok {
			continue
		}
		t, ok := ulidTime(e.Name())
		if !ok {
			continue
		}
		day := t.UTC().Format("2006-01-02")
		p := byDay[day]
		if p == nil {
			p = &burnPoint{Day: day}
			byDay[day] = p
		}
		p.Tokens += int64(u.output)
		p.Cost += u.cost
		p.Runs++
		if len(p.RunIDs) < analyticsEvidenceLimit {
			p.RunIDs = append(p.RunIDs, e.Name())
		}
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)
	out := make([]burnPoint, 0, len(days))
	for _, d := range days {
		p := byDay[d]
		if p.Runs > 0 {
			p.PerRun = float64(p.Tokens) / float64(p.Runs)
		}
		out = append(out, *p)
	}
	return out
}

// runUsage is the subset of a run's usage.txt the burn view reads. Duplicated
// from store.Usage rather than imported piecemeal so this slice stays self-
// contained; the parse mirrors store.readUsage.
type runUsage struct {
	output int
	cost   float64
}

// readRunUsage parses a run dir's usage.txt. ok is false when the file is absent
// (a text runtime) or carries no output-token count — the same rule
// store.readUsage applies, so the burn series and calibration agree on which
// runs "count."
func readRunUsage(runDir string) (runUsage, bool) {
	f, err := os.Open(filepath.Join(runDir, "usage.txt"))
	if err != nil {
		return runUsage{}, false
	}
	defer func() { _ = f.Close() }()
	var u runUsage
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, val, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(k) {
		case "output_tokens":
			u.output, _ = strconv.Atoi(val)
		case "cost_usd":
			u.cost, _ = strconv.ParseFloat(val, 64)
		}
	}
	return u, u.output > 0
}

// readRunRole reads a run dir's invocation.txt for the two fields the burn
// population filter needs: the role that produced the run and whether the run is
// a verify-panel seat. Duplicated (not imported from store.readInvocation) for
// the same slice-isolation reason burnWindows duplicates the governor parse — a
// feature slice may not reach into another. A missing invocation.txt yields an
// empty role and isVerify=false, so an early or taskless run counts as
// implementer work rather than being silently dropped from the burn.
func readRunRole(runDir string) (role string, isVerify bool) {
	f, err := os.Open(filepath.Join(runDir, "invocation.txt"))
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "role":
			role = v
		case "verify_panel_seat":
			isVerify = true
		}
	}
	return role, isVerify
}

// reviewerRoles is the set of role names the burn Rate must exclude so it
// counts the same implementer population the Ceiling is calibrated over.
// Built from the workspace's role files (store.LoadRoles), a role is excluded
// when EITHER its calibrated kind is "reviewer" OR its grant is read-only
// (model.GrantRO): an ro-grant role "never implements" by construction — it
// cannot complete a task and so never becomes a calibration sample — so
// counting its runs in the Rate would dilute against a population the Ceiling
// was never built from, regardless of whether the role file also happens to
// carry role_kind: reviewer. This closes the gap where a read-only role file
// that omits role_kind (e.g. an unbackfilled reviewer.md) would otherwise
// slip through the Kind-only check and dilute the yell.
func reviewerRoles(w *workspace.Workspace) map[string]bool {
	roles, err := store.LoadRoles(w)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, r := range roles {
		if r.Kind == "reviewer" || r.Grant == string(model.GrantRO) {
			out[r.Name] = true
		}
	}
	return out
}

// crockford is the Crockford base32 alphabet ulid.At encodes with. Duplicated
// (not imported) because the ulid package intentionally exposes only At/Valid —
// decoding a ULID's millisecond timestamp is a dashboard concern, not a change
// to the zero-dependency ULID kernel.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ulidTime decodes the millisecond timestamp from a ULID's first 10 base32
// characters — the inverse of ulid.At's timestamp half. ok is false when the
// string is too short or carries a non-alphabet character (a run dir that is not
// a ULID), in which case the run is skipped rather than bucketed to the epoch.
func ulidTime(id string) (time.Time, bool) {
	if len(id) < 10 {
		return time.Time{}, false
	}
	var ms uint64
	for i := 0; i < 10; i++ {
		idx := strings.IndexByte(crockford, id[i])
		if idx < 0 {
			return time.Time{}, false
		}
		ms = ms<<5 | uint64(idx)
	}
	return time.UnixMilli(int64(ms)).UTC(), true
}

// burnBands groups the calibration samples that carry a real token actual by
// their agent band and reports each band's median output tokens per run. Bands
// are sorted by descending expected cost so the hungriest agent leads. A band
// with no token-bearing sample is invisible — a hole in the data, not a zero.
func burnBands(samples []store.CalibSample) []burnBand {
	byBand := map[store.Band][]float64{}
	for _, s := range samples {
		if s.HasTokens() {
			byBand[s.Band] = append(byBand[s.Band], float64(s.Tokens))
		}
	}
	out := make([]burnBand, 0, len(byBand))
	for band, tokens := range byBand {
		out = append(out, burnBand{
			Band:       band.String(),
			Role:       band.Role,
			Expected:   median(tokens),
			N:          len(tokens),
			Calibrated: len(tokens) >= calibrationGate,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Expected != out[j].Expected {
			return out[i].Expected > out[j].Expected
		}
		return out[i].Band < out[j].Band
	})
	return out
}

// calibrationGate is the n≥10 threshold every band readout in the store uses
// before its distribution counts as authoritative (store.MedianTokenRatio's
// contract). Mirrored here so a band the burn view labels "calibrated" is the
// same one the estimator would trust.
const calibrationGate = 10

// calibratedCeiling is the workspace's per-run token norm: the median output
// tokens across EVERY token-bearing calibration sample, regardless of band. It
// is the ceiling the current burn rate is measured against — 0 when no run has
// ever reported usage, so the caller raises no alert against an absent norm.
func calibratedCeiling(samples []store.CalibSample) float64 {
	var tokens []float64
	for _, s := range samples {
		if s.HasTokens() {
			tokens = append(tokens, float64(s.Tokens))
		}
	}
	return median(tokens)
}

// median returns the median of xs (linear interpolation on the two middle
// elements for an even count), or 0 for an empty slice — the 0-safe convention
// the rest of the burn view degrades to.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// burnWindows reads every persisted governor snapshot under the loop dir and
// reports each project's live window spend. The dashboard slice cannot import
// the orchestration slice (feature slices never import each other — arch_test),
// so the parse of loop/<project>-governor.txt is duplicated here, exactly as
// completionDay and liveAgents duplicate their orchestration/execution twins.
func burnWindows(w *workspace.Workspace) []burnWindow {
	dir := filepath.Join(w.Root, workspace.Dir, "loop")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []burnWindow
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "-governor.txt") {
			continue
		}
		project := strings.TrimSuffix(e.Name(), "-governor.txt")
		bw := burnWindow{Project: project}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			k, val, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			val = strings.TrimSpace(val)
			switch k {
			case "window_spent":
				bw.Spent, _ = strconv.ParseInt(val, 10, 64)
			case "window_start":
				if t, err := time.Parse(time.RFC3339, val); err == nil && !t.IsZero() {
					bw.Start = t.UTC().Format(time.RFC3339)
				}
			}
		}
		out = append(out, bw)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}
