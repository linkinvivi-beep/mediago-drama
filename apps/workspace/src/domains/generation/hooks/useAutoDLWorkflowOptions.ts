import { useMemo } from "react";
import useSWR from "swr";
import {
	autoDLSettingsKey,
	getAutoDLSettings,
	type AutoDLInstance,
	type AutoDLSettingsResponse,
	type AutoDLWorkflowProfile,
	type AutoDLWorkflowVersion,
} from "@/domains/settings/api/autodl";

export interface AutoDLWorkflowOptionState {
	automaticError: string | null;
	compatibleProfiles: AutoDLWorkflowProfile[];
	error: string | null;
	isLoading: boolean;
	parameterNames: string[];
	readyInstances: AutoDLInstance[];
	selectedProfile: AutoDLWorkflowProfile | null;
	selectedVersion: AutoDLWorkflowVersion | null;
}

export const useAutoDLWorkflowOptions = (
	routeId: string,
	referenceCount: number,
	workflowProfileId?: string,
): AutoDLWorkflowOptionState => {
	const enabled = isAutoDLRoute(routeId);
	const { data, error, isLoading } = useSWR(enabled ? autoDLSettingsKey : null, getAutoDLSettings, {
		shouldRetryOnError: false,
	});
	return useMemo(
		() =>
			resolveAutoDLWorkflowOptions(
				data,
				routeId,
				referenceCount,
				workflowProfileId,
				enabled ? error : undefined,
				enabled && isLoading,
			),
		[data, enabled, error, isLoading, referenceCount, routeId, workflowProfileId],
	);
};

export const resolveAutoDLWorkflowOptions = (
	settings: AutoDLSettingsResponse | undefined,
	routeId: string,
	referenceCount: number,
	workflowProfileId?: string,
	loadError?: unknown,
	isLoading = false,
): AutoDLWorkflowOptionState => {
	const compatibleProfiles = (settings?.workflowProfiles ?? []).filter((profile) =>
		isCompatibleAutoDLWorkflow(settings, profile, routeId, referenceCount),
	);
	const selectedProfile =
		(settings?.workflowProfiles ?? []).find((profile) => profile.id === workflowProfileId) ?? null;
	const selectedVersion = selectedProfile ? currentWorkflowVersion(selectedProfile) : null;
	const readyInstances =
		selectedProfile && selectedVersion
			? (settings?.instances ?? []).filter((instance) =>
					isReadyAutoDLInstance(instance, selectedProfile, selectedVersion),
				)
			: [];
	const defaults = (settings?.workflowDefaults ?? []).filter(
		(item) =>
			(item.routeId === routeId || (!item.routeId && routeId === "autodl.image")) &&
			referenceCount >= item.minReferences &&
			referenceCount <= item.maxReferences,
	);
	let automaticError: string | null = null;
	if (!isLoading && settings) {
		if (defaults.length === 0) automaticError = "当前参考图数量没有默认工作流";
		else if (defaults.length > 1) automaticError = "当前参考图数量匹配了多个默认工作流";
		else {
			const profile = compatibleProfiles.find(
				(item) => item.id === defaults[0]?.workflowProfileId && item.autoSelectable,
			);
			if (!profile) automaticError = "默认工作流尚未启用或未通过实例验证";
		}
	}
	return {
		automaticError,
		compatibleProfiles,
		error: loadError ? "无法读取 MediaLink 工作流配置" : null,
		isLoading,
		parameterNames: selectedVersion
			? [...new Set((selectedVersion.bindings.parameters ?? []).map((item) => item.name.trim()))]
					.filter(Boolean)
					.sort((left, right) => left.localeCompare(right))
			: [],
		readyInstances,
		selectedProfile,
		selectedVersion,
	};
};

const isCompatibleAutoDLWorkflow = (
	settings: AutoDLSettingsResponse | undefined,
	profile: AutoDLWorkflowProfile,
	routeId: string,
	referenceCount: number,
) => {
	const expectedMediaKind = routeId === "autodl.minimax-h3" ? "video" : "image";
	if (
		!profile.enabled ||
		profile.archived ||
		profile.mediaKind !== expectedMediaKind ||
		profile.routeId !== routeId
	)
		return false;
	const version = currentWorkflowVersion(profile);
	if (
		!version ||
		version.bindingStatus !== "confirmed" ||
		referenceCount < version.references.min ||
		referenceCount > version.references.max
	)
		return false;
	return (settings?.instances ?? []).some((instance) =>
		isReadyAutoDLInstance(instance, profile, version),
	);
};

const isAutoDLRoute = (routeId: string) =>
	routeId === "autodl.image" || routeId === "autodl.minimax-h3";

const currentWorkflowVersion = (profile: AutoDLWorkflowProfile) =>
	profile.versions.find((version) => version.versionId === profile.currentVersionId) ?? null;

const isReadyAutoDLInstance = (
	instance: AutoDLInstance,
	profile: AutoDLWorkflowProfile,
	version: AutoDLWorkflowVersion,
) => {
	if (!instance.enabled || !instance.hasPassword || !instance.hostFingerprint?.trim()) return false;
	return (instance.workflowValidations ?? []).some(
		(validation) =>
			validation.workflowProfileId === profile.id &&
			validation.versionId === version.versionId &&
			validation.status === "ready" &&
			validation.workflowDigest === version.workflowDigest &&
			validation.apiTemplateDigest === version.apiTemplateDigest &&
			validation.instanceFingerprint === instance.hostFingerprint,
	);
};
