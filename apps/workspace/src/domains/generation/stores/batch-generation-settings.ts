import type { GenerationKind } from "@/domains/generation/api/generation";
import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { immer } from "zustand/middleware/immer";

export type BatchGenerationDialogKind = Extract<GenerationKind, "image" | "video">;

export interface BatchGenerationStoredSettings {
	familyId?: string;
	instanceProfileId?: string;
	params?: Record<string, unknown>;
	promptOptimizeItemId?: string;
	promptOptimizeRouteId?: string;
	promptSupplementItemIds?: string[];
	routeId?: string;
	usePromptOptimization?: boolean;
	usePromptSupplement?: boolean;
	versionId?: string;
	workflowParameters?: Record<string, string | number | boolean>;
	workflowProfileId?: string;
}

export type BatchGenerationStoredSettingsMap = Partial<
	Record<BatchGenerationDialogKind, BatchGenerationStoredSettings>
>;

interface BatchGenerationSettingsPreferenceState {
	settingsByKind: BatchGenerationStoredSettingsMap;
	setSettings: (kind: BatchGenerationDialogKind, settings: BatchGenerationStoredSettings) => void;
}

export const batchGenerationSettingsStorageKey = "generation.batch-settings.v1";

export const batchGenerationPromptOptimizationDefaultEnabled = false;
export const batchGenerationPromptSupplementDefaultEnabled = false;

export const batchGenerationPromptOptimizationEnabled = (
	settings: BatchGenerationStoredSettings | null | undefined,
) =>
	typeof settings?.usePromptOptimization === "boolean"
		? settings.usePromptOptimization
		: batchGenerationPromptOptimizationDefaultEnabled;

export const batchGenerationPromptSupplementEnabled = (
	settings: BatchGenerationStoredSettings | null | undefined,
) =>
	typeof settings?.usePromptSupplement === "boolean"
		? settings.usePromptSupplement
		: batchGenerationPromptSupplementDefaultEnabled;

export const useBatchGenerationSettingsPreferenceStore =
	create<BatchGenerationSettingsPreferenceState>()(
		persist(
			immer((set) => ({
				settingsByKind: readLegacyBatchGenerationSettings(),
				setSettings: (kind, settings) =>
					set((state) => {
						state.settingsByKind[kind] = batchGenerationStoredSettingsForPersistence(settings);
					}),
			})),
			{
				name: batchGenerationSettingsStorageKey,
				storage: createJSONStorage(() => localStorage),
				version: 1,
				partialize: (state) => ({ settingsByKind: state.settingsByKind }),
				merge: (persisted, current) => {
					const state =
						(persisted as
							| Partial<Pick<BatchGenerationSettingsPreferenceState, "settingsByKind">>
							| undefined) ?? {};
					const settingsByKind = normalizeBatchGenerationStoredSettingsMap(state.settingsByKind);
					return {
						...current,
						settingsByKind: hasBatchGenerationStoredSettings(settingsByKind)
							? settingsByKind
							: current.settingsByKind,
					};
				},
			},
		),
	);

function readLegacyBatchGenerationSettings(): BatchGenerationStoredSettingsMap {
	if (typeof localStorage === "undefined") return {};
	try {
		const rawValue = localStorage.getItem(batchGenerationSettingsStorageKey);
		if (!rawValue) return {};
		const parsed = JSON.parse(rawValue) as unknown;
		if (!isRecord(parsed) || "state" in parsed) return {};
		return normalizeBatchGenerationStoredSettingsMap(parsed);
	} catch {
		return {};
	}
}

function normalizeBatchGenerationStoredSettingsMap(
	value: unknown,
): BatchGenerationStoredSettingsMap {
	if (!isRecord(value)) return {};
	return {
		image: normalizeBatchGenerationStoredSettings(value.image) ?? undefined,
		video: normalizeBatchGenerationStoredSettings(value.video) ?? undefined,
	};
}

function hasBatchGenerationStoredSettings(settings: BatchGenerationStoredSettingsMap) {
	return Boolean(settings.image || settings.video);
}

