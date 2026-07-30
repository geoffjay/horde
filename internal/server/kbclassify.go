package server

// kbClassifyAction is the convergence action for one path from the three-way
// comparison (KSP §5.1). A read-only participant executes the clean rows and
// handles the dirty rows by preserving the local file to the conflict area
// (KSP §5.2).
type kbClassifyAction int

const (
	// kbActNone: A=S=D — in sync, nothing to do.
	kbActNone kbClassifyAction = iota
	// kbActPull: remote change, clean local — pull the file, set S=A.
	kbActPull
	// kbActPullNew: new upstream file — pull, set S=A (same as Pull but the
	// record doesn't exist yet, so there's no S to compare).
	kbActPullNew
	// kbActDeleteLocal: deleted upstream, clean local — delete the local file,
	// drop the sync record.
	kbActDeleteLocal
	// kbActConflict: D ≠ S — a local edit that conflicts with remote state
	// (concurrent edit, or edited-locally-then-deleted-upstream, or an
	// untracked local file at a path that exists upstream). A non-pushing
	// node preserves to the conflict area, then converges to canonical.
	kbActConflict
	// kbActPush: local edit only, clean upstream — push If-Match: S. Push path
	// only; a non-pushing node treats this as a conflict (preserve, then
	// converge).
	kbActPush
	// kbActPushNew: new local file — push If-None-Match: *. Push path only;
	// a non-pushing node treats this as a conflict.
	kbActPushNew
	// kbActPushDelete: deleted locally, exists upstream — push DELETE
	// If-Match: S. Push path only; a non-pushing node treats this as a
	// conflict (preserve nothing to delete, but the missing local file means
	// the authority's version is canonical, so pull it).
	kbActPushDelete
	// kbActDropRecord: the authority deleted the file and the node already
	// removed it from disk, but a stale sync record remains. Drop the record
	// without touching the filesystem (KSP §5.1: A=—, S=x, D=—).
	kbActDropRecord
)

// kbClassifyResult is the three-way classification for one path.
type kbClassifyResult struct {
	Action kbClassifyAction
	// AuthorityDigest is A: the authority's entry digest, or "" if absent.
	AuthorityDigest string
	// SyncedDigest is S: this node's synced_digest, or "" if never synced.
	SyncedDigest string
	// DiskDigest is D: the on-disk digest, or "" if the file doesn't exist.
	DiskDigest string
}

