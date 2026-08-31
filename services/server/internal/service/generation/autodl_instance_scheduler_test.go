package generation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	platformautodl "github.com/mediago-dev/mediago-drama/services/server/internal/platform/autodl"
	settingsservice "github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

func TestAutoDLInstanceSchedulerStableRoundRobin(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(
		enabledInstance("instance-b"),
		enabledInstance("instance-a"),
	)
	fixture.setReady("instance-a", "image", true)
	fixture.setReady("instance-b", "image", true)
	scheduler := fixture.scheduler()

	var got []string
	for index := 0; index < 5; index++ {
		lease, err := scheduler.AcquireNew(context.Background(), InstanceRequest{
			TaskID:            fmt.Sprintf("task-%d", index),
			WorkflowProfileID: "image",
		})
		if err != nil {
			t.Fatalf("AcquireNew(%d) error = %v", index, err)
		}
		got = append(got, lease.InstanceProfileID())
		lease.ReleaseBeforeSubmit()
	}
	want := []string{"instance-a", "instance-b", "instance-a", "instance-b", "instance-a"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("round robin = %v, want %v", got, want)
	}
}

func TestAutoDLInstanceSchedulerRoundRobinUsesFullCompatibleOrderWhileBusy(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(
		enabledInstance("instance-c"),
		enabledInstance("instance-a"),
		enabledInstance("instance-b"),
	)
	for _, instanceID := range []string{"instance-a", "instance-b", "instance-c"} {
		fixture.setReady(instanceID, "image", true)
	}
	scheduler := fixture.scheduler()

	first := acquireLease(t, scheduler, InstanceRequest{TaskID: "task-a", WorkflowProfileID: "image"})
	second := acquireLease(t, scheduler, InstanceRequest{TaskID: "task-b", WorkflowProfileID: "image"})
	third := acquireLease(t, scheduler, InstanceRequest{TaskID: "task-c", WorkflowProfileID: "image"})
	got := []string{first.InstanceProfileID(), second.InstanceProfileID(), third.InstanceProfileID()}
	want := []string{"instance-a", "instance-b", "instance-c"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("concurrent round robin = %v, want %v", got, want)
	}
	first.ReleaseBeforeSubmit()
	second.ReleaseBeforeSubmit()
	third.ReleaseBeforeSubmit()

	for index, wantInstanceID := range []string{"instance-a", "instance-b", "instance-c"} {
		next := acquireLease(t, scheduler, InstanceRequest{
			TaskID: fmt.Sprintf("task-next-%d", index), WorkflowProfileID: "image",
		})
		if got := next.InstanceProfileID(); got != wantInstanceID {
			t.Fatalf("round robin after release at %d = %q, want %q", index, got, wantInstanceID)
		}
		next.ReleaseBeforeSubmit()
	}
}

func TestAutoDLInstanceSchedulerAllowsConcurrentDistinctInstances(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"), enabledInstance("instance-b"))
	fixture.setReady("instance-a", "image", true)
	fixture.setReady("instance-b", "image", true)
	scheduler := fixture.scheduler()

	start := make(chan struct{})
	results := make(chan InstanceLease, 2)
	errorsCh := make(chan error, 2)
	for _, taskID := range []string{"task-a", "task-b"} {
		taskID := taskID
		go func() {
			<-start
			lease, err := scheduler.AcquireNew(context.Background(), InstanceRequest{TaskID: taskID, WorkflowProfileID: "image"})
			if err != nil {
				errorsCh <- err
				return
			}
			results <- lease
		}()
	}
	close(start)
	first := receiveLease(t, results, errorsCh)
	second := receiveLease(t, results, errorsCh)
	defer first.ReleaseBeforeSubmit()
	defer second.ReleaseBeforeSubmit()
	if first.InstanceProfileID() == second.InstanceProfileID() {
		t.Fatalf("concurrent leases both used %q", first.InstanceProfileID())
	}
}

func TestAutoDLInstanceSchedulerSerializesImageAndVideoOnOneInstance(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	fixture.setReady("instance-a", "video", true)
	scheduler := fixture.scheduler()

	imageLease := acquireLease(t, scheduler, InstanceRequest{TaskID: "image-task", WorkflowProfileID: "image"})
	videoResult := acquireAsync(scheduler, InstanceRequest{TaskID: "video-task", WorkflowProfileID: "video"})
	assertAcquireBlocked(t, videoResult)

	imageLease.ReleaseBeforeSubmit()
	videoLease := receiveAcquire(t, videoResult)
	defer videoLease.ReleaseBeforeSubmit()
	if videoLease.InstanceProfileID() != "instance-a" {
		t.Fatalf("video instance = %q, want instance-a", videoLease.InstanceProfileID())
	}
}

