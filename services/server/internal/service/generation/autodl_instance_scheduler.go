package generation

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	platformautodl "github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	settingsservice "github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

var (
	ErrAutoDLSchedulerInvalidRequest = errors.New("AutoDL scheduler request is invalid")
	ErrAutoDLSchedulerUnavailable    = errors.New("AutoDL scheduler dependencies are unavailable")
	ErrAutoDLReservationConflict     = errors.New("AutoDL instance reservation conflicts with existing state")
	ErrAutoDLReservationNotFound     = errors.New("AutoDL instance reservation was not found")
)

type InstanceRequest struct {
	TaskID                    string
	WorkflowProfileID         string
	WorkflowVersionID         string
	SelectedInstanceProfileID string
}

type PersistedInstanceReservation struct {
	TaskID            string
	InstanceProfileID string
	WorkflowProfileID string
	WorkflowVersionID string
	PromptID          string
	Quarantined       bool
	QuarantineReason  string
}

type AutoDLInstanceProfiles func(context.Context) ([]settingsservice.AutoDLInstanceProfile, error)

type AutoDLInstanceReadiness func(context.Context, settingsservice.AutoDLInstanceProfile, string, string) (platformautodl.Tunnel, bool)

type InstanceLease interface {
	InstanceProfileID() string
	Tunnel() platformautodl.Tunnel
	BindPrompt(promptID string) error
	Quarantine(reason string) error
	ReleaseBeforeSubmit()
	ReleaseTerminal()
}

type InstanceScheduler interface {
	AcquireNew(context.Context, InstanceRequest) (InstanceLease, error)
	Resume(context.Context, string, string, string) (InstanceLease, error)
	RestoreReservations([]PersistedInstanceReservation) error
	ReconcileQuarantine(instanceProfileID string, taskID string) error
	NotifyInstancesChanged()
}

type autoDLInstanceScheduler struct {
	profiles  AutoDLInstanceProfiles
	readiness AutoDLInstanceReadiness

	mu         sync.Mutex
	slots      map[string]*autoDLInstanceReservation
	tasks      map[string]string
	nextToken  uint64
	autoCursor uint64
	revision   uint64
	wake       chan struct{}
}

type autoDLInstanceReservation struct {
	taskID            string
	workflowProfileID string
	workflowVersionID string
	promptID          string
	tunnel            platformautodl.Tunnel
	token             uint64
	quarantined       bool
	quarantineReason  string
}

type autoDLInstanceCandidate struct {
	instanceID string
	tunnel     platformautodl.Tunnel
}

type autoDLInstanceLease struct {
	scheduler  *autoDLInstanceScheduler
	instanceID string
	taskID     string
	token      uint64
	tunnel     platformautodl.Tunnel
}

func NewAutoDLInstanceScheduler(profiles AutoDLInstanceProfiles, readiness AutoDLInstanceReadiness) InstanceScheduler {
	return &autoDLInstanceScheduler{
		profiles:  profiles,
		readiness: readiness,
		slots:     make(map[string]*autoDLInstanceReservation),
		tasks:     make(map[string]string),
		wake:      make(chan struct{}),
	}
}

