package networkengine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Compile-time check.
var _ CommissionRunStore = (*MemoryCommissionRunStore)(nil)

// MemoryCommissionRunStore is an in-memory CommissionRunStore for tests.
//
// It enforces in code what the Postgres store gets from the database. Today
// that is one active run per period, which Postgres gets from a partial
// unique index. Per-structure result replacement and id-ascending result
// order land with the results half in Task 4.
//
// The shared suite asserts the two implementations behave identically, so
// anything an index or a constraint enforces there has to be enforced here.
type MemoryCommissionRunStore struct {
	mu      sync.RWMutex
	runs    map[uuid.UUID]*CommissionRun
	results map[uuid.UUID][]CommissionResult // by run id, kept in id order
	nextID  int64
}

// NewMemoryCommissionRunStore returns an empty in-memory store.
func NewMemoryCommissionRunStore() *MemoryCommissionRunStore {
	return &MemoryCommissionRunStore{
		runs:    map[uuid.UUID]*CommissionRun{},
		results: map[uuid.UUID][]CommissionResult{},
		nextID:  1,
	}
}

// activeRunLocked returns the period's non-voided run. Callers hold the lock.
func (s *MemoryCommissionRunStore) activeRunLocked(periodID string) *CommissionRun {
	for _, r := range s.runs {
		if r.PeriodID == periodID && r.Status != RunStatusVoided {
			return r
		}
	}
	return nil
}

func (s *MemoryCommissionRunStore) CreateRun(_ context.Context, periodID, planHash string) (uuid.UUID, error) {
	if err := validateRunInput(periodID, planHash); err != nil {
		return uuid.Nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createRunLocked(periodID, planHash)
}

// createRunLocked mirrors the partial unique index on
// (period_id) WHERE status <> 'voided'. Callers hold the lock.
func (s *MemoryCommissionRunStore) createRunLocked(periodID, planHash string) (uuid.UUID, error) {
	if existing := s.activeRunLocked(periodID); existing != nil {
		return uuid.Nil, &LiveRunExistsError{PeriodID: periodID, ExistingRunID: existing.ID}
	}
	id := uuid.New()
	s.runs[id] = &CommissionRun{
		ID:        id,
		PeriodID:  periodID,
		PlanHash:  planHash,
		Status:    RunStatusRunning,
		StartedAt: time.Now(),
	}
	return id, nil
}

func (s *MemoryCommissionRunStore) CompleteRun(_ context.Context, runID uuid.UUID, carryForward json.RawMessage) error {
	if err := validateCarryForward(carryForward); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	run, ok := s.runs[runID]
	if !ok {
		return &RunNotFoundError{RunID: runID}
	}
	if run.Status != RunStatusRunning {
		return &RunNotRunningError{RunID: runID, Status: run.Status}
	}
	now := time.Now()
	run.Status = RunStatusComplete
	run.CompletedAt = &now
	// Clone: the caller keeps its slice and must not be able to mutate what
	// the store holds. Postgres copies the bytes on write, so this is parity.
	// cloneRaw also collapses an empty slice to nil, matching the NULL that
	// Postgres stores and reads back for a zero-length carry-forward.
	run.CarryForward = cloneRaw(carryForward)
	return nil
}

func (s *MemoryCommissionRunStore) VoidRun(_ context.Context, runID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.voidRunLocked(runID)
}

// voidRunLocked is a no-op on an already-voided run so a retry is safe.
// It never writes SupersededBy — only ReplaceRun does, once the replacement
// exists. Callers hold the lock.
func (s *MemoryCommissionRunStore) voidRunLocked(runID uuid.UUID) error {
	run, ok := s.runs[runID]
	if !ok {
		return &RunNotFoundError{RunID: runID}
	}
	if run.Status == RunStatusVoided {
		return nil
	}
	now := time.Now()
	run.Status = RunStatusVoided
	run.VoidedAt = &now
	return nil
}

// ReplaceRun voids the old run and opens its replacement under one lock,
// matching the Postgres store's single transaction.
func (s *MemoryCommissionRunStore) ReplaceRun(_ context.Context, oldRunID uuid.UUID, planHash string) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, ok := s.runs[oldRunID]
	if !ok {
		return uuid.Nil, &RunNotFoundError{RunID: oldRunID}
	}
	if old.Status == RunStatusVoided {
		// Allowed is set because ReplaceRun takes a complete run too. Without
		// it the message would read "not running" and send an operator
		// looking for the wrong state.
		return uuid.Nil, &RunNotRunningError{
			RunID:   oldRunID,
			Status:  old.Status,
			Allowed: []CommissionRunStatus{RunStatusRunning, RunStatusComplete},
		}
	}
	periodID := old.PeriodID
	// Only the hash needs checking. period_id is inherited from the run being
	// replaced, not supplied by the caller, so validating it here could
	// report an error about a value the caller never passed.
	//
	// Validate before mutating anything, so a bad hash cannot leave the old
	// run voided with no replacement.
	if err := validatePlanHashOnly(planHash); err != nil {
		return uuid.Nil, err
	}

	// Void first: the replacement cannot exist alongside a non-voided run.
	if err := s.voidRunLocked(oldRunID); err != nil {
		return uuid.Nil, err
	}
	// Cannot fail: the void above just freed the period, and nothing else can
	// take it while this call holds the lock. If that invariant ever breaks,
	// the old run is left voided with no replacement — the Postgres store
	// gets atomicity here from its transaction, which this cannot match.
	newID, err := s.createRunLocked(periodID, planHash)
	if err != nil {
		return uuid.Nil, err
	}
	// Link only once the replacement exists, mirroring the foreign key.
	// This is the only place SupersededBy is ever written, which is what
	// keeps the same-period rule enforceable in one spot.
	linked := newID
	old.SupersededBy = &linked
	return newID, nil
}

