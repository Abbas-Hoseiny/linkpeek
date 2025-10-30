package realtime

// SnapshotFunc adapts a function to the SnapshotProvider interface.
type SnapshotFunc func() (interface{}, error)

// Snapshot returns the value produced by the wrapped function.
func (f SnapshotFunc) Snapshot() (interface{}, error) {
	return f()
}
