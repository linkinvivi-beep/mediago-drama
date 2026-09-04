import httpClient from "@/shared/lib/http";

export interface WorkflowTarget {
	nodeId: string;
	inputName: string;
}

export interface ReferenceBinding {
	index: number;
	target: WorkflowTarget;
}

export interface OutputBinding {
	nodeId: string;
	outputIndex?: number;
}

export interface ParameterBinding {
	name: string;
	target: WorkflowTarget;
}

export interface WorkflowBindings {
	confirmed: boolean;
	prompts: WorkflowTarget[];
	negative?: WorkflowTarget;
	references?: ReferenceBinding[];
	outputs: OutputBinding[];
	parameters?: ParameterBinding[];
}

export interface AutoDLReferenceContract {
	min: number;
	max: number;
	slots?: ReferenceBinding[];
}

export interface AutoDLWorkflowValidation {
	workflowProfileId: string;
	versionId: string;
	status: "ready" | "invalid" | "unknown" | "stale";
	workflowDigest: string;
	apiTemplateDigest: string;
	objectInfoDigest?: string;
	instanceFingerprint?: string;
	validatedAt?: string;
	reason?: string;
}

export interface AutoDLInstance {
	id: string;
	name: string;
	host: string;
	sshPort: number;
	sshUser: string;
	comfyPort: number;
	startupCommand?: string;
	localPort?: number;
	hostFingerprint?: string;
	credentialRef: string;
	enabled: boolean;
	hasPassword: boolean;
	workflowValidations?: AutoDLWorkflowValidation[];
}

export interface AutoDLWorkflowVersion {
	versionId: string;
	sequence: number;
	sourceVersionId?: string;
	createdAt: string;
	workflowDigest: string;
	apiTemplateDigest: string;
	bindingStatus: "confirmed" | "unconfirmed";
	bindings: WorkflowBindings;
	references: AutoDLReferenceContract;
	promptGuide?: string;
}

export interface AutoDLWorkflowProfile {
	id: string;
	name: string;
	description?: string;
	mediaKind: string;
	routeId: string;
	enabled: boolean;
	autoSelectable: boolean;
	archived: boolean;
	currentVersionId: string;
	versions: AutoDLWorkflowVersion[];
}

export interface AutoDLWorkflowDefault {
	id: string;
	routeId?: string;
	minReferences: number;
	maxReferences: number;
	workflowProfileId: string;
}

export interface AutoDLSettingsResponse {
	instances: AutoDLInstance[];
	workflowProfiles: AutoDLWorkflowProfile[];
	workflowDefaults: AutoDLWorkflowDefault[];
}

export interface WorkflowSuggestions {
	prompts: WorkflowTarget[];
	references?: ReferenceBinding[];
	outputs: OutputBinding[];
	parameters?: ParameterBinding[];
}

export interface AutoDLWorkflowPreview {
	inspection: {
		confirmed: false;
		nodeCount: number;
		linkCount: number;
		requiredNodes: string[];
		suggestions: WorkflowSuggestions;
	};
	objectInfoDigest: string;
}

export interface AutoDLInstanceMutation {
	name: string;
	sshCommand: string;
	password?: string;
	comfyPort?: number;
	startupCommand?: string;
	localPort?: number;
	hostFingerprint?: string;
	enabled: boolean;
}

export interface AutoDLWorkflowMutation {
	instanceProfileId: string;
	mediaKind?: "image" | "video";
	routeId?: "autodl.image" | "autodl.minimax-h3";
	id?: string;
	name?: string;
	description?: string;
	expectedCurrentVersionId?: string;
	uiWorkflow: unknown;
	bindings: WorkflowBindings;
	references: AutoDLReferenceContract;
	promptGuide?: string;
}

export interface AutoDLInstanceCheck {
	connected: boolean;
	stage?:
		| "connecting"
		| "probing"
		| "starting"
		| "waiting_health"
		| "tunneling"
		| "validating_api"
		| "ready"
		| "failed";
	localPort?: number;
	comfyuiVersion?: string;
	devices?: string[];
	reason?: string;
}

export const autoDLSettingsKey = "/settings/autodl";

export const getAutoDLSettings = async () =>
	(await httpClient.get<AutoDLSettingsResponse>(autoDLSettingsKey)).data;