func (scheduler *autoDLInstanceScheduler) AcquireNew(ctx context.Context, request InstanceRequest) (InstanceLease, error) {
	if err := validateInstanceRequest(ctx, request); err != nil {
		return nil, err
	}
	if scheduler == nil || scheduler.profiles == nil || scheduler.readiness == nil {
		return nil, ErrAutoDLSchedulerUnavailable
	}

	for {
		scheduler.mu.Lock()
		if _, exists := scheduler.tasks[request.TaskID]; exists {
			scheduler.mu.Unlock()
			return nil, ErrAutoDLReservationConflict
		}
		revision := scheduler.revision
		scheduler.mu.Unlock()

		candidates, err := scheduler.loadCandidates(ctx, request.WorkflowProfileID, request.WorkflowVersionID, request.SelectedInstanceProfileID, true)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		scheduler.mu.Lock()
		if _, exists := scheduler.tasks[request.TaskID]; exists {
			scheduler.mu.Unlock()
			return nil, ErrAutoDLReservationConflict
		}
		if scheduler.revision != revision {
			scheduler.mu.Unlock()
			continue
		}
		selectedIndex := -1
		if len(candidates) > 0 {
			start := 0
			if request.SelectedInstanceProfileID == "" {
				start = int(scheduler.autoCursor % uint64(len(candidates)))
			}
			for offset := 0; offset < len(candidates); offset++ {
				index := (start + offset) % len(candidates)
				if scheduler.slots[candidates[index].instanceID] == nil {
					selectedIndex = index
					break
				}
			}
		}
		if selectedIndex >= 0 {
			if err := ctx.Err(); err != nil {
				scheduler.mu.Unlock()
				return nil, err
			}
			selected := candidates[selectedIndex]
			if request.SelectedInstanceProfileID == "" {
				scheduler.autoCursor = uint64((selectedIndex + 1) % len(candidates))
			}
			scheduler.nextToken++
			reservation := &autoDLInstanceReservation{
				taskID: request.TaskID, workflowProfileID: request.WorkflowProfileID,
				workflowVersionID: request.WorkflowVersionID,
				tunnel: selected.tunnel, token: scheduler.nextToken,
			}
			scheduler.slots[selected.instanceID] = reservation
			scheduler.tasks[request.TaskID] = selected.instanceID
			lease := scheduler.leaseLocked(selected.instanceID, reservation)
			scheduler.mu.Unlock()
			return lease, nil
		}
		wake := scheduler.wake
		scheduler.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (scheduler *autoDLInstanceScheduler) Resume(ctx context.Context, taskID string, instanceProfileID string, promptID string) (InstanceLease, error) {
	if err := requireSchedulerContext(ctx); err != nil {
		return nil, err
	}
	if !validSchedulerID(taskID) || !validSchedulerID(instanceProfileID) || (promptID != "" && !validSchedulerID(promptID)) {
		return nil, ErrAutoDLSchedulerInvalidRequest
	}
	if scheduler == nil || scheduler.profiles == nil || scheduler.readiness == nil {
		return nil, ErrAutoDLSchedulerUnavailable
	}

	for {
		scheduler.mu.Lock()
		reservation, err := scheduler.exactReservationLocked(taskID, instanceProfileID)
		if err != nil || reservation.promptID != promptID {
			scheduler.mu.Unlock()
			return nil, ErrAutoDLReservationConflict
		}
		if reservation.tunnel.InstanceProfileID == instanceProfileID && reservation.tunnel.BaseURL != "" {
			lease := scheduler.leaseLocked(instanceProfileID, reservation)
			scheduler.mu.Unlock()
			return lease, nil
		}
		revision := scheduler.revision
		workflowProfileID := reservation.workflowProfileID
		workflowVersionID := reservation.workflowVersionID
		token := reservation.token
		scheduler.mu.Unlock()

		candidates, err := scheduler.loadCandidates(ctx, workflowProfileID, workflowVersionID, instanceProfileID, false)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		scheduler.mu.Lock()
		reservation, exactErr := scheduler.exactReservationLocked(taskID, instanceProfileID)
		if exactErr != nil || reservation.token != token || reservation.promptID != promptID {
			scheduler.mu.Unlock()
			return nil, ErrAutoDLReservationConflict
		}
		if scheduler.revision != revision {
			scheduler.mu.Unlock()
			continue
		}
		if len(candidates) == 1 {
			reservation.tunnel = candidates[0].tunnel
			lease := scheduler.leaseLocked(instanceProfileID, reservation)
			scheduler.mu.Unlock()
			return lease, nil
		}
		wake := scheduler.wake
		scheduler.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (scheduler *autoDLInstanceScheduler) RestoreReservations(reservations []PersistedInstanceReservation) error {
	if scheduler == nil {
		return ErrAutoDLSchedulerUnavailable
	}
	instances := make(map[string]struct{}, len(reservations))
	tasks := make(map[string]struct{}, len(reservations))
	for _, reservation := range reservations {
		if !validPersistedReservation(reservation) {
			return ErrAutoDLSchedulerInvalidRequest
		}
		if _, exists := instances[reservation.InstanceProfileID]; exists {
			return ErrAutoDLReservationConflict
		}
		if _, exists := tasks[reservation.TaskID]; exists {
			return ErrAutoDLReservationConflict
		}
		instances[reservation.InstanceProfileID] = struct{}{}
		tasks[reservation.TaskID] = struct{}{}
	}

	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	for _, reservation := range reservations {
		if scheduler.slots[reservation.InstanceProfileID] != nil {
			return ErrAutoDLReservationConflict
		}
		if _, exists := scheduler.tasks[reservation.TaskID]; exists {
			return ErrAutoDLReservationConflict
		}
	}
	for _, persisted := range reservations {
		scheduler.nextToken++
		reservation := &autoDLInstanceReservation{
			taskID: persisted.TaskID, workflowProfileID: persisted.WorkflowProfileID,
			workflowVersionID: persisted.WorkflowVersionID,
			promptID: persisted.PromptID, token: scheduler.nextToken,
			quarantined: persisted.Quarantined, quarantineReason: strings.TrimSpace(persisted.QuarantineReason),
		}
		scheduler.slots[persisted.InstanceProfileID] = reservation
		scheduler.tasks[persisted.TaskID] = persisted.InstanceProfileID
	}
	if len(reservations) > 0 {
		scheduler.signalLocked()
	}
	return nil
}

func (scheduler *autoDLInstanceScheduler) ReconcileQuarantine(instanceProfileID string, taskID string) error {
	if scheduler == nil {
		return ErrAutoDLSchedulerUnavailable
	}
	if !validSchedulerID(instanceProfileID) || !validSchedulerID(taskID) {
		return ErrAutoDLSchedulerInvalidRequest
	}
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	reservation, err := scheduler.exactReservationLocked(taskID, instanceProfileID)
	if err != nil {
		return err
	}
	if !reservation.quarantined {
		return ErrAutoDLReservationConflict
	}
	scheduler.releaseLocked(instanceProfileID, reservation)
	return nil
}

func (scheduler *autoDLInstanceScheduler) NotifyInstancesChanged() {
	if scheduler == nil {
		return
	}
	scheduler.mu.Lock()
	scheduler.signalLocked()
	scheduler.mu.Unlock()
}

func (scheduler *autoDLInstanceScheduler) loadCandidates(ctx context.Context, workflowProfileID string, workflowVersionID string, selectedInstanceProfileID string, requireEnabled bool) ([]autoDLInstanceCandidate, error) {
	profiles, err := scheduler.profiles(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(profiles, func(left int, right int) bool { return profiles[left].ID < profiles[right].ID })
	candidates := make([]autoDLInstanceCandidate, 0, len(profiles))
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !validSchedulerID(profile.ID) || (requireEnabled && !profile.Enabled) {
			continue
		}
		if selectedInstanceProfileID != "" && profile.ID != selectedInstanceProfileID {
			continue
		}
		if _, exists := seen[profile.ID]; exists {
			continue
		}
		seen[profile.ID] = struct{}{}
		tunnel, ready := scheduler.readiness(ctx, profile, workflowProfileID, workflowVersionID)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ready || tunnel.InstanceProfileID != profile.ID || strings.TrimSpace(tunnel.BaseURL) == "" {
			continue
		}
		candidates = append(candidates, autoDLInstanceCandidate{instanceID: profile.ID, tunnel: tunnel})
	}
	return candidates, nil
}

func (scheduler *autoDLInstanceScheduler) leaseLocked(instanceProfileID string, reservation *autoDLInstanceReservation) InstanceLease {
	return &autoDLInstanceLease{
		scheduler: scheduler, instanceID: instanceProfileID, taskID: reservation.taskID,
		token: reservation.token, tunnel: reservation.tunnel,
	}
}

func (scheduler *autoDLInstanceScheduler) exactReservationLocked(taskID string, instanceProfileID string) (*autoDLInstanceReservation, error) {
	if scheduler.tasks[taskID] != instanceProfileID {
		return nil, ErrAutoDLReservationConflict
	}
	reservation := scheduler.slots[instanceProfileID]
	if reservation == nil || reservation.taskID != taskID {
		return nil, ErrAutoDLReservationNotFound
	}
	return reservation, nil
}

func (scheduler *autoDLInstanceScheduler) signalLocked() {
	close(scheduler.wake)
	scheduler.wake = make(chan struct{})
	scheduler.revision++
}

func (scheduler *autoDLInstanceScheduler) releaseLocked(instanceProfileID string, reservation *autoDLInstanceReservation) {
	if scheduler.slots[instanceProfileID] != reservation {
		return
	}
	delete(scheduler.slots, instanceProfileID)
	delete(scheduler.tasks, reservation.taskID)
	scheduler.signalLocked()
}

func (lease *autoDLInstanceLease) InstanceProfileID() string {
	if lease == nil {
		return ""
	}
	return lease.instanceID
}

func (lease *autoDLInstanceLease) Tunnel() platformautodl.Tunnel {
	if lease == nil {
		return platformautodl.Tunnel{}
	}
	return lease.tunnel
}

func (lease *autoDLInstanceLease) BindPrompt(promptID string) error {
	if lease == nil || lease.scheduler == nil || !validSchedulerID(promptID) {
		return ErrAutoDLSchedulerInvalidRequest
	}
	lease.scheduler.mu.Lock()
	defer lease.scheduler.mu.Unlock()
	reservation := lease.scheduler.slots[lease.instanceID]
	if reservation == nil || reservation.taskID != lease.taskID || reservation.token != lease.token {
		return ErrAutoDLReservationConflict
	}
	if reservation.promptID != "" && reservation.promptID != promptID {
		return ErrAutoDLReservationConflict
	}
	reservation.promptID = promptID
	return nil
}

func (lease *autoDLInstanceLease) Quarantine(reason string) error {
	if lease == nil || lease.scheduler == nil || strings.TrimSpace(reason) == "" {
		return ErrAutoDLSchedulerInvalidRequest
	}
	lease.scheduler.mu.Lock()
	defer lease.scheduler.mu.Unlock()
	reservation := lease.scheduler.slots[lease.instanceID]
	if reservation == nil || reservation.taskID != lease.taskID || reservation.token != lease.token || reservation.quarantined {
		return ErrAutoDLReservationConflict
	}
	reservation.quarantined = true
	reservation.quarantineReason = strings.TrimSpace(reason)
	lease.scheduler.signalLocked()
	return nil
}

func (lease *autoDLInstanceLease) ReleaseBeforeSubmit() {
	if lease == nil || lease.scheduler == nil {
		return
	}
	lease.scheduler.mu.Lock()
	defer lease.scheduler.mu.Unlock()
	reservation := lease.scheduler.slots[lease.instanceID]
	if reservation == nil || reservation.taskID != lease.taskID || reservation.token != lease.token || reservation.promptID != "" || reservation.quarantined {
		return
	}
	lease.scheduler.releaseLocked(lease.instanceID, reservation)
}

func (lease *autoDLInstanceLease) ReleaseTerminal() {
	if lease == nil || lease.scheduler == nil {
		return
	}
	lease.scheduler.mu.Lock()
	defer lease.scheduler.mu.Unlock()
	reservation := lease.scheduler.slots[lease.instanceID]
	if reservation == nil || reservation.taskID != lease.taskID || reservation.token != lease.token || reservation.quarantined {
		return
	}
	lease.scheduler.releaseLocked(lease.instanceID, reservation)
}

func validateInstanceRequest(ctx context.Context, request InstanceRequest) error {
	if err := requireSchedulerContext(ctx); err != nil {
		return err
	}
	if !validSchedulerID(request.TaskID) || !validSchedulerID(request.WorkflowProfileID) || !validSchedulerID(request.WorkflowVersionID) {
		return ErrAutoDLSchedulerInvalidRequest
	}
	if request.SelectedInstanceProfileID != "" && !validSchedulerID(request.SelectedInstanceProfileID) {
		return ErrAutoDLSchedulerInvalidRequest
	}
	return nil
}

func validPersistedReservation(reservation PersistedInstanceReservation) bool {
	if !validSchedulerID(reservation.TaskID) || !validSchedulerID(reservation.InstanceProfileID) || !validSchedulerID(reservation.WorkflowProfileID) || !validSchedulerID(reservation.WorkflowVersionID) {
		return false
	}
	if reservation.Quarantined {
		return strings.TrimSpace(reservation.QuarantineReason) != "" && (reservation.PromptID == "" || validSchedulerID(reservation.PromptID))
	}
	return reservation.PromptID == "" || validSchedulerID(reservation.PromptID)
}

func requireSchedulerContext(ctx context.Context) error {
	if ctx == nil {
		return ErrAutoDLSchedulerInvalidRequest
	}
	return ctx.Err()
}

func validSchedulerID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 4096 && !strings.ContainsRune(value, 0)
}