func (s *MemoryCommissionRunStore) GetRun(_ context.Context, runID uuid.UUID) (*CommissionRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	run, ok := s.runs[runID]
	if !ok {
		return nil, &RunNotFoundError{RunID: runID}
	}
	return copyRun(run), nil
}

func (s *MemoryCommissionRunStore) GetActiveRun(_ context.Context, periodID string) (*CommissionRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	run := s.activeRunLocked(periodID)
	if run == nil {
		return nil, nil
	}
	return copyRun(run), nil
}

// copyRun returns a caller-owned copy. The struct copy alone is not enough:
// every reference field would stay aliased to store state, letting a caller
// write through a returned value and silently change what the store hands
// everyone else. CarryForward is a json.RawMessage (a slice), and the three
// optional fields are pointers.
//
// Postgres scans fresh values on every read, so this is parity, not just
// hygiene: without it a suite case that mutates a returned run would pass
// against the real store and corrupt this one.
func copyRun(run *CommissionRun) *CommissionRun {
	cp := *run
	cp.CarryForward = cloneRaw(run.CarryForward)
	if run.CompletedAt != nil {
		completed := *run.CompletedAt
		cp.CompletedAt = &completed
	}
	if run.VoidedAt != nil {
		voided := *run.VoidedAt
		cp.VoidedAt = &voided
	}
	if run.SupersededBy != nil {
		superseded := *run.SupersededBy
		cp.SupersededBy = &superseded
	}
	return &cp
}

// errResultsUnimplemented keeps the results half of the interface loud until
// Task 4 fills it in. Returning zero values instead would be quietly wrong:
// several of the contract's own cases assert an EMPTY result set — a running
// run's rows are invisible, a voided run's are too, and saving an empty batch
// leaves none — so a stub returning (nil, nil) would make them pass against a
// store that does nothing. That is the exact drift this suite exists to
// catch.
var errResultsUnimplemented = errors.New("commission results: not implemented until HEU-555 task 4")

// SaveResults is implemented in Task 4.
func (s *MemoryCommissionRunStore) SaveResults(_ context.Context, _ uuid.UUID, _ string, _ []CommissionResultInput) error {
	return errResultsUnimplemented
}

// GetResults is implemented in Task 4.
func (s *MemoryCommissionRunStore) GetResults(_ context.Context, _ uuid.UUID) ([]CommissionResult, error) {
	return nil, errResultsUnimplemented
}

// GetLiveResults is implemented in Task 4.
func (s *MemoryCommissionRunStore) GetLiveResults(_ context.Context, _ string) ([]CommissionResult, error) {
	return nil, errResultsUnimplemented
}