export const saveAutoDLInstance = async (input: AutoDLInstanceMutation, instanceId?: string) => {
	const path = instanceId
		? `${autoDLSettingsKey}/instances/${encodeURIComponent(instanceId)}`
		: `${autoDLSettingsKey}/instances`;
	const response = instanceId
		? await httpClient.put<AutoDLInstance>(path, input)
		: await httpClient.post<AutoDLInstance>(path, input);
	return response.data;
};

export const setAutoDLPassword = async (instanceId: string, password: string) =>
	(
		await httpClient.put<AutoDLInstance>(
			`${autoDLSettingsKey}/instances/${encodeURIComponent(instanceId)}/password`,
			{ password },
		)
	).data;

export const clearAutoDLPassword = async (instanceId: string) =>
	(
		await httpClient.delete<AutoDLInstance>(
			`${autoDLSettingsKey}/instances/${encodeURIComponent(instanceId)}/password`,
		)
	).data;

export const deleteAutoDLInstance = async (instanceId: string) =>
	(
		await httpClient.delete<AutoDLSettingsResponse>(
			`${autoDLSettingsKey}/instances/${encodeURIComponent(instanceId)}`,
		)
	).data;

export const scanAutoDLFingerprint = async (instanceId: string) =>
	(
		await httpClient.post<{ fingerprint: string }>(
			`${autoDLSettingsKey}/instances/${encodeURIComponent(instanceId)}/scan-fingerprint`,
		)
	).data;

export const checkAutoDLInstance = async (instanceId: string) =>
	(
		await httpClient.post<AutoDLInstanceCheck>(
			`${autoDLSettingsKey}/instances/${encodeURIComponent(instanceId)}/check`,
			undefined,
			{ timeout: 100_000 },
		)
	).data;

export const getAutoDLInstanceReadiness = async (instanceId: string) =>
	(
		await httpClient.get<AutoDLInstanceCheck>(
			`${autoDLSettingsKey}/instances/${encodeURIComponent(instanceId)}/readiness`,
		)
	).data;

export const previewAutoDLWorkflow = async (instanceProfileId: string, uiWorkflow: unknown) =>
	(
		await httpClient.post<AutoDLWorkflowPreview>(`${autoDLSettingsKey}/workflows/preview`, {
			instanceProfileId,
			uiWorkflow,
		})
	).data;

export const createAutoDLWorkflow = async (input: AutoDLWorkflowMutation) =>
	(await httpClient.post<AutoDLWorkflowProfile>(`${autoDLSettingsKey}/workflows`, input)).data;

export const replaceAutoDLWorkflow = async (profileId: string, input: AutoDLWorkflowMutation) =>
	(
		await httpClient.post<AutoDLWorkflowProfile>(
			`${autoDLSettingsKey}/workflows/${encodeURIComponent(profileId)}/versions`,
			input,
		)
	).data;

export const duplicateAutoDLWorkflow = async (
	profileId: string,
	input: { id: string; name: string; description?: string },
) =>
	(
		await httpClient.post<AutoDLWorkflowProfile>(
			`${autoDLSettingsKey}/workflows/${encodeURIComponent(profileId)}/duplicate`,
			input,
		)
	).data;

export const updateAutoDLWorkflowState = async (
	profileId: string,
	input: { enabled?: boolean; autoSelectable?: boolean; archived?: boolean },
) =>
	(
		await httpClient.patch<AutoDLWorkflowProfile>(
			`${autoDLSettingsKey}/workflows/${encodeURIComponent(profileId)}`,
			input,
		)
	).data;

export const setAutoDLWorkflowDefaults = async (defaults: AutoDLWorkflowDefault[]) =>
	(await httpClient.put<AutoDLSettingsResponse>(`${autoDLSettingsKey}/defaults`, { defaults }))
		.data;

export const validateAutoDLWorkflow = async (
	profileId: string,
	versionId: string,
	instanceId: string,
) =>
	(
		await httpClient.post<AutoDLWorkflowValidation>(
			`${autoDLSettingsKey}/workflows/${encodeURIComponent(profileId)}/versions/${encodeURIComponent(versionId)}/validate/${encodeURIComponent(instanceId)}`,
		)
	).data;
