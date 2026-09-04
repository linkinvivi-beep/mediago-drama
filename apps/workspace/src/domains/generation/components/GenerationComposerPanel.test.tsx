import { cleanup, createEvent, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GenerationComposerPanel } from "./GenerationComposerPanel";

describe("GenerationComposerPanel", () => {
	afterEach(cleanup);

	it("renders copy prompt as an icon-only toolbar button before parameter controls", () => {
		const onCopyPrompt = vi.fn();

		render(
			<GenerationComposerPanel
				canCopyPrompt
				canSubmit
				isSubmitting={false}
				promptInput={<div data-testid="prompt-input" className="prompt-input" />}
				rightControls={<button type="button">参数</button>}
				submitLabel="生成"
				onCopyPrompt={onCopyPrompt}
			/>,
		);

		const copyButton = screen.getByRole("button", { name: "复制 Prompt" });
		const paramButton = screen.getByRole("button", { name: "参数" });

		expect(screen.queryByText("复制 Prompt")).toBeNull();
		expect(copyButton.className).not.toContain("absolute");
		expect(copyButton.className).toContain("h-[var(--generation-control-height)]");
		expect(copyButton.className).toContain("w-[var(--generation-control-height)]");
		expect(copyButton.className).toContain("bg-muted");
		expect(screen.getByTestId("prompt-input")).not.toHaveClass("pb-12");
		expect(copyButton.compareDocumentPosition(paramButton)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);

		fireEvent.click(copyButton);

		expect(onCopyPrompt).toHaveBeenCalledTimes(1);
	});

	it("imports dropped reference files without submitting generation", () => {
		const onImportReferenceFiles = vi.fn();
		const onSubmit = vi.fn((event: React.FormEvent) => event.preventDefault());
		const first = new File(["a"], "a.png", { type: "image/png" });
		const second = new File(["b"], "b.png", { type: "image/png" });
		render(
			<form onSubmit={onSubmit}>
				<GenerationComposerPanel
					canSelectReference
					canSubmit
					isSubmitting={false}
					onImportReferenceFiles={onImportReferenceFiles}
					promptInput={<textarea aria-label="Prompt" />}
					submitLabel="生成"
				/>
			</form>,
		);
		const card = screen.getByTestId("generation-composer-drop-target");
		const transfer = { files: [first, second], types: ["Files"] };

		fireEvent.dragEnter(card, { dataTransfer: transfer });
		expect(screen.getByText("松开以加入素材库并设为参考素材")).toBeTruthy();
		fireEvent.drop(card, { dataTransfer: transfer });

		expect(onImportReferenceFiles).toHaveBeenCalledWith([first, second]);
		expect(onSubmit).not.toHaveBeenCalled();
	});

	it("shows import progress and does not claim disabled or internal drags", () => {
		const onImportReferenceFiles = vi.fn();
		const { rerender } = render(
			<GenerationComposerPanel
				canSelectReference
				canSubmit
				isImportingReferences
				isSubmitting={false}
				onImportReferenceFiles={onImportReferenceFiles}
				promptInput={<textarea aria-label="Prompt" />}
				referenceImportProgress={{ processed: 2, total: 5 }}
				submitLabel="生成"
			/>,
		);
		expect(screen.getByText("正在导入 2/5")).toBeTruthy();

		rerender(
			<GenerationComposerPanel
				canSelectReference={false}
				canSubmit
				isSubmitting={false}
				onImportReferenceFiles={onImportReferenceFiles}
				promptInput={<textarea aria-label="Prompt" />}
				submitLabel="生成"
			/>,
		);
		const card = screen.getByTestId("generation-composer-drop-target");
		const fileDrop = createEvent.drop(card, {
			dataTransfer: {
				files: [new File(["a"], "a.png", { type: "image/png" })],
				types: ["Files"],
			},
		});
		fireEvent(card, fileDrop);
		expect(fileDrop.defaultPrevented).toBe(false);

		rerender(
			<GenerationComposerPanel
				canSelectReference
				canSubmit
				isSubmitting={false}
				onImportReferenceFiles={onImportReferenceFiles}
				promptInput={<textarea aria-label="Prompt" />}
				submitLabel="生成"
			/>,
		);
		const textDrag = createEvent.dragEnter(card, {
			dataTransfer: { files: [], types: ["text/plain"] },
		});
		fireEvent(card, textDrag);
		expect(textDrag.defaultPrevented).toBe(false);
		expect(screen.queryByText("松开以加入素材库并设为参考素材")).toBeNull();
		expect(onImportReferenceFiles).not.toHaveBeenCalled();
	});
});
