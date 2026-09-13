package coresight

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrWalkLimit means an explicit traversal bound prevented further inspection.
	ErrWalkLimit = errors.New("coresight: ROM walk limit reached")
	// ErrRepeatedTable means a table was reached twice, through a cycle or duplicate reference.
	ErrRepeatedTable = errors.New("coresight: repeated ROM table")
	// ErrPowerDomain means a child was skipped because its entry names a power domain.
	ErrPowerDomain = errors.New("coresight: power domain access not established")
)

// WalkLimits bounds one entire walk. All limits are explicit; there are no defaults.
type WalkLimits struct {
	MaxDepth      int // Root depth is zero; zero permits only the root.
	MaxComponents int // Maximum visits, including the root and skipped children. Must be positive.
	MaxEntries    int // Maximum entry reads across all tables, including absent entries and terminators. Must be positive.
}

// Validate checks limits without traffic, for use before opening hardware.
func (l WalkLimits) Validate() error {
	if l.MaxDepth < 0 || l.MaxComponents <= 0 || l.MaxEntries <= 0 {
		return errors.New("coresight: require nonnegative depth and positive component and entry limits")
	}
	return nil
}

// Visit records one component reached in depth-first entry order. Parent indexes
// the returned slice; Parent and Index are -1 for the root, whose Entry is zero.
// Otherwise Index identifies the entry in the parent table. Component is nil
// when identity was not obtained. Err records a skipped or failed component;
// table entry read errors and limits are reported by Walk's returned error.
type Visit struct {
	Parent    int
	Index     int
	Entry     ROMEntry
	Component *Component
	Err       error
}

// Walk identifies root and follows present ROM entries within limits. It returns
// the visits recorded so far and a non-nil error whenever inspection is incomplete.
// Power-domain children are recorded with ErrPowerDomain without being accessed;
// their accessible siblings are still inspected. Any other error stops all reads,
// preserving the underlying error. Repeated tables are rejected before rereading.
// A non-table root is a successful single visit. Unknown component architectures
// are leaves; recognized ROM tables with unsupported formats fail.
//
// The caller supplies a safe root address and retains reader ownership. Walk
// writes no target memory, requests no component power, and performs no unlocks.
// Walk uses an explicit stack; limits bound visits, entry reads, and hierarchy depth.
func Walk(ctx context.Context, reader ScalarReader, base uint64, limits WalkLimits) ([]Visit, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil || reader == nil || base&0xfff != 0 {
		return nil, errors.New("coresight: invalid walk context, reader, or root address")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := walker{ctx: ctx, reader: reader, limits: limits, seen: map[uint64]bool{}}
	err := w.inspect(base, Visit{Parent: -1, Index: -1}, 0)
	for err == nil && len(w.stack) > 0 {
		err = w.next()
	}
	return w.visits, errors.Join(append(w.skipped, err)...)
}

type walkFrame struct {
	table               ROMTable
	parent, next, depth int
}

type walker struct {
	ctx     context.Context
	reader  ScalarReader
	limits  WalkLimits
	entries int
	visits  []Visit
	stack   []walkFrame
	seen    map[uint64]bool
	skipped []error
}

func (w *walker) next() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	top := len(w.stack) - 1
	frame := w.stack[top]
	if frame.next == frame.table.EntryCount() {
		w.stack = w.stack[:top]
		return nil
	}
	if w.entries == w.limits.MaxEntries {
		return fmt.Errorf("%w: entries at table %#x index %d", ErrWalkLimit, frame.table.base, frame.next)
	}
	w.entries++
	entry, err := frame.table.ReadEntry(w.ctx, w.reader, frame.next)
	if err != nil {
		return err
	}
	w.stack[top].next++
	if entry.End {
		w.stack = w.stack[:top]
		return nil
	}
	if !entry.Present {
		return nil
	}
	return w.inspect(entry.Base, Visit{Parent: frame.parent, Index: frame.next, Entry: entry}, frame.depth+1)
}

func (w *walker) inspect(base uint64, visit Visit, depth int) error {
	if depth > w.limits.MaxDepth || len(w.visits) == w.limits.MaxComponents {
		return fmt.Errorf("%w: component %#x at depth %d", ErrWalkLimit, base, depth)
	}
	index := len(w.visits)
	w.visits = append(w.visits, visit)
	if visit.Entry.PowerIDValid {
		err := fmt.Errorf("component %#x power ID %d: %w", base, visit.Entry.PowerID, ErrPowerDomain)
		w.visits[index].Err = err
		w.skipped = append(w.skipped, err)
		return nil
	}
	err := w.identify(base, index, depth)
	if err != nil {
		w.visits[index].Err = err
	}
	return err
}

func (w *walker) identify(base uint64, index, depth int) error {
	if w.seen[base] {
		return fmt.Errorf("%w at %#x", ErrRepeatedTable, base)
	}
	component, err := Identify(w.ctx, w.reader, base)
	if err != nil {
		return err
	}
	w.visits[index].Component = &component
	table, err := component.ROMTable()
	if errors.Is(err, ErrNotROMTable) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("coresight: table %#x: %w", base, err)
	}
	w.seen[base] = true
	w.stack = append(w.stack, walkFrame{table: table, parent: index, depth: depth})
	return nil
}
