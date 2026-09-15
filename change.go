package editrig

// Change is one row of a record diff: the field and its values before and
// after, both in wire form (the shape they travel in JSON).
//
// The type lives in the CORE although the core never uses it: the diff is
// computed by the storage implementation, the only party holding both states
// of the row. Were every store to declare its own type, the application would
// write one bridge to its journal per store; with a shared type the bridge is
// one and independent of the store. The core writes no audit itself (see
// doc.go); it only provides the form in which a write is reported.
type Change struct {
	Field string
	Old   any
	New   any
}
