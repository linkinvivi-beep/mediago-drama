import {
	uploadMediaAsset,
	type MediaAsset,
	type MediaAssetKind,
} from "@/domains/workspace/api/media";

type ReferenceFileKind = Exclude<MediaAssetKind, "text">;

export type ReferenceImportStatus = "uploaded" | "uploaded_incompatible" | "rejected" | "failed";

export interface ReferenceImportProgress {
	processed: number;
	total: number;
}

export interface ReferenceImportItemResult {
	asset?: MediaAsset;
	file: File;
	index: number;
	reason?: string;
	status: ReferenceImportStatus;
}

export interface ReferenceImportBatchResult {
	results: ReferenceImportItemResult[];
	selectableAssets: MediaAsset[];
	storedAssets: MediaAsset[];
}

interface ImportReferenceFilesOptions {
	availableSlots?: number;
	files: readonly File[];
	isUploadedAssetCompatible: (asset: MediaAsset) => boolean;
	onProgress?: (progress: ReferenceImportProgress) => void;
	projectId?: string | null;
	selectableKinds: ReadonlySet<MediaAssetKind>;
	upload?: (file: File, projectId?: string | null) => Promise<MediaAsset>;
}

const referenceExtensions: Record<ReferenceFileKind, ReadonlySet<string>> = {
	image: new Set(["png", "jpg", "jpeg", "webp", "gif", "bmp", "avif"]),
	video: new Set(["mp4", "webm", "mov", "m4v"]),
	audio: new Set(["mp3", "wav", "m4a", "aac", "ogg", "flac"]),
};

const referenceKindLabels: Record<ReferenceFileKind, string> = {
	audio: "音频",
	image: "图片",
	video: "视频",
};

const referenceFileKind = (file: File): ReferenceFileKind | null => {
	const mimeKind = file.type.trim().toLowerCase().split("/", 1)[0];
	if (mimeKind === "image" || mimeKind === "video" || mimeKind === "audio") return mimeKind;

	if (file.type.trim()) return null;
	const extension = file.name
		.trim()
		.toLowerCase()
		.match(/\.([^.]+)$/u)?.[1];
	if (!extension) return null;
	if (referenceExtensions.image.has(extension)) return "image";
	if (referenceExtensions.video.has(extension)) return "video";
	if (referenceExtensions.audio.has(extension)) return "audio";
	return null;
};

const uploadErrorMessage = (error: unknown) => {
	if (error instanceof Error && error.message.trim()) return error.message.trim();
	return "素材上传失败。";
};

export const importReferenceFiles = async ({
	availableSlots,
	files,
	isUploadedAssetCompatible,
	onProgress,
	projectId,
	selectableKinds,
	upload = uploadMediaAsset,
}: ImportReferenceFilesOptions): Promise<ReferenceImportBatchResult> => {
	const results: Array<ReferenceImportItemResult | undefined> = Array(files.length);
	const acceptedIndexes: number[] = [];
	const slotLimit =
		typeof availableSlots === "number" ? Math.max(0, Math.floor(availableSlots)) : undefined;

	for (const [index, file] of files.entries()) {
		if (file.size <= 0) {
			results[index] = {
				file,
				index,
				reason: "文件为空，无法作为参考素材。",
				status: "rejected",
			};
			continue;
		}

		const kind = referenceFileKind(file);
		if (!kind) {
			results[index] = {
				file,
				index,
				reason: "无法识别该文件的视觉素材类型。",
				status: "rejected",
			};
			continue;
		}
		if (!selectableKinds.has(kind)) {
			results[index] = {
				file,
				index,
				reason: `当前模型不支持${referenceKindLabels[kind]}参考素材。`,
				status: "rejected",
			};
			continue;
		}
		if (slotLimit !== undefined && acceptedIndexes.length >= slotLimit) {
			results[index] = {
				file,
				index,
				reason: "已超过当前模型可选择的参考素材数量。",
				status: "rejected",
			};
			continue;
		}
		acceptedIndexes.push(index);
	}

	let processed = results.reduce((count, result) => count + (result ? 1 : 0), 0);
	onProgress?.({ processed, total: files.length });

	let cursor = 0;
	const worker = async () => {
		while (cursor < acceptedIndexes.length) {
			const index = acceptedIndexes[cursor];
			cursor += 1;
			if (index === undefined) return;
			const file = files[index];
			if (!file) return;

			try {
				const asset = await upload(file, projectId);
				results[index] = isUploadedAssetCompatible(asset)
					? { asset, file, index, status: "uploaded" }
					: {
							asset,
							file,
							index,
							reason: "素材已入库，但不兼容当前模型。",
							status: "uploaded_incompatible",
						};
			} catch (error) {
				results[index] = {
					file,
					index,
					reason: uploadErrorMessage(error),
					status: "failed",
				};
			} finally {
				processed += 1;
				onProgress?.({ processed, total: files.length });
			}
		}
	};

	const workerCount = Math.min(2, acceptedIndexes.length);
	await Promise.all(Array.from({ length: workerCount }, () => worker()));

	const orderedResults = results.filter(
		(result): result is ReferenceImportItemResult => result !== undefined,
	);
	return {
		results: orderedResults,
		selectableAssets: orderedResults.flatMap((result) =>
			result.status === "uploaded" && result.asset ? [result.asset] : [],
		),
		storedAssets: orderedResults.flatMap((result) =>
			(result.status === "uploaded" || result.status === "uploaded_incompatible") && result.asset
				? [result.asset]
				: [],
		),
	};
};

export const mergeMediaAssetsByID = (
	current: readonly MediaAsset[],
	incoming: readonly MediaAsset[],
) => {
	const merged = [...current];
	const indexByID = new Map(merged.map((asset, index) => [asset.id, index]));
	for (const asset of incoming) {
		const existingIndex = indexByID.get(asset.id);
		if (existingIndex === undefined) {
			indexByID.set(asset.id, merged.length);
			merged.push(asset);
		} else {
			merged[existingIndex] = asset;
		}
	}
	return merged;
};

export const mediaAssetsInIDOrder = (ids: readonly string[], assets: readonly MediaAsset[]) => {
	const byID = new Map(assets.map((asset) => [asset.id, asset]));
	return ids.flatMap((id) => {
		const asset = byID.get(id);
		return asset ? [asset] : [];
	});
};

export const referenceImportIssueMessage = (batch: ReferenceImportBatchResult) => {
	const issues = batch.results.filter((result) => result.status !== "uploaded");
	if (issues.length === 0) return null;
	return `部分素材未能选为参考：${issues
		.map((result) => `${result.file.name}：${result.reason || "处理失败。"}`)
		.join("；")}`;
};
