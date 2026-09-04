package generation

import (
	"strings"
	"testing"
)

func TestGenerationTaskWithMessageKeepsPartialBatchUsable(t *testing.T) {
	task := GenerationTaskRecord{
		ID:   "task-partial",
		Kind: "image",
		Assets: []GenerationAsset{
			{Kind: "image", SlotIndex: 0, AssetID: "asset-ok"},
		},
	}
	failed := FailedGenerationResponse("task-partial", assertableError("second image failed"))
	failed.Assets = task.Assets

	merged := GenerationTaskWithMessage(task, failed)
	if merged.Status != "completed" {
		t.Fatalf("status = %q, want completed for partial success", merged.Status)
	}
	if !strings.Contains(merged.Message, "部分成功") {
		t.Fatalf("message = %q, want partial-success note", merged.Message)
	}
	if merged.Error == "" {
		t.Fatal("error detail should stay visible on partial success")
	}
}

func TestGenerationTaskWithMessageKeepsFullFailureFailed(t *testing.T) {
	task := GenerationTaskRecord{ID: "task-failed", Kind: "image"}
	failed := FailedGenerationResponse("task-failed", assertableError("provider down"))

	merged := GenerationTaskWithMessage(task, failed)
	if merged.Status != "failed" {
		t.Fatalf("status = %q, want failed when no assets stored", merged.Status)
	}
}

func TestGenerationTaskWithMessageIgnoresDeletedSlotsForPartial(t *testing.T) {
	task := GenerationTaskRecord{
		ID:                "task-deleted",
		Kind:              "image",
		DeletedAssetSlots: []int{0},
		Assets:            []GenerationAsset{{Kind: "image", SlotIndex: 0, AssetID: "asset-gone"}},
	}
	failed := FailedGenerationResponse("task-deleted", assertableError("boom"))
	failed.Assets = task.Assets

	merged := GenerationTaskWithMessage(task, failed)
	if merged.Status != "failed" {
		t.Fatalf("status = %q, want failed when only deleted slots remain", merged.Status)
	}
}

func TestGenerationTaskWithMessageClearsErrorsForRecoveryStatuses(t *testing.T) {
	for _, status := range []string{"preparing", "importing", "waiting_reconnect"} {
		t.Run(status, func(t *testing.T) {
			task := GenerationTaskRecord{
				ID:        "task-" + status,
				Kind:      "image",
				Status:    "failed",
				Error:     "old provider error",
				ErrorCode: "provider_error",
				ErrorType: "provider_error",
				Retryable: true,
			}

			merged := GenerationTaskWithMessage(task, GenerationMessageResponse{Status: status})
			if merged.Error != "" || merged.ErrorCode != "" || merged.ErrorType != "" || merged.Retryable {
				t.Fatalf("merged task = %+v, want active recovery state with old errors cleared", merged)
			}
		})
	}
}

type assertableError string

func (e assertableError) Error() string { return string(e) }