export function normalizeBatchGenerationStoredSettings(
	value: unknown,
): BatchGenerationStoredSettings | null {
	if (!isRecord(value)) return null;

	return compactBatchGenerationStoredSettings({
		familyId: stringValue(value.familyId),
		instanceProfileId: stringValue(value.instanceProfileId),
		params: isRecord(value.params) ? { ...value.params } : undefined,
		promptOptimizeItemId: stringValue(value.promptOptimizeItemId),
		promptOptimizeRouteId: stringValue(value.promptOptimizeRouteId),
		promptSupplementItemIds: promptSupplementItemIdsValue(value),
		routeId: stringValue(value.routeId),
		usePromptOptimization:
			typeof value.usePromptOptimization === "boolean" ? value.usePromptOptimization : undefined,
		usePromptSupplement:
			typeof value.usePromptSupplement === "boolean" ? value.usePromptSupplement : undefined,
		versionId: stringValue(value.versionId),
		workflowParameters: workflowParametersValue(value.workflowParameters),
		workflowProfileId: stringValue(value.workflowProfileId),
	});
}

// Keep the persisted preference intentionally smaller than a generation request.
// In particular, references and prompt snapshots are request-scoped and must never
// be restored into a later generation by localStorage.
export const batchGenerationStoredSettingsForPersistence = (
	settings: BatchGenerationStoredSettings,
): BatchGenerationStoredSettings => normalizeBatchGenerationStoredSettings(settings) ?? {};

function compactBatchGenerationStoredSettings(
	settings: BatchGenerationStoredSettings,
): BatchGenerationStoredSettings {
	const next: BatchGenerationStoredSettings = {};
	const familyId = stringValue(settings.familyId);
	const instanceProfileId = stringValue(settings.instanceProfileId);
	const promptOptimizeItemId = stringValue(settings.promptOptimizeItemId);
	const promptOptimizeRouteId = stringValue(settings.promptOptimizeRouteId);
	const promptSupplementItemIds = promptSupplementItemIdsValue({
		promptSupplementItemIds: settings.promptSupplementItemIds,
	});
	const routeId = stringValue(settings.routeId);
	const versionId = stringValue(settings.versionId);
	const workflowProfileId = stringValue(settings.workflowProfileId);
	if (familyId) next.familyId = familyId;
	if (instanceProfileId) next.instanceProfileId = instanceProfileId;
	if (settings.params && Object.keys(settings.params).length > 0)
		next.params = { ...settings.params };
	if (promptOptimizeItemId) next.promptOptimizeItemId = promptOptimizeItemId;
	if (promptOptimizeRouteId) next.promptOptimizeRouteId = promptOptimizeRouteId;
	if (promptSupplementItemIds) next.promptSupplementItemIds = promptSupplementItemIds;
	if (routeId) next.routeId = routeId;
	if (typeof settings.usePromptOptimization === "boolean") {
		next.usePromptOptimization = settings.usePromptOptimization;
	}
	if (typeof settings.usePromptSupplement === "boolean") {
		next.usePromptSupplement = settings.usePromptSupplement;
	}
	if (versionId) next.versionId = versionId;
	if (settings.workflowParameters && Object.keys(settings.workflowParameters).length > 0) {
		next.workflowParameters = { ...settings.workflowParameters };
	}
	if (workflowProfileId) next.workflowProfileId = workflowProfileId;
	return next;
}

function workflowParametersValue(value: unknown) {
	if (!isRecord(value)) return undefined;
	const entries = Object.entries(value).filter(
		([, candidate]) =>
			typeof candidate === "string" ||
			typeof candidate === "boolean" ||
			(typeof candidate === "number" && Number.isFinite(candidate)),
	);
	return entries.length > 0
		? (Object.fromEntries(entries) as Record<string, string | number | boolean>)
		: undefined;
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringValue(value: unknown) {
	return typeof value === "string" ? value.trim() : undefined;
}

function promptSupplementItemIdsValue(value: Record<string, unknown>): string[] | undefined {
	const candidates = Array.isArray(value.promptSupplementItemIds)
		? value.promptSupplementItemIds
		: [];
	// promptSupplementItemId is the legacy single-value field written before multi-select.
	const ids = [
		...new Set(
			[...candidates, value.promptSupplementItemId]
				.map(stringValue)
				.filter((id): id is string => Boolean(id)),
		),
	];
	return ids.length > 0 ? ids : undefined;
}
