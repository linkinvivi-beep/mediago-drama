import { describe, expect, it, vi } from "vitest";
import type { MediaAsset } from "@/domains/workspace/api/media";
import {
	importReferenceFiles,
	mediaAssetsInIDOrder,
	mergeMediaAssetsByID,
	referenceImportIssueMessage,
} from "./reference-file-import";

const mediaAsset = (overrides: Partial<MediaAsset> = {}): MediaAsset => ({
	createdAt: "2026-09-02T00:00:00.000Z",
	filename: "reference.png",
	id: "asset-reference",
	kind: "image",
	mimeType: "image/png",
	sizeBytes: 1024,
	updatedAt: "2026-09-02T00:00:00.000Z",
	url: "/api/v1/media-assets/asset-reference/content",
	...overrides,
});

const deferred = <T>() => {
	let resolve!: (value: T) => void;
	let reject!: (reason?: unknown) => void;
	const promise = new Promise<T>((resolvePromise, rejectPromise) => {
		resolve = resolvePromise;
		reject = rejectPromise;
	});
	return { promise, reject, resolve };
};

describe("importReferenceFiles", () => {
	it("rejects unsupported and overflow files before upload", async () => {
		const upload = vi.fn().mockRejectedValue(new Error("upload failed"));
		const result = await importReferenceFiles({
			availableSlots: 1,
			files: [
				new File(["png"], "first.png", { type: "image/png" }),
				new File(["mp4"], "clip.mp4", { type: "video/mp4" }),
				new File(["png"], "second.png", { type: "image/png" }),
			],
			isUploadedAssetCompatible: (asset) => asset.kind === "image",
			projectId: "project-a",
			selectableKinds: new Set(["image"]),
			upload,
		});

		expect(upload).toHaveBeenCalledTimes(1);
		expect(upload).toHaveBeenCalledWith(
			expect.objectContaining({ name: "first.png" }),
			"project-a",
		);
		expect(result.results.map((item) => item.status)).toEqual(["failed", "rejected", "rejected"]);
		expect(result.results[1]?.reason).toBe("当前模型不支持视频参考素材。");
		expect(result.results[2]?.reason).toBe("已超过当前模型可选择的参考素材数量。");
	});

	it("uses supported extensions only when the browser MIME type is empty", async () => {
		const uploaded = mediaAsset({ filename: "reference.JPG", id: "asset-jpg" });
		const upload = vi.fn().mockResolvedValue(uploaded);
		const result = await importReferenceFiles({
			files: [
				new File(["jpeg"], "reference.JPG"),
				new File([], "empty.png", { type: "image/png" }),
				new File(["data"], "unknown.bin"),
			],
			isUploadedAssetCompatible: (asset) => asset.kind === "image",
			selectableKinds: new Set(["image"]),
			upload,
		});

		expect(upload).toHaveBeenCalledTimes(1);
		expect(result.results.map((item) => item.status)).toEqual(["uploaded", "rejected", "rejected"]);
		expect(result.results[1]?.reason).toBe("文件为空，无法作为参考素材。");
		expect(result.results[2]?.reason).toBe("无法识别该文件的视觉素材类型。");
	});

	it("limits uploads to two concurrent requests and preserves input order", async () => {
		const pending = {
			"first.png": deferred<MediaAsset>(),
			"second.png": deferred<MediaAsset>(),
			"third.png": deferred<MediaAsset>(),
		};
		let active = 0;
		let maxActive = 0;
		const upload = vi.fn(async (file: File) => {
			active += 1;
			maxActive = Math.max(maxActive, active);
			try {
				return await pending[file.name as keyof typeof pending].promise;
			} finally {
				active -= 1;
			}
		});
		const onProgress = vi.fn();
		const resultPromise = importReferenceFiles({
			files: [
				new File(["1"], "first.png", { type: "image/png" }),
				new File(["2"], "second.png", { type: "image/png" }),
				new File(["3"], "third.png", { type: "image/png" }),
			],
			isUploadedAssetCompatible: (asset) => asset.kind === "image",
			onProgress,
			selectableKinds: new Set(["image"]),
			upload,
		});

		await vi.waitFor(() => expect(upload).toHaveBeenCalledTimes(2));
		pending["second.png"].resolve(mediaAsset({ filename: "second.png", id: "asset-2" }));
		await vi.waitFor(() => expect(upload).toHaveBeenCalledTimes(3));
		pending["third.png"].resolve(mediaAsset({ filename: "third.png", id: "asset-3" }));
		pending["first.png"].resolve(mediaAsset({ filename: "first.png", id: "asset-1" }));

		const result = await resultPromise;
		expect(maxActive).toBe(2);
		expect(result.selectableAssets.map((asset) => asset.id)).toEqual([
			"asset-1",
			"asset-2",
			"asset-3",
		]);
		expect(onProgress).toHaveBeenLastCalledWith({ processed: 3, total: 3 });
	});

	it("keeps a server-detected incompatible upload in storage without selecting it", async () => {
		const uploadedVideo = mediaAsset({
			filename: "spoofed.mp4",
			id: "asset-video",
			kind: "video",
			mimeType: "video/mp4",
		});
		const result = await importReferenceFiles({
			files: [new File(["content"], "spoofed.png", { type: "image/png" })],
			isUploadedAssetCompatible: (asset) => asset.kind === "image",
			selectableKinds: new Set(["image"]),
			upload: vi.fn().mockResolvedValue(uploadedVideo),
		});

		expect(result.results[0]).toMatchObject({
			asset: uploadedVideo,
			reason: "素材已入库，但不兼容当前模型。",
			status: "uploaded_incompatible",
		});
		expect(result.storedAssets).toEqual([uploadedVideo]);
		expect(result.selectableAssets).toEqual([]);
	});
});

