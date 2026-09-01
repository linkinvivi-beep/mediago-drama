import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { GenerationRoute } from "@/domains/generation/api/generation";
import type { MediaAsset } from "@/domains/workspace/api/media";
import { uploadMediaAsset } from "@/domains/workspace/api/media";
import { useGenerationReferences } from "./useGenerationReferences";

vi.mock("@/domains/workspace/api/media", () => ({
	uploadMediaAsset: vi.fn(),
}));

const imageRoute: GenerationRoute = {
	adapter: "test.image",
	configured: true,
	docUrl: "https://example.test/image",
	familyId: "image-family",
	id: "image-route",
	async: false,
	kind: "image",
	label: "Image Route",
	model: "image-model",
	params: [],
	provider: "openai",
	status: "available",
	supportsReferenceUrls: true,
	versionId: "image-version",
};

const videoRoute: GenerationRoute = {
	...imageRoute,
	adapter: "openrouter.video",
	docUrl: "https://example.test/video",
	familyId: "video-family",
	id: "video-route",
	kind: "video",
	model: "video-model",
	provider: "openrouter",
	versionId: "video-version",
};
const libTVVideoRoute: GenerationRoute = {
	...videoRoute,
	adapter: "libtv.cli.video",
	id: "libtv-video-route",
	provider: "libtv",
};
const libTVImageRoute: GenerationRoute = {
	...imageRoute,
	adapter: "libtv.cli.image",
	id: "libtv-image-route",
	maxReferenceUrls: 2,
	provider: "libtv",
};
const unsupportedImageRoute: GenerationRoute = {
	...imageRoute,
	id: "unsupported-image-route",
	supportsReferenceUrls: false,
};

const mediaAsset = (overrides: Partial<MediaAsset> = {}): MediaAsset => ({
	createdAt: "2026-06-18T00:00:00.000Z",
	filename: "reference.png",
	id: "reference-image",
	kind: "image",
	mimeType: "image/png",
	sizeBytes: 1024,
	updatedAt: "2026-06-18T00:00:00.000Z",
	url: "/api/v1/media-assets/reference-image/content",
	...overrides,
});

const deferred = <T,>() => {
	let resolve!: (value: T) => void;
	const promise = new Promise<T>((resolvePromise) => {
		resolve = resolvePromise;
	});
	return { promise, resolve };
};

