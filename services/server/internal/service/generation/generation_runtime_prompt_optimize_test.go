package generation

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	coregeneration "github.com/mediago-dev/mediago-drama/packages/core/pkg/generation"
	"github.com/mediago-dev/mediago-drama/services/server/internal/service/textcompletion"
)

func TestCodexImagePromptOptimizationRouting(t *testing.T) {
	t.Run("disabled sends the original prompt directly to image generation", func(t *testing.T) {
		workflow := newPromptSupplementsTestWorkflow(t)
		provider := &imagePromptOptimizationProvider{requests: make(chan coregeneration.Request, 1)}
		workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, mediaLinkPromptOptimizationReady)

		var textCalls atomic.Int32
		workflow.SetCodexTextBackend(
			textcompletion.BackendFunc(func(context.Context, textcompletion.Request) (textcompletion.Result, error) {
				textCalls.Add(1)
				return textcompletion.Result{Text: "must not be used"}, nil
			}),
			func(context.Context, textcompletion.Request) bool { return true },
		)

		response, status, err := workflow.CreateGenerationMessage(context.Background(), GenerationMessageRequest{
			Kind:    string(coregeneration.KindImage),
			RouteID: coregeneration.RouteCodexImage,
			Prompt:  "用户原始人物提示词",
		})
		if err != nil || status != http.StatusOK {
			t.Fatalf("CreateGenerationMessage() status = %d error = %v", status, err)
		}
		request := waitForPromptSupplementsProviderRequest(t, provider.requests)
		if request.Prompt != "用户原始人物提示词" || textCalls.Load() != 0 {
			t.Fatalf("image prompt = %q, text calls = %d; want untouched original prompt and no optimizer", request.Prompt, textCalls.Load())
		}
		waitForGenerationTask(t, workflow.generationTasks, response.ID, func(task GenerationTaskRecord) bool {
			return task.Status == "completed"
		})
	})

	t.Run("enabled uses the internal Codex optimizer before image generation", func(t *testing.T) {
		workflow := newPromptSupplementsTestWorkflow(t)
		provider := &imagePromptOptimizationProvider{requests: make(chan coregeneration.Request, 1)}
		workflow.SetMediaLinkProviders(provider, &mediaLinkTestProvider{name: "h3"}, mediaLinkPromptOptimizationReady)

		var codexRequest textcompletion.Request
		workflow.SetCodexTextBackend(
			textcompletion.BackendFunc(func(_ context.Context, request textcompletion.Request) (textcompletion.Result, error) {
				codexRequest = request
				return textcompletion.Result{
					Text:     "优化后的人物图提示词",
					Executor: textcompletion.ExecutorCodex,
					Model:    "codex",
				}, nil
			}),
			func(context.Context, textcompletion.Request) bool { return true },
		)

		response, status, err := workflow.CreatePromptOptimizedGenerationMessage(context.Background(), GenerationMessageRequest{
			Kind:    string(coregeneration.KindImage),
			RouteID: coregeneration.RouteCodexImage,
			Prompt:  "用户原始人物提示词",
			PromptOptimization: &GenerationPromptOptimizationRequest{
				Executor:        string(textcompletion.ExecutorCodex),
				ReferenceName:   "人物立绘",
				ReferencePrompt: "电影质感",
			},
		})
		if err != nil || status != http.StatusOK {
			t.Fatalf("CreatePromptOptimizedGenerationMessage() status = %d error = %v", status, err)
		}
		if response.OptimizedPrompt != "优化后的人物图提示词" || response.Optimization.Text != "优化后的人物图提示词" {
			t.Fatalf("response = %+v, want existing optimizedPrompt and optimization history fields", response)
		}
		for _, required := range []string{
			"人物、场景和道具的身份",
			"构图",
			"媒介",
			"光线",
			"宽高比",
			"参考图的顺序和角色",
			"只输出优化后的提示词正文",
		} {
			if !strings.Contains(codexRequest.SystemInstruction, required) {
				t.Fatalf("image optimization system instruction = %q, want %q", codexRequest.SystemInstruction, required)
			}
		}
		request := waitForPromptSupplementsProviderRequest(t, provider.requests)
		if request.Prompt != "优化后的人物图提示词" {
			t.Fatalf("image prompt = %q, want optimized prompt", request.Prompt)
		}
		stored := waitForGenerationTask(t, workflow.generationTasks, response.Generation.ID, func(task GenerationTaskRecord) bool {
			return task.Status == "completed"
		})
		if stored.Prompt != "优化后的人物图提示词" || stored.RuntimeState.RevisedPrompt != "imagegen engine revision" {
			t.Fatalf("stored task prompt/runtime = %q/%+v, want optimizer output and separate imagegen revision", stored.Prompt, stored.RuntimeState)
		}
		for _, route := range workflow.ListGenerationModels().Routes {
			if route.Kind == coregeneration.KindText {
				t.Fatalf("visible catalog contains internal text optimization route %q", route.ID)
			}
		}
	})
}

func mediaLinkPromptOptimizationReady(context.Context, string) (bool, string) {
	return true, ""
}

type imagePromptOptimizationProvider struct {
	requests chan coregeneration.Request
}

func (*imagePromptOptimizationProvider) Name() string { return "image-prompt-optimization" }

func (provider *imagePromptOptimizationProvider) Generate(_ context.Context, request coregeneration.Request) (coregeneration.Response, error) {
	provider.requests <- request
	return coregeneration.Response{
		ID:     codexImageResponseIDPrefix + "prompt-optimization",
		Model:  request.Model,
		Status: "completed",
		Assets: []coregeneration.Asset{{
			Kind:     coregeneration.KindImage,
			MIMEType: "image/png",
			Base64:   base64.StdEncoding.EncodeToString(testPNGBytes()),
		}},
		Metadata: map[string]any{
			"runtime_state": GenerationTaskRuntimeState{RevisedPrompt: "imagegen engine revision"},
		},
	}, nil
}

func (*imagePromptOptimizationProvider) Get(context.Context, string) (coregeneration.Response, error) {
	return coregeneration.Response{}, nil
}