// classifyPath is the pure three-way comparison from KSP §5.1. It takes the
// authority's entry digest (A), the node's synced_digest (S), and the on-disk
// digest (D), and returns the convergence action. Empty string = absent.
//
// This is the correctness core of KSP — it is a pure function, exhaustively
// covered by the table-driven test. A non-pushing caller maps push actions to
// conflicts (KSP §5.2: a read-only participant does not push).
//
//nolint:gocyclo // exhaustive KSP §5.1 table — one case per row
func classifyPath(authority, synced, disk string) kbClassifyResult {
	a := authority != "" // authority has the path
	s := synced != ""    // node has a sync record
	d := disk != ""      // file exists on disk

	// Helper to check if two digests match (both present and equal).
	match := func(x, y string) bool { return x != "" && x == y }

	switch {
	// A=x, S=x, D=x — in sync
	case a && s && d && match(authority, synced) && match(synced, disk):
		return kbClassifyResult{Action: kbActNone, AuthorityDigest: authority, SyncedDigest: synced, DiskDigest: disk}

	// A=y, S=x, D=x — remote change, clean local (S=D≠A)
	case a && s && d && match(synced, disk) && authority != synced:
		return kbClassifyResult{Action: kbActPull, AuthorityDigest: authority, SyncedDigest: synced, DiskDigest: disk}

	// A=x, S=x, D=z — local edit only (A=S≠D)
	case a && s && d && match(authority, synced) && disk != synced:
		return kbClassifyResult{Action: kbActPush, AuthorityDigest: authority, SyncedDigest: synced, DiskDigest: disk}

	// A=y, S=x, D=y — local edit matches the new upstream (authority == disk).
	// The local change converges to the same content as the remote, so this
	// is a clean pull: set S=A, the file already matches (no-op write).
	case a && s && d && match(authority, disk):
		return kbClassifyResult{Action: kbActPull, AuthorityDigest: authority, SyncedDigest: synced, DiskDigest: disk}
	// A=y, S=x, D=z — concurrent remote + local change
	case a && s && d && authority != synced && disk != synced && !match(synced, disk):
		return kbClassifyResult{Action: kbActConflict, AuthorityDigest: authority, SyncedDigest: synced, DiskDigest: disk}

	// A=—, S=x, D=x — deleted upstream, clean local
	case !a && s && d && match(synced, disk):
		return kbClassifyResult{Action: kbActDeleteLocal, AuthorityDigest: "", SyncedDigest: synced, DiskDigest: disk}

	// A=—, S=x, D=z — deleted upstream, edited locally
	case !a && s && d && disk != synced:
		return kbClassifyResult{Action: kbActConflict, AuthorityDigest: "", SyncedDigest: synced, DiskDigest: disk}

	// A=x, S=x, D=— — deleted locally
	case a && s && !d && match(authority, synced):
		return kbClassifyResult{Action: kbActPushDelete, AuthorityDigest: authority, SyncedDigest: synced, DiskDigest: ""}

	// A=y, S=x, D=— — deleted locally, changed upstream. The file was
	// deleted locally but the authority's version changed; pull the new
	// version (re-materialize from authority). Without this case the path
	// falls through to default → none, never converging.
	case a && s && !d && authority != synced:
		return kbClassifyResult{Action: kbActPull, AuthorityDigest: authority, SyncedDigest: synced, DiskDigest: ""}

		// A=x, S=—, D=z — exists upstream, untracked local file. If the content
		// matches, record it as synced (no-op write); otherwise it's a conflict.
	case a && !s && d && match(authority, disk):
		return kbClassifyResult{Action: kbActPullNew, AuthorityDigest: authority, SyncedDigest: "", DiskDigest: disk}
	case a && !s && d:
		return kbClassifyResult{Action: kbActConflict, AuthorityDigest: authority, SyncedDigest: "", DiskDigest: disk}

	// A=—, S=—, D=z — new local file
	// A=—, S=x, D=— — deleted upstream and locally; stale sync record remains.
	// Drop the record without touching the filesystem.
	case !a && s && !d:
		return kbClassifyResult{Action: kbActDropRecord, AuthorityDigest: "", SyncedDigest: synced, DiskDigest: ""}

	case !a && !s && d:
		return kbClassifyResult{Action: kbActPushNew, AuthorityDigest: "", SyncedDigest: "", DiskDigest: disk}

	// A=y, S=—, D=— — new upstream file
	case a && !s && !d:
		return kbClassifyResult{Action: kbActPullNew, AuthorityDigest: authority, SyncedDigest: "", DiskDigest: ""}

	// A=—, S=—, D=— — nothing exists anywhere (shouldn't be called)
	default:
		return kbClassifyResult{Action: kbActNone, AuthorityDigest: "", SyncedDigest: "", DiskDigest: ""}
	}
}

// classifyStage1 maps the raw three-way result to an action for a
// non-pushing node. A read-only participant does not push (KSP §5.2): any
// push action becomes a conflict (preserve the local file, then converge to
// canonical). The only actions it executes are: none, pull (including
// pull-new), delete-local, and conflict (preserve).
func classifyStage1(authority, synced, disk string) kbClassifyResult {
	r := classifyPath(authority, synced, disk)
	switch r.Action {
	case kbActPush, kbActPushNew:
		// A local edit a non-pushing node cannot propagate. Preserve to
		// the conflict area, then pull the authority's version (KSP §5.2).
		r.Action = kbActConflict
	case kbActPushDelete:
		// Deleted locally but exists upstream. A non-pushing node: the
		// authority's version is canonical — pull it back (re-materialize).
		r.Action = kbActPull
	}
	return r
}