describe("useGenerationReferences", () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it("selects an uploaded video when the active video route supports video references", async () => {
		const uploadedVideo = mediaAsset({
			filename: "scene.mp4",
			id: "reference-video",
			kind: "video",
			mimeType: "video/mp4",
			url: "/api/v1/media-assets/reference-video/content",
		});
		vi.mocked(uploadMediaAsset).mockResolvedValue(uploadedVideo);

		const { result } = renderHook(() =>
			useGenerationReferences({
				extraReferenceAssetIds: [],
				extraReferenceUrls: [],
				mediaAssetProjectId: "project-a",
				mediaAssets: [uploadedVideo],
				mutateMediaAssets: vi.fn(),
				prompt: "镜头推进",
				selectedRoute: videoRoute,
				setError: vi.fn(),
			}),
		);

		await act(async () => {
			await result.current.importReferenceFiles([
				new File(["video"], "scene.mp4", { type: "video/mp4" }),
			]);
		});

		expect(result.current.selectedReferenceAssetIds).toEqual(["reference-video"]);
	});

	it("optimistically caches batch uploads and preserves selection order", async () => {
		const firstImage = mediaAsset({ filename: "first.png", id: "reference-first" });
		const secondImage = mediaAsset({ filename: "second.png", id: "reference-second" });
		vi.mocked(uploadMediaAsset).mockImplementation(async (file) =>
			file.name === "first.png" ? firstImage : secondImage,
		);
		const mutateMediaAssets = vi.fn().mockResolvedValue({ assets: [secondImage, firstImage] });

		const { result } = renderHook(() =>
			useGenerationReferences({
				extraReferenceAssetIds: [],
				extraReferenceUrls: [],
				mediaAssetProjectId: "project-a",
				mediaAssets: [secondImage, firstImage],
				mutateMediaAssets,
				prompt: "批量图片参考",
				selectedRoute: { ...imageRoute, maxReferenceUrls: 4 },
				setError: vi.fn(),
			}),
		);

		await act(async () => {
			await result.current.importReferenceFiles([
				new File(["first"], "first.png", { type: "image/png" }),
				new File(["second"], "second.png", { type: "image/png" }),
			]);
		});

		expect(result.current.selectedReferenceAssetIds).toEqual([
			"reference-first",
			"reference-second",
		]);
		expect(result.current.selectedReferenceAssets.map((asset) => asset.id)).toEqual([
			"reference-first",
			"reference-second",
		]);
		expect(mutateMediaAssets).toHaveBeenCalledWith(expect.any(Function), { revalidate: false });
		expect(result.current.referenceImportProgress).toBeNull();
	});

	it("rejects overlapping imports and does not select against a changed route", async () => {
		const uploadedImage = mediaAsset({ filename: "pending.png", id: "reference-pending" });
		const pendingUpload = deferred<MediaAsset>();
		vi.mocked(uploadMediaAsset).mockReturnValue(pendingUpload.promise);
		const mutateMediaAssets = vi.fn().mockResolvedValue({ assets: [uploadedImage] });
		const setError = vi.fn();
		const { result, rerender } = renderHook(
			({ selectedRoute }: { selectedRoute: GenerationRoute }) =>
				useGenerationReferences({
					extraReferenceAssetIds: [],
					extraReferenceUrls: [],
					mediaAssetProjectId: "project-a",
					mediaAssets: [uploadedImage],
					mutateMediaAssets,
					prompt: "切换模型",
					selectedRoute,
					setError,
				}),
			{ initialProps: { selectedRoute: imageRoute } },
		);

		let firstImport!: ReturnType<typeof result.current.importReferenceFiles>;
		act(() => {
			firstImport = result.current.importReferenceFiles([
				new File(["pending"], "pending.png", { type: "image/png" }),
			]);
		});
		await waitFor(() => expect(uploadMediaAsset).toHaveBeenCalledTimes(1));

		let overlappingResult: Awaited<ReturnType<typeof result.current.importReferenceFiles>> = null;
		await act(async () => {
			overlappingResult = await result.current.importReferenceFiles([
				new File(["other"], "other.png", { type: "image/png" }),
			]);
		});
		expect(overlappingResult).toBeNull();
		expect(uploadMediaAsset).toHaveBeenCalledTimes(1);

		rerender({ selectedRoute: { ...imageRoute, id: "image-route-next" } });
		pendingUpload.resolve(uploadedImage);
		await act(async () => {
			await firstImport;
		});

		expect(result.current.selectedReferenceAssetIds).toEqual([]);
		expect(mutateMediaAssets).toHaveBeenCalledWith(expect.any(Function), { revalidate: false });
		expect(setError).toHaveBeenCalledWith("模型已切换，素材已入库，请按当前模型重新选择。");
	});

	it("allows LibTV routes to select image video and audio references", () => {
		const referenceImage = mediaAsset({ id: "reference-image", kind: "image" });
		const referenceVideo = mediaAsset({
			filename: "scene.mp4",
			id: "reference-video",
			kind: "video",
			mimeType: "video/mp4",
			url: "/api/v1/media-assets/reference-video/content",
		});
		const referenceAudio = mediaAsset({
			filename: "voice.wav",
			id: "reference-audio",
			kind: "audio",
			mimeType: "audio/wav",
			url: "/api/v1/media-assets/reference-audio/content",
		});

		const { result } = renderHook(() =>
			useGenerationReferences({
				extraReferenceAssetIds: [],
				extraReferenceUrls: [],
				mediaAssetProjectId: "project-a",
				mediaAssets: [referenceImage, referenceVideo, referenceAudio],
				mutateMediaAssets: vi.fn(),
				prompt: "多模态参考",
				selectedRoute: libTVVideoRoute,
				setError: vi.fn(),
			}),
		);

		expect(Array.from(result.current.selectableReferenceKinds).sort()).toEqual([
			"audio",
			"image",
			"video",
		]);

		act(() => {
			result.current.selectReferenceAsset(referenceImage);
			result.current.selectReferenceAsset(referenceVideo);
			result.current.selectReferenceAsset(referenceAudio);
		});

		expect(result.current.selectedReferenceAssetIds).toEqual([
			"reference-image",
			"reference-video",
			"reference-audio",
		]);
	});

	it("limits LibTV image routes to image references and honors the route maximum", () => {
		const firstImage = mediaAsset({ id: "reference-image-a" });
		const secondImage = mediaAsset({ id: "reference-image-b" });
		const thirdImage = mediaAsset({ id: "reference-image-c" });
		const referenceVideo = mediaAsset({
			filename: "scene.mp4",
			id: "reference-video",
			kind: "video",
			mimeType: "video/mp4",
			url: "/api/v1/media-assets/reference-video/content",
		});
		const referenceAudio = mediaAsset({
			filename: "voice.wav",
			id: "reference-audio",
			kind: "audio",
			mimeType: "audio/wav",
			url: "/api/v1/media-assets/reference-audio/content",
		});
		const setError = vi.fn();
		const { result } = renderHook(() =>
			useGenerationReferences({
				extraReferenceAssetIds: [],
				extraReferenceUrls: [],
				mediaAssetProjectId: "project-a",
				mediaAssets: [firstImage, secondImage, thirdImage, referenceVideo, referenceAudio],
				mutateMediaAssets: vi.fn(),
				prompt: "图片参考",
				selectedRoute: libTVImageRoute,
				setError,
			}),
		);

		expect(Array.from(result.current.selectableReferenceKinds)).toEqual(["image"]);

		act(() => {
			result.current.selectReferenceAsset(firstImage);
			result.current.selectReferenceAsset(referenceVideo);
			result.current.selectReferenceAsset(referenceAudio);
			result.current.selectReferenceAsset(secondImage);
			result.current.selectReferenceAsset(thirdImage);
		});

		expect(result.current.selectedReferenceAssetIds).toEqual([
			"reference-image-a",
			"reference-image-b",
		]);
		expect(setError).toHaveBeenCalledWith("当前模型最多支持 2 个参考素材。");
	});

	it("does not upload a video for image-only reference routes", async () => {
		const setError = vi.fn();
		const { result } = renderHook(() =>
			useGenerationReferences({
				extraReferenceAssetIds: [],
				extraReferenceUrls: [],
				mediaAssetProjectId: "project-a",
				mediaAssets: [],
				mutateMediaAssets: vi.fn(),
				prompt: "生成图片",
				selectedRoute: imageRoute,
				setError,
			}),
		);

		await act(async () => {
			await result.current.importReferenceFiles([
				new File(["video"], "scene.mp4", { type: "video/mp4" }),
			]);
		});

		expect(uploadMediaAsset).not.toHaveBeenCalled();
		expect(result.current.selectedReferenceAssetIds).toEqual([]);
		expect(setError).toHaveBeenCalledWith(
			"部分素材未能选为参考：scene.mp4：当前模型不支持视频参考素材。",
		);
	});

	it("drops selected references when the active route no longer supports them", async () => {
		const referenceImage = mediaAsset();
		const { result, rerender } = renderHook(
			({ selectedRoute }: { selectedRoute: GenerationRoute }) =>
				useGenerationReferences({
					extraReferenceAssetIds: [],
					extraReferenceUrls: [],
					mediaAssetProjectId: "project-a",
					mediaAssets: [referenceImage],
					mutateMediaAssets: vi.fn(),
					prompt: "生成图片",
					selectedRoute,
					setError: vi.fn(),
				}),
			{ initialProps: { selectedRoute: imageRoute } },
		);

		act(() => {
			result.current.selectReferenceAsset(referenceImage);
		});
		expect(result.current.selectedReferenceAssetIds).toEqual(["reference-image"]);

		rerender({ selectedRoute: unsupportedImageRoute });

		await waitFor(() => expect(result.current.selectedReferenceAssetIds).toEqual([]));
	});

	it("does not add references beyond the selected route limit", () => {
		const firstImage = mediaAsset({ id: "reference-a" });
		const secondImage = mediaAsset({ id: "reference-b" });
		const setError = vi.fn();
		const { result } = renderHook(() =>
			useGenerationReferences({
				extraReferenceAssetIds: [],
				extraReferenceUrls: [],
				mediaAssetProjectId: "project-a",
				mediaAssets: [firstImage, secondImage],
				mutateMediaAssets: vi.fn(),
				prompt: "生成图片",
				selectedRoute: { ...imageRoute, maxReferenceUrls: 1 },
				setError,
			}),
		);

		act(() => {
			result.current.selectReferenceAsset(firstImage);
			result.current.selectReferenceAsset(secondImage);
		});

		expect(result.current.selectedReferenceAssetIds).toEqual(["reference-a"]);
		expect(setError).toHaveBeenCalledWith("当前模型最多支持 1 个参考素材。");
	});

	it("counts extra reference URLs against the selected route limit", () => {
		const referenceImage = mediaAsset({ id: "reference-a" });
		const setError = vi.fn();
		const { result } = renderHook(() =>
			useGenerationReferences({
				extraReferenceAssetIds: [],
				extraReferenceUrls: ["https://example.test/reference.png"],
				mediaAssetProjectId: "project-a",
				mediaAssets: [referenceImage],
				mutateMediaAssets: vi.fn(),
				prompt: "生成图片",
				selectedRoute: { ...imageRoute, maxReferenceUrls: 1 },
				setError,
			}),
		);

		act(() => {
			result.current.selectReferenceAsset(referenceImage);
		});

		expect(result.current.referenceCount).toBe(1);
		expect(result.current.selectedReferenceAssetIds).toEqual([]);
		expect(setError).toHaveBeenCalledWith("当前模型最多支持 1 个参考素材。");
	});
});