describe("reference import helpers", () => {
	it("merges returned assets by id and resolves selected assets in id order", () => {
		const first = mediaAsset({ filename: "first.png", id: "asset-1" });
		const second = mediaAsset({ filename: "old.png", id: "asset-2" });
		const updatedSecond = mediaAsset({ filename: "new.png", id: "asset-2" });
		const third = mediaAsset({ filename: "third.png", id: "asset-3" });

		const merged = mergeMediaAssetsByID([first, second], [updatedSecond, third]);

		expect(merged.map((asset) => `${asset.id}:${asset.filename}`)).toEqual([
			"asset-1:first.png",
			"asset-2:new.png",
			"asset-3:third.png",
		]);
		expect(mediaAssetsInIDOrder(["asset-3", "asset-1", "missing"], merged)).toEqual([third, first]);
	});

	it("reports every file that was not selected and stays quiet on full success", () => {
		const successFile = new File(["ok"], "ok.png", { type: "image/png" });
		const rejectedFile = new File(["bad"], "bad.mov", { type: "video/quicktime" });
		const failedFile = new File(["fail"], "fail.png", { type: "image/png" });

		expect(
			referenceImportIssueMessage({
				results: [
					{
						asset: mediaAsset(),
						file: successFile,
						index: 0,
						status: "uploaded",
					},
				],
				selectableAssets: [mediaAsset()],
				storedAssets: [mediaAsset()],
			}),
		).toBeNull();

		expect(
			referenceImportIssueMessage({
				results: [
					{
						file: rejectedFile,
						index: 0,
						reason: "当前模型不支持视频参考素材。",
						status: "rejected",
					},
					{
						file: failedFile,
						index: 1,
						reason: "网络错误",
						status: "failed",
					},
				],
				selectableAssets: [],
				storedAssets: [],
			}),
		).toBe("部分素材未能选为参考：bad.mov：当前模型不支持视频参考素材。；fail.png：网络错误");
	});
});
