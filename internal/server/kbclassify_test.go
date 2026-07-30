package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClassifyPath_AllRows is the exhaustive table-driven test over the KSP
// §5.1 three-way comparison. Every row of the normative table is covered.
// This is the correctness core of KSP convergence.
func TestClassifyPath_AllRows(t *testing.T) {
	// Digests: x = original, y = remote-change, z = local-edit
	// "" = absent (no entry / no file / no record)
	const x, y, z = "sha256:x", "sha256:y", "sha256:z"

	tests := []struct {
		name string
		a    string // authority digest (A)
		s    string // synced digest (S)
		d    string // on-disk digest (D)
		want kbClassifyAction
	}{
		// In sync
		{"A=x S=x D=x → none", x, x, x, kbActNone},

		// Remote change, clean local
		{"A=y S=x D=x → pull", y, x, x, kbActPull},

		// Local edit only (push path: push)
		{"A=x S=x D=z → push", x, x, z, kbActPush},

		// Concurrent remote + local change
		{"A=y S=x D=z → conflict", y, x, z, kbActConflict},

		// Deleted upstream, clean local
		{"A=— S=x D=x → delete-local", "", x, x, kbActDeleteLocal},

		// Deleted upstream, edited locally
		{"A=— S=x D=z → conflict", "", x, z, kbActConflict},

		// Deleted locally (push path: push-delete)
		{"A=x S=x D=— → push-delete", x, x, "", kbActPushDelete},
		// Deleted locally, changed upstream → pull (re-materialize)
		{"A=y S=x D=— → pull", y, x, "", kbActPull},

		// Exists upstream, untracked local file
		{"A=x S=— D=z → conflict", x, "", z, kbActConflict},

		// New local file (push path: push-new)
		{"A=— S=— D=z → push-new", "", "", z, kbActPushNew},

		// New upstream file
		{"A=y S=— D=— → pull-new", y, "", "", kbActPullNew},

		// Nothing anywhere (shouldn't be called, but must not panic)
		{"A=— S=— D=— → none", "", "", "", kbActNone},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := classifyPath(tc.a, tc.s, tc.d)
			assert.Equal(t, tc.want, r.Action, "action mismatch")
		})
	}
}

// TestClassifyStage1_MapsPushToConflict verifies that a non-pushing node maps
// push actions to conflicts (KSP §5.2: a read-only participant does not push).
func TestClassifyStage1_MapsPushToConflict(t *testing.T) {
	const x, z = "sha256:x", "sha256:z"

	tests := []struct {
		name       string
		a, s, d    string
		rawWant    kbClassifyAction
		stage1Want kbClassifyAction
	}{
		{"local edit → conflict", x, x, z, kbActPush, kbActConflict},
		{"new local file → conflict", "", "", z, kbActPushNew, kbActConflict},
		{"deleted locally → pull-back", x, x, "", kbActPushDelete, kbActPull},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := classifyPath(tc.a, tc.s, tc.d)
			assert.Equal(t, tc.rawWant, raw.Action, "raw classification")

			stage1 := classifyStage1(tc.a, tc.s, tc.d)
			assert.Equal(t, tc.stage1Want, stage1.Action, "non-pushing classification")
		})
	}
}

// TestClassifyStage1_CleanRowsUnchanged verifies the non-push rows are
// identical between raw and non-pushing classification.
func TestClassifyStage1_CleanRowsUnchanged(t *testing.T) {
	const x, y, z = "sha256:x", "sha256:y", "sha256:z"

	tests := []struct {
		a, s, d string
		want    kbClassifyAction
	}{
		{x, x, x, kbActNone},
		{y, x, x, kbActPull},
		{y, x, "", kbActPull}, // deleted locally, changed upstream
		{y, x, z, kbActConflict},
		{"", x, x, kbActDeleteLocal},
		{"", x, z, kbActConflict},
		{x, "", z, kbActConflict},
		{y, "", "", kbActPullNew},
	}

	for _, tc := range tests {
		raw := classifyPath(tc.a, tc.s, tc.d)
		stage1 := classifyStage1(tc.a, tc.s, tc.d)
		assert.Equal(t, raw.Action, stage1.Action,
			"non-pushing should not remap non-push rows (A=%q S=%q D=%q)", tc.a, tc.s, tc.d)
	}
}

// TestClassifyStage2_PushRows verifies that a pushing node (stage 2, WatchLocal
// enabled) executes the push rows directly — the raw classifyPath result with
// no remapping. A local edit pushes (If-Match: S), a new local file pushes
// (If-None-Match: *), and a deleted-locally file pushes a DELETE (If-Match: S).
// This is the symmetric multi-writer path (KSP §5.1, §11).
func TestClassifyStage2_PushRows(t *testing.T) {
	const x, y, z = "sha256:x", "sha256:y", "sha256:z"

	tests := []struct {
		name    string
		a, s, d string
		want    kbClassifyAction
	}{
		// Clean rows — same as stage 1.
		{"in sync", x, x, x, kbActNone},
		{"remote change, clean local", y, x, x, kbActPull},
		{"new upstream file", y, "", "", kbActPullNew},
		{"deleted upstream, clean local", "", x, x, kbActDeleteLocal},
		{"stale record", "", x, "", kbActDropRecord},

		// Push rows — the stage 2 difference: these are executed, not
		// remapped to conflicts.
		{"local edit only → push", x, x, z, kbActPush},
		{"new local file → push-new", "", "", z, kbActPushNew},
		{"deleted locally → push-delete", x, x, "", kbActPushDelete},

		// Conflict rows — still conflicts in stage 2.
		{"concurrent remote + local change → conflict", y, x, z, kbActConflict},
		{"deleted upstream, edited locally → conflict", "", x, z, kbActConflict},
		{"exists upstream, untracked local → conflict", x, "", z, kbActConflict},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := classifyStage2(tc.a, tc.s, tc.d)
			assert.Equal(t, tc.want, r.Action, "pushing classification")
		})
	}
}

// TestClassifyStage2_EqualsRaw verifies classifyStage2 is the identity of
// classifyPath — a pushing node executes the raw three-way comparison with no
// remapping (KSP §11, stage 2).
func TestClassifyStage2_EqualsRaw(t *testing.T) {
	// All combinations of {absent, x, y, z} for A, S, D.
	digests := []string{"", "sha256:x", "sha256:y", "sha256:z"}
	for _, a := range digests {
		for _, s := range digests {
			for _, d := range digests {
				raw := classifyPath(a, s, d)
				stage2 := classifyStage2(a, s, d)
				assert.Equal(t, raw.Action, stage2.Action,
					"classifyStage2 must equal raw classifyPath (A=%q S=%q D=%q)", a, s, d)
			}
		}
	}
}
