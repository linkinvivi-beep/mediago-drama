import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GenerationRoute } from "@/domains/generation/api/generation";
import { usePromptOptimize } from "./usePromptOptimize";

const mocks = vi.hoisted(() => ({
	getCodexAccount: vi.fn(),
	streamGenerationText: vi.fn(),
}));

vi.mock("@/domains/settings/api/settings", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@/domains/settings/api/settings")>();
	return { ...actual, getCodexAccount: mocks.getCodexAccount };
});

vi.mock("@/domains/generation/api/generation", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@/domains/generation/api/generation")>();
	return { ...actual, streamGenerationText: mocks.streamGenerationText };
});

const Harness = () => {
	const optimizer = usePromptOptimize({
		catalog: { families: [], models: [], providers: [], routes: [], versions: [] },
		documentContext: {
			documentId: "storyboard-doc",
			projectId: "project-1",
			sectionId: "shot-1",
		},
		kind: "image",
		onOptimized: vi.fn(),
		projectId: "project-1",
		referenceAssetIds: ["character-asset"],
		referenceBindings: [{ assetId: "character-asset", documentId: "character-doc" }],
		referenceUrls: ["https://example.test/character.png"],
		targetRoute: { id: "target-image-route", kind: "image" } as GenerationRoute,
	});
	return (
		<button
			type="button"
			disabled={!optimizer.canOptimize}
			onClick={() =>
				void optimizer.optimize({
					currentPrompt: "a hero",
					referenceName: "cinematic",
					referencePrompt: "cinematic lighting",
				})
			}
		>
			{optimizer.codexAvailable ? "Codex ready" : "Unavailable"}
		</button>
	);
};

const CatalogHarness = ({ preferCodex }: { preferCodex: boolean }) => {
	const optimizer = usePromptOptimize({
		catalog: {
			families: [{ id: "gpt", kind: "text", label: "GPT" }],
			models: [],
			providers: [],
			routes: [
				{
					adapter: "openai.responses",
					async: false,
					configured: true,
					docUrl: "",
					familyId: "gpt",
					id: "route-gpt",
					kind: "text",
					label: "GPT",
					model: "gpt",
					params: [],
					provider: "openai",
					status: "available",
					supportsReferenceUrls: false,
					versionId: "gpt-v1",
				},
			],
			versions: [
				{
					canonicalModel: "gpt",
					capabilities: { async: false, supportsReferenceUrls: false },
					familyId: "gpt",
					id: "gpt-v1",
					kind: "text",
					label: "GPT",
				},
			],
		},
		onOptimized: vi.fn(),
		preferCodex,
	});
	return (
		<button
			type="button"
			disabled={!optimizer.canOptimize}
			onClick={() =>
				void optimizer.optimize({
					currentPrompt: "a hero",
					referenceName: "cinematic",
					referencePrompt: "cinematic lighting",
				})
			}
		>
			Optimize
		</button>
	);
};

describe("usePromptOptimize", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		mocks.getCodexAccount.mockResolvedValue({
			codexHome: "/tmp/codex",
			shared: true,
			status: "loggedIn",
		});
		mocks.streamGenerationText.mockImplementation(async (_request, handlers) => {
			handlers.onDone?.({ text: "optimized", status: "completed" });
		});
	});

	it("uses Codex when no configured text route exists", async () => {
		render(
			<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}>
				<Harness />
			</SWRConfig>,
		);

		const button = await screen.findByRole("button", { name: "Codex ready" });
		fireEvent.click(button);

		await waitFor(() => expect(mocks.streamGenerationText).toHaveBeenCalledTimes(1));
		expect(mocks.streamGenerationText.mock.calls[0]?.[0]).toMatchObject({
			documentContext: {
				documentId: "storyboard-doc",
				projectId: "project-1",
				sectionId: "shot-1",
			},
			documentId: "storyboard-doc",
			kind: "text",
			model: "",
			projectId: "project-1",
			prompt: "a hero",
			promptOptimization: {
				referenceName: "cinematic",
				referencePrompt: "cinematic lighting",
			},
			referenceAssetIds: ["character-asset"],
			referenceBindings: [{ assetId: "character-asset", documentId: "character-doc" }],
			referenceUrls: ["https://example.test/character.png"],
			routeId: "",
			sectionId: "shot-1",
			textExecutor: "codex",
		});
		expect(mocks.streamGenerationText.mock.calls[0]?.[0].params).toMatchObject({
			_mediago_prompt_optimization_target_kind: "image",
			_mediago_prompt_optimization_target_route: "target-image-route",
		});
		expect(mocks.streamGenerationText.mock.calls[0]?.[0].params).not.toHaveProperty(
			"system_instruction",
		);
	});

	it("uses Codex when it is preferred even if a configured text route exists", async () => {
		render(
			<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}>
				<CatalogHarness preferCodex />
			</SWRConfig>,
		);

		fireEvent.click(await screen.findByRole("button", { name: "Optimize" }));

		await waitFor(() => expect(mocks.streamGenerationText).toHaveBeenCalledTimes(1));
		expect(mocks.streamGenerationText.mock.calls[0]?.[0]).toMatchObject({
			routeId: "",
			textExecutor: "codex",
		});
	});
});