func TestAutoDLInstanceSchedulerExcludesDisabledAndIncompatibleInstances(t *testing.T) {
	t.Parallel()

	disabled := enabledInstance("instance-disabled")
	disabled.Enabled = false
	fixture := newSchedulerFixture(disabled, enabledInstance("instance-incompatible"), enabledInstance("instance-ready"))
	fixture.setReady("instance-disabled", "image", true)
	fixture.setReady("instance-incompatible", "other-workflow", true)
	fixture.setReady("instance-ready", "image", true)
	scheduler := fixture.scheduler()

	lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "task-1", WorkflowProfileID: "image"})
	defer lease.ReleaseBeforeSubmit()
	if lease.InstanceProfileID() != "instance-ready" {
		t.Fatalf("selected instance = %q, want instance-ready", lease.InstanceProfileID())
	}
}

func TestAutoDLInstanceSchedulerAutomaticWaitWakesOnProfileChange(t *testing.T) {
	t.Parallel()

	disabled := enabledInstance("instance-a")
	disabled.Enabled = false
	fixture := newSchedulerFixture(disabled)
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()

	result := acquireAsync(scheduler, InstanceRequest{TaskID: "task-1", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, result)
	fixture.setProfiles(enabledInstance("instance-a"))
	scheduler.NotifyInstancesChanged()

	lease := receiveAcquire(t, result)
	defer lease.ReleaseBeforeSubmit()
	if lease.InstanceProfileID() != "instance-a" {
		t.Fatalf("selected instance = %q, want instance-a", lease.InstanceProfileID())
	}
}

func TestAutoDLInstanceSchedulerManualSelectionWaitsWithoutFallback(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"), enabledInstance("instance-b"))
	fixture.setReady("instance-a", "image", false)
	fixture.setReady("instance-b", "image", true)
	scheduler := fixture.scheduler()

	result := acquireAsync(scheduler, InstanceRequest{
		TaskID:                    "task-manual",
		WorkflowProfileID:         "image",
		SelectedInstanceProfileID: "instance-a",
	})
	assertAcquireBlocked(t, result)
	fixture.setReady("instance-a", "image", true)
	scheduler.NotifyInstancesChanged()

	lease := receiveAcquire(t, result)
	defer lease.ReleaseBeforeSubmit()
	if lease.InstanceProfileID() != "instance-a" {
		t.Fatalf("manual selection = %q, want instance-a", lease.InstanceProfileID())
	}
}

func TestAutoDLInstanceSchedulerManualSelectionWaitsForBusySelectedInstance(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"), enabledInstance("instance-b"))
	fixture.setReady("instance-a", "image", true)
	fixture.setReady("instance-b", "image", true)
	scheduler := fixture.scheduler()

	holder := acquireLease(t, scheduler, InstanceRequest{
		TaskID:                    "holder",
		WorkflowProfileID:         "image",
		SelectedInstanceProfileID: "instance-a",
	})
	result := acquireAsync(scheduler, InstanceRequest{
		TaskID:                    "waiter",
		WorkflowProfileID:         "image",
		SelectedInstanceProfileID: "instance-a",
	})
	assertAcquireBlocked(t, result)
	holder.ReleaseBeforeSubmit()

	lease := receiveAcquire(t, result)
	defer lease.ReleaseBeforeSubmit()
	if lease.InstanceProfileID() != "instance-a" {
		t.Fatalf("manual waiter selected %q, want instance-a", lease.InstanceProfileID())
	}
}

func TestAutoDLInstanceSchedulerWaitCancellationDoesNotMutatePool(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", false)
	scheduler := fixture.scheduler()
	ctx, cancel := context.WithCancel(context.Background())
	result := acquireAsyncContext(ctx, scheduler, InstanceRequest{TaskID: "canceled-task", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, result)
	cancel()
	if err := receiveAcquireError(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireNew() error = %v, want context.Canceled", err)
	}

	fixture.setReady("instance-a", "image", true)
	scheduler.NotifyInstancesChanged()
	lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "next-task", WorkflowProfileID: "image"})
	lease.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerPreCanceledAcquireDoesNotReadOrReserve(t *testing.T) {
	t.Parallel()

	var profileReads atomic.Int32
	scheduler := NewAutoDLInstanceScheduler(
		func(context.Context) ([]settingsservice.AutoDLInstanceProfile, error) {
			profileReads.Add(1)
			return []settingsservice.AutoDLInstanceProfile{enabledInstance("instance-a")}, nil
		},
		func(context.Context, settingsservice.AutoDLInstanceProfile, string) (platformautodl.Tunnel, bool) {
			return platformautodl.Tunnel{InstanceProfileID: "instance-a", BaseURL: "http://127.0.0.1:8188"}, true
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scheduler.AcquireNew(ctx, InstanceRequest{TaskID: "canceled-task", WorkflowProfileID: "image"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireNew() error = %v, want context.Canceled", err)
	}
	if profileReads.Load() != 0 {
		t.Fatalf("profile reads = %d, want 0", profileReads.Load())
	}

	lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "fresh-task", WorkflowProfileID: "image"})
	lease.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerCancellationDuringReadinessDoesNotReserve(t *testing.T) {
	t.Parallel()

	readinessStarted := make(chan struct{})
	releaseReadiness := make(chan struct{})
	var readinessCalls atomic.Int32
	scheduler := NewAutoDLInstanceScheduler(
		func(context.Context) ([]settingsservice.AutoDLInstanceProfile, error) {
			return []settingsservice.AutoDLInstanceProfile{enabledInstance("instance-a")}, nil
		},
		func(context.Context, settingsservice.AutoDLInstanceProfile, string) (platformautodl.Tunnel, bool) {
			if readinessCalls.Add(1) == 1 {
				close(readinessStarted)
				<-releaseReadiness
			}
			return platformautodl.Tunnel{InstanceProfileID: "instance-a", BaseURL: "http://127.0.0.1:8188"}, true
		},
	)
	ctx, cancel := context.WithCancel(context.Background())
	result := acquireAsyncContext(ctx, scheduler, InstanceRequest{TaskID: "canceled-task", WorkflowProfileID: "image"})
	waitForSchedulerSignal(t, readinessStarted, "readiness callback")
	cancel()
	close(releaseReadiness)
	if err := receiveAcquireError(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireNew() error = %v, want context.Canceled", err)
	}

	lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "fresh-task", WorkflowProfileID: "image"})
	lease.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerRejectsNonCanonicalIDs(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()
	for _, request := range []InstanceRequest{
		{TaskID: " task", WorkflowProfileID: "image"},
		{TaskID: "task", WorkflowProfileID: "image "},
		{TaskID: "task", WorkflowProfileID: "image", SelectedInstanceProfileID: " instance-a"},
	} {
		if _, err := scheduler.AcquireNew(context.Background(), request); !errors.Is(err, ErrAutoDLSchedulerInvalidRequest) {
			t.Fatalf("AcquireNew(%#v) error = %v, want invalid request", request, err)
		}
	}
}

func TestAutoDLInstanceSchedulerAcquiredLeaseOutlivesCallerContext(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()
	ctx, cancel := context.WithCancel(context.Background())
	lease, err := scheduler.AcquireNew(ctx, InstanceRequest{TaskID: "task-1", WorkflowProfileID: "image"})
	if err != nil {
		t.Fatalf("AcquireNew() error = %v", err)
	}
	cancel()

	result := acquireAsync(scheduler, InstanceRequest{TaskID: "task-2", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, result)
	lease.ReleaseBeforeSubmit()
	next := receiveAcquire(t, result)
	next.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerReleaseBeforeSubmitIsSafeOnlyBeforeBind(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()

	lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "task-1", WorkflowProfileID: "image"})
	if err := lease.BindPrompt("prompt-1"); err != nil {
		t.Fatalf("BindPrompt() error = %v", err)
	}
	lease.ReleaseBeforeSubmit()

	result := acquireAsync(scheduler, InstanceRequest{TaskID: "task-2", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, result)
	resumed, err := scheduler.Resume(context.Background(), "task-1", "instance-a", "prompt-1")
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	resumed.ReleaseTerminal()

	next := receiveAcquire(t, result)
	next.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerRejectsDuplicateTaskAcquire(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"), enabledInstance("instance-b"))
	fixture.setReady("instance-a", "image", true)
	fixture.setReady("instance-b", "image", true)
	scheduler := fixture.scheduler()
	lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "same-task", WorkflowProfileID: "image"})
	defer lease.ReleaseBeforeSubmit()

	if _, err := scheduler.AcquireNew(context.Background(), InstanceRequest{TaskID: "same-task", WorkflowProfileID: "image"}); !errors.Is(err, ErrAutoDLReservationConflict) {
		t.Fatalf("duplicate AcquireNew() error = %v, want reservation conflict", err)
	}
}

func TestAutoDLInstanceSchedulerStaleLeaseCannotMutateNewReservation(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()
	stale := acquireLease(t, scheduler, InstanceRequest{TaskID: "old-task", WorkflowProfileID: "image"})
	stale.ReleaseBeforeSubmit()
	stale.ReleaseBeforeSubmit()

	current := acquireLease(t, scheduler, InstanceRequest{TaskID: "new-task", WorkflowProfileID: "image"})
	if err := stale.BindPrompt("stale-prompt"); !errors.Is(err, ErrAutoDLReservationConflict) {
		t.Fatalf("stale BindPrompt() error = %v, want reservation conflict", err)
	}
	if err := stale.Quarantine("submission_outcome_unknown"); !errors.Is(err, ErrAutoDLReservationConflict) {
		t.Fatalf("stale Quarantine() error = %v, want reservation conflict", err)
	}
	stale.ReleaseTerminal()
	stale.ReleaseTerminal()

	blocked := acquireAsync(scheduler, InstanceRequest{TaskID: "third-task", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, blocked)
	if err := current.BindPrompt("current-prompt"); err != nil {
		t.Fatalf("current BindPrompt() error = %v", err)
	}
	current.ReleaseTerminal()
	current.ReleaseTerminal()
	next := receiveAcquire(t, blocked)
	next.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerRestoreAndExactTaskResume(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()
	if err := scheduler.RestoreReservations([]PersistedInstanceReservation{{
		TaskID: "task-restored", InstanceProfileID: "instance-a", WorkflowProfileID: "image", PromptID: "prompt-restored",
	}}); err != nil {
		t.Fatalf("RestoreReservations() error = %v", err)
	}

	blocked := acquireAsync(scheduler, InstanceRequest{TaskID: "task-new", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, blocked)
	for _, resume := range []struct {
		taskID     string
		instanceID string
		promptID   string
	}{
		{taskID: "wrong-task", instanceID: "instance-a", promptID: "prompt-restored"},
		{taskID: "task-restored", instanceID: "wrong-instance", promptID: "prompt-restored"},
		{taskID: "task-restored", instanceID: "instance-a", promptID: "wrong-prompt"},
	} {
		if _, err := scheduler.Resume(context.Background(), resume.taskID, resume.instanceID, resume.promptID); !errors.Is(err, ErrAutoDLReservationConflict) {
			t.Fatalf("Resume(%q, %q, %q) error = %v, want conflict", resume.taskID, resume.instanceID, resume.promptID, err)
		}
	}

	lease, err := scheduler.Resume(context.Background(), "task-restored", "instance-a", "prompt-restored")
	if err != nil {
		t.Fatalf("Resume(exact) error = %v", err)
	}
	if lease.InstanceProfileID() != "instance-a" || lease.Tunnel().InstanceProfileID != "instance-a" {
		t.Fatalf("resumed lease = (%q, %#v)", lease.InstanceProfileID(), lease.Tunnel())
	}
	lease.ReleaseTerminal()

	next := receiveAcquire(t, blocked)
	next.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerRestoresEmptyPromptReservationForExactResume(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()
	if err := scheduler.RestoreReservations([]PersistedInstanceReservation{{
		TaskID: "task-pre-submit", InstanceProfileID: "instance-a", WorkflowProfileID: "image", PromptID: "",
	}}); err != nil {
		t.Fatalf("RestoreReservations(empty prompt) error = %v", err)
	}

	blocked := acquireAsync(scheduler, InstanceRequest{TaskID: "task-new", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, blocked)
	if _, err := scheduler.Resume(context.Background(), "task-pre-submit", "instance-a", "wrong-prompt"); !errors.Is(err, ErrAutoDLReservationConflict) {
		t.Fatalf("Resume(nonempty mismatch) error = %v, want conflict", err)
	}
	lease, err := scheduler.Resume(context.Background(), "task-pre-submit", "instance-a", "")
	if err != nil {
		t.Fatalf("Resume(exact empty prompt) error = %v", err)
	}
	lease.ReleaseTerminal()

	next := receiveAcquire(t, blocked)
	next.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerQuarantineHoldsSlotUntilExactReconciliation(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()
	lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "unknown-task", WorkflowProfileID: "image"})
	if err := lease.Quarantine("submission_outcome_unknown"); err != nil {
		t.Fatalf("Quarantine() error = %v", err)
	}
	if err := lease.Quarantine("submission_outcome_unknown"); !errors.Is(err, ErrAutoDLReservationConflict) {
		t.Fatalf("second Quarantine() error = %v, want reservation conflict", err)
	}
	lease.ReleaseBeforeSubmit()
	lease.ReleaseTerminal()

	blocked := acquireAsync(scheduler, InstanceRequest{TaskID: "next-task", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, blocked)
	if err := scheduler.ReconcileQuarantine("instance-a", "wrong-task"); !errors.Is(err, ErrAutoDLReservationConflict) {
		t.Fatalf("ReconcileQuarantine(wrong task) error = %v, want conflict", err)
	}
	assertAcquireBlocked(t, blocked)
	if err := scheduler.ReconcileQuarantine("instance-a", "unknown-task"); err != nil {
		t.Fatalf("ReconcileQuarantine(exact task) error = %v", err)
	}

	next := receiveAcquire(t, blocked)
	next.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerRestoresQuarantinedReservation(t *testing.T) {
	t.Parallel()

	fixture := newSchedulerFixture(enabledInstance("instance-a"))
	fixture.setReady("instance-a", "image", true)
	scheduler := fixture.scheduler()
	if err := scheduler.RestoreReservations([]PersistedInstanceReservation{{
		TaskID: "unknown-task", InstanceProfileID: "instance-a", WorkflowProfileID: "image",
		Quarantined: true, QuarantineReason: "submission_outcome_unknown",
	}}); err != nil {
		t.Fatalf("RestoreReservations() error = %v", err)
	}

	blocked := acquireAsync(scheduler, InstanceRequest{TaskID: "next-task", WorkflowProfileID: "image"})
	assertAcquireBlocked(t, blocked)
	if err := scheduler.ReconcileQuarantine("instance-a", "unknown-task"); err != nil {
		t.Fatalf("ReconcileQuarantine() error = %v", err)
	}
	next := receiveAcquire(t, blocked)
	next.ReleaseBeforeSubmit()
}

func TestAutoDLInstanceSchedulerRestoreRejectsDuplicateTasksAndInstancesAtomically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		reservations []PersistedInstanceReservation
	}{
		{
			name: "duplicate task",
			reservations: []PersistedInstanceReservation{
				{TaskID: "same-task", InstanceProfileID: "instance-a", WorkflowProfileID: "image", PromptID: "prompt-a"},
				{TaskID: "same-task", InstanceProfileID: "instance-b", WorkflowProfileID: "image", PromptID: "prompt-b"},
			},
		},
		{
			name: "duplicate instance",
			reservations: []PersistedInstanceReservation{
				{TaskID: "task-a", InstanceProfileID: "instance-a", WorkflowProfileID: "image", PromptID: "prompt-a"},
				{TaskID: "task-b", InstanceProfileID: "instance-a", WorkflowProfileID: "image", PromptID: "prompt-b"},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newSchedulerFixture(enabledInstance("instance-a"), enabledInstance("instance-b"))
			fixture.setReady("instance-a", "image", true)
			fixture.setReady("instance-b", "image", true)
			scheduler := fixture.scheduler()
			if err := scheduler.RestoreReservations(test.reservations); !errors.Is(err, ErrAutoDLReservationConflict) {
				t.Fatalf("RestoreReservations() error = %v, want reservation conflict", err)
			}
			lease := acquireLease(t, scheduler, InstanceRequest{TaskID: "fresh-task", WorkflowProfileID: "image"})
			lease.ReleaseBeforeSubmit()
		})
	}
}

type schedulerFixture struct {
	mu       sync.RWMutex
	profiles []settingsservice.AutoDLInstanceProfile
	ready    map[string]map[string]bool
}

func newSchedulerFixture(profiles ...settingsservice.AutoDLInstanceProfile) *schedulerFixture {
	return &schedulerFixture{profiles: profiles, ready: make(map[string]map[string]bool)}
}

func (fixture *schedulerFixture) scheduler() InstanceScheduler {
	return NewAutoDLInstanceScheduler(fixture.loadProfiles, fixture.readiness)
}

func (fixture *schedulerFixture) loadProfiles(ctx context.Context) ([]settingsservice.AutoDLInstanceProfile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fixture.mu.RLock()
	defer fixture.mu.RUnlock()
	return append([]settingsservice.AutoDLInstanceProfile(nil), fixture.profiles...), nil
}

func (fixture *schedulerFixture) readiness(ctx context.Context, profile settingsservice.AutoDLInstanceProfile, workflowProfileID string) (platformautodl.Tunnel, bool) {
	if ctx.Err() != nil {
		return platformautodl.Tunnel{}, false
	}
	fixture.mu.RLock()
	ready := fixture.ready[profile.ID][workflowProfileID]
	fixture.mu.RUnlock()
	if !ready {
		return platformautodl.Tunnel{}, false
	}
	return platformautodl.Tunnel{InstanceProfileID: profile.ID, BaseURL: "http://127.0.0.1:8188"}, true
}

func (fixture *schedulerFixture) setProfiles(profiles ...settingsservice.AutoDLInstanceProfile) {
	fixture.mu.Lock()
	fixture.profiles = append([]settingsservice.AutoDLInstanceProfile(nil), profiles...)
	fixture.mu.Unlock()
}

func (fixture *schedulerFixture) setReady(instanceID string, workflowProfileID string, ready bool) {
	fixture.mu.Lock()
	if fixture.ready[instanceID] == nil {
		fixture.ready[instanceID] = make(map[string]bool)
	}
	fixture.ready[instanceID][workflowProfileID] = ready
	fixture.mu.Unlock()
}

func enabledInstance(id string) settingsservice.AutoDLInstanceProfile {
	return settingsservice.AutoDLInstanceProfile{ID: id, Name: id, Enabled: true}
}

type acquireResult struct {
	lease InstanceLease
	err   error
}

func acquireAsync(scheduler InstanceScheduler, request InstanceRequest) <-chan acquireResult {
	return acquireAsyncContext(context.Background(), scheduler, request)
}

func acquireAsyncContext(ctx context.Context, scheduler InstanceScheduler, request InstanceRequest) <-chan acquireResult {
	result := make(chan acquireResult, 1)
	go func() {
		lease, err := scheduler.AcquireNew(ctx, request)
		result <- acquireResult{lease: lease, err: err}
	}()
	return result
}

func acquireLease(t *testing.T, scheduler InstanceScheduler, request InstanceRequest) InstanceLease {
	t.Helper()
	lease, err := scheduler.AcquireNew(context.Background(), request)
	if err != nil {
		t.Fatalf("AcquireNew(%#v) error = %v", request, err)
	}
	return lease
}

func assertAcquireBlocked(t *testing.T, result <-chan acquireResult) {
	t.Helper()
	select {
	case got := <-result:
		if got.lease != nil {
			got.lease.ReleaseBeforeSubmit()
		}
		t.Fatalf("AcquireNew() returned while expected blocked: %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func receiveAcquire(t *testing.T, result <-chan acquireResult) InstanceLease {
	t.Helper()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("AcquireNew() error = %v", got.err)
		}
		if got.lease == nil {
			t.Fatal("AcquireNew() lease = nil")
		}
		return got.lease
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AcquireNew()")
		return nil
	}
}

func receiveAcquireError(t *testing.T, result <-chan acquireResult) error {
	t.Helper()
	select {
	case got := <-result:
		if got.lease != nil {
			got.lease.ReleaseBeforeSubmit()
			t.Fatal("AcquireNew() returned a lease, want error")
		}
		return got.err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for AcquireNew() error")
		return nil
	}
}

func receiveLease(t *testing.T, leases <-chan InstanceLease, errorsCh <-chan error) InstanceLease {
	t.Helper()
	select {
	case err := <-errorsCh:
		t.Fatalf("AcquireNew() error = %v", err)
		return nil
	case lease := <-leases:
		return lease
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for concurrent lease")
		return nil
	}
}

func waitForSchedulerSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
