import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { KeyedMutator } from "swr";
import type { MediaAsset, MediaAssetsResponse } from "@/domains/workspace/api/media";
import type { GenerationRoute } from "@/domains/generation/api/generation";
import {
	importReferenceFiles as runReferenceFileImport,
	mediaAssetsInIDOrder,
	mergeMediaAssetsByID,
	referenceImportIssueMessage,
	type ReferenceImportBatchResult,
	type ReferenceImportProgress,
} from "@/domains/generation/lib/reference-file-import";
import {
	canUseAssetAsReference,
	maxReferenceUrlsForRoute,
	referenceKindsForRoute,
	resolveGenerationExtraValue,
	uniqueStrings,
	type GenerationExtraValue,
} from "./useGenerationWorkspace.helpers";

interface UseGenerationReferencesOptions {
	extraReferenceAssetIds: GenerationExtraValue<string[]>;
	extraReferenceUrls: GenerationExtraValue<string[]>;
	mediaAssetProjectId: string;
	mediaAssets: MediaAsset[];
	mutateMediaAssets: KeyedMutator<MediaAssetsResponse>;
	prompt: string;
	selectedRoute: GenerationRoute;
	setError: (message: string | null) => void;
}

export const useGenerationReferences = ({
	extraReferenceAssetIds,
	extraReferenceUrls,
	mediaAssetProjectId,
	mediaAssets,
	mutateMediaAssets,
	prompt,
	selectedRoute,
	setError,
}: UseGenerationReferencesOptions) => {
	const [selectedReferenceAssetIds, setSelectedReferenceAssetIds] = useState<string[]>([]);
	const [isUploadingAsset, setIsUploadingAsset] = useState(false);
	const [referenceImportProgress, setReferenceImportProgress] =
		useState<ReferenceImportProgress | null>(null);
	const importInFlightRef = useRef(false);
	const isMountedRef = useRef(true);
	const selectedRouteIDRef = useRef(selectedRoute.id);
	selectedRouteIDRef.current = selectedRoute.id;
	const selectableReferenceKinds = useMemo(
		() => referenceKindsForRoute(selectedRoute),
		[selectedRoute],
	);
	const maxReferenceUrls = maxReferenceUrlsForRoute(selectedRoute);
	const selectedReferenceAssets = useMemo(
		() => mediaAssetsInIDOrder(selectedReferenceAssetIds, mediaAssets),
		[mediaAssets, selectedReferenceAssetIds],
	);
	const resolvedExtraReferenceAssetIds = useMemo(
		() => resolveGenerationExtraValue(extraReferenceAssetIds, prompt),
		[extraReferenceAssetIds, prompt],
	);
	const resolvedExtraReferenceUrls = useMemo(
		() => resolveGenerationExtraValue(extraReferenceUrls, prompt),
		[extraReferenceUrls, prompt],
	);
	const effectiveReferenceAssetIds = useMemo(
		() => uniqueStrings([...selectedReferenceAssetIds, ...resolvedExtraReferenceAssetIds]),
		[resolvedExtraReferenceAssetIds, selectedReferenceAssetIds],
	);
	const effectiveReferenceUrls = useMemo(
		() => uniqueStrings(resolvedExtraReferenceUrls.map((url) => url.trim()).filter(Boolean)),
		[resolvedExtraReferenceUrls],
	);
	const referenceCountForAssetIds = useCallback(
		(assetIds: string[]) =>
			uniqueStrings([...assetIds, ...resolvedExtraReferenceAssetIds]).length +
			effectiveReferenceUrls.length,
		[effectiveReferenceUrls, resolvedExtraReferenceAssetIds],
	);
	const referenceCount = referenceCountForAssetIds(selectedReferenceAssetIds);
	const trimReferenceAssetIdsToLimit = useCallback(
		(assetIds: string[]) => {
			if (!maxReferenceUrls) return assetIds;

			const next: string[] = [];
			for (const assetId of assetIds) {
				const candidate = [...next, assetId];
				if (referenceCountForAssetIds(candidate) <= maxReferenceUrls) {
					next.push(assetId);
				}
			}
			return next;
		},
		[maxReferenceUrls, referenceCountForAssetIds],
	);
	const referenceLimitMessage = useCallback(
		() => `当前模型最多支持 ${maxReferenceUrls} 个参考素材。`,
		[maxReferenceUrls],
	);
	const addReferenceAssetIdWithinLimit = useCallback(
		(current: string[], assetId: string) => {
			if (current.includes(assetId)) return current;
			const next = [...current, assetId];
			if (maxReferenceUrls && referenceCountForAssetIds(next) > maxReferenceUrls) {
				setError(referenceLimitMessage());
				return current;
			}
			return next;
		},
		[maxReferenceUrls, referenceCountForAssetIds, referenceLimitMessage, setError],
	);

	useEffect(() => {
		isMountedRef.current = true;
		return () => {
			isMountedRef.current = false;
		};
	}, []);

	useEffect(() => {
		if (mediaAssets.length === 0) {
			setSelectedReferenceAssetIds((current) => (current.length === 0 ? current : []));
			return;
		}

		const validIDs = new Set(
			mediaAssets
				.filter((asset) => canUseAssetAsReference(asset, selectedRoute, selectableReferenceKinds))
				.map((asset) => asset.id),
		);
		setSelectedReferenceAssetIds((current) => {
			const next = trimReferenceAssetIdsToLimit(current.filter((id) => validIDs.has(id)));
			return sameStringList(current, next) ? current : next;
		});
	}, [mediaAssets, selectableReferenceKinds, selectedRoute, trimReferenceAssetIdsToLimit]);

	const removeReferenceAsset = useCallback((assetId: string) => {
		setSelectedReferenceAssetIds((current) => current.filter((id) => id !== assetId));
	}, []);

	const selectReferenceAsset = useCallback(
		(asset: MediaAsset) => {
			if (!canUseAssetAsReference(asset, selectedRoute, selectableReferenceKinds)) return;

			setSelectedReferenceAssetIds((current) => addReferenceAssetIdWithinLimit(current, asset.id));
		},
		[addReferenceAssetIdWithinLimit, selectableReferenceKinds, selectedRoute],
	);

	const toggleReferenceAsset = useCallback(
		(asset: MediaAsset) => {
			if (!canUseAssetAsReference(asset, selectedRoute, selectableReferenceKinds)) return;

			setSelectedReferenceAssetIds((current) => {
				if (current.includes(asset.id)) return current.filter((id) => id !== asset.id);
				return addReferenceAssetIdWithinLimit(current, asset.id);
			});
		},
		[addReferenceAssetIdWithinLimit, selectableReferenceKinds, selectedRoute],
	);

	const importReferenceFiles = useCallback(
		async (files: readonly File[]): Promise<ReferenceImportBatchResult | null> => {
			if (importInFlightRef.current || files.length === 0) return null;
			importInFlightRef.current = true;
			const routeID = selectedRoute.id;
			const availableSlots = maxReferenceUrls
				? Math.max(0, maxReferenceUrls - referenceCount)
				: undefined;
			setIsUploadingAsset(true);
			setReferenceImportProgress({ processed: 0, total: files.length });
			setError(null);

			try {
				const batch = await runReferenceFileImport({
					availableSlots,
					files,
					isUploadedAssetCompatible: (asset) =>
						canUseAssetAsReference(asset, selectedRoute, selectableReferenceKinds),
					onProgress: (progress) => {
						if (isMountedRef.current) setReferenceImportProgress(progress);
					},
					projectId: mediaAssetProjectId,
					selectableKinds: selectableReferenceKinds,
				});

				if (batch.storedAssets.length > 0) {
					await mutateMediaAssets(
						(current) => ({
							assets: mergeMediaAssetsByID(current?.assets ?? mediaAssets, batch.storedAssets),
						}),
						{ revalidate: false },
					);
					void Promise.resolve(mutateMediaAssets()).catch(() => {
						if (isMountedRef.current) {
							setError("素材已入库，但素材列表刷新失败，请稍后重试。");
						}
					});
				}

				if (!isMountedRef.current) return batch;
				if (selectedRouteIDRef.current !== routeID) {
					setError("模型已切换，素材已入库，请按当前模型重新选择。");
					return batch;
				}

				const importedIDs = batch.selectableAssets.map((asset) => asset.id);
				if (importedIDs.length > 0) {
					setSelectedReferenceAssetIds((current) =>
						trimReferenceAssetIdsToLimit(uniqueStrings([...current, ...importedIDs])),
					);
				}
				setError(referenceImportIssueMessage(batch));
				return batch;
			} finally {
				importInFlightRef.current = false;
				if (isMountedRef.current) {
					setIsUploadingAsset(false);
					setReferenceImportProgress(null);
				}
			}
		},
		[
			maxReferenceUrls,
			mediaAssetProjectId,
			mediaAssets,
			mutateMediaAssets,
			referenceCount,
			selectableReferenceKinds,
			selectedRoute,
			setError,
			trimReferenceAssetIdsToLimit,
		],
	);

	return {
		effectiveReferenceAssetIds,
		effectiveReferenceUrls,
		isUploadingAsset,
		importReferenceFiles,
		referenceCount,
		referenceImportProgress,
		removeReferenceAsset,
		selectReferenceAsset,
		selectableReferenceKinds,
		selectedReferenceAssetIds,
		selectedReferenceAssets,
		toggleReferenceAsset,
	};
};

const sameStringList = (left: string[], right: string[]) =>
	left.length === right.length && left.every((value, index) => value === right[index]);
