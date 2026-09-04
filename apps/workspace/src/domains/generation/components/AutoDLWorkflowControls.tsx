import { CloudCog } from "lucide-react";
import type React from "react";
import type { AutoDLWorkflowOptionState } from "@/domains/generation/hooks/useAutoDLWorkflowOptions";
import type { GenerationSettingsFormController } from "@/domains/generation/hooks/useGenerationSettingsForm";
import { Input } from "@/shared/components/ui/input";

const automaticValue = "__automatic__";

export interface AutoDLWorkflowControlsValue {
	instanceProfileId?: string;
	routeId: string;
	workflowParameters?: Record<string, string | number | boolean>;
	workflowProfileId?: string;
}

export interface AutoDLWorkflowControlsViewProps {
	disabled?: boolean;
	onInstanceProfileChange: (instanceProfileId: string) => void;
	onWorkflowParameterChange: (name: string, value: string | number | boolean | undefined) => void;
	onWorkflowProfileChange: (workflowProfileId: string) => void;
	options: AutoDLWorkflowOptionState;
	value: AutoDLWorkflowControlsValue;
}

export const AutoDLWorkflowControls: React.FC<{
	controller: GenerationSettingsFormController;
	disabled?: boolean;
}> = ({ controller, disabled = false }) => (
	<AutoDLWorkflowControlsView
		disabled={disabled}
		onInstanceProfileChange={controller.updateInstanceProfile}
		onWorkflowParameterChange={controller.updateWorkflowParameter}
		onWorkflowProfileChange={controller.updateWorkflowProfile}
		options={controller.autoDLWorkflowOptions}
		value={controller.value}
	/>
);

export const AutoDLWorkflowControlsView: React.FC<AutoDLWorkflowControlsViewProps> = ({
	disabled = false,
	onInstanceProfileChange,
	onWorkflowParameterChange,
	onWorkflowProfileChange,
	options,
	value,
}) => {
	if (!isAutoDLRoute(value.routeId)) return null;
	const selectedProfileAvailable =
		!value.workflowProfileId ||
		options.compatibleProfiles.some((profile) => profile.id === value.workflowProfileId);
	const selectedInstanceAvailable =
		!value.instanceProfileId ||
		options.readyInstances.some((instance) => instance.id === value.instanceProfileId);
	const selectedProfileName = options.selectedProfile?.name ?? value.workflowProfileId;

	return (
		<section aria-label="MediaLink 工作流" className="grid gap-3 border-t border-border/70 pt-3">
			<div className="flex items-center gap-2">
				<CloudCog className="size-4 text-muted-foreground" />
				<h3 className="text-sm font-semibold text-foreground">MediaLink 工作流</h3>
			</div>
			<label className="grid gap-1 text-xs text-muted-foreground">
				<span>云端工作流</span>
				<select
					aria-label="云端工作流"
					className="h-9 rounded-md border border-border bg-muted px-2.5 text-xs font-semibold text-foreground"
					disabled={disabled || options.isLoading}
					value={value.workflowProfileId || automaticValue}
					onChange={(event) =>
						onWorkflowProfileChange(event.target.value === automaticValue ? "" : event.target.value)
					}
				>
					<option value={automaticValue}>自动选择兼容工作流</option>
					{value.workflowProfileId && !selectedProfileAvailable ? (
						<option value={value.workflowProfileId} disabled>
							{selectedProfileName || "不可用工作流"}（当前不可用）
						</option>
					) : null}
					{options.compatibleProfiles.map((profile) => (
						<option key={profile.id} value={profile.id}>
							{profile.name}
						</option>
					))}
				</select>
			</label>
			{options.error || (!value.workflowProfileId && options.automaticError) ? (
				<p className="text-xs text-error-foreground">{options.error ?? options.automaticError}</p>
			) : null}
			{value.workflowProfileId && !selectedProfileAvailable ? (
				<p className="text-xs text-error-foreground">
					所选工作流与当前参考图数量不兼容，或尚未在可用实例上验证。
				</p>
			) : null}

			{options.parameterNames.length > 0 ? (
				<div className="grid gap-2 sm:grid-cols-2">
					{options.parameterNames.map((name) => (
						<label key={name} className="grid gap-1 text-xs text-muted-foreground">
							<span>{name}</span>
							<Input
								aria-label={`工作流参数：${name}`}
								disabled={disabled}
								value={String(value.workflowParameters?.[name] ?? "")}
								onChange={(event) =>
									onWorkflowParameterChange(name, parseScalarInput(event.target.value))
								}
							/>
						</label>
					))}
				</div>
			) : null}

			<details className="rounded-md bg-muted/60 px-3 py-2">
				<summary className="cursor-pointer text-xs font-semibold text-foreground">高级设置</summary>
				<div className="mt-2 grid gap-2">
					<label className="grid gap-1 text-xs text-muted-foreground">
						<span>云 GPU 实例</span>
						<select
							aria-label="云 GPU 实例"
							className="h-9 rounded-md border border-border bg-background px-2.5 text-xs text-foreground"
							disabled={disabled || !value.workflowProfileId}
							value={value.instanceProfileId || automaticValue}
							onChange={(event) =>
								onInstanceProfileChange(
									event.target.value === automaticValue ? "" : event.target.value,
								)
							}
						>
							<option value={automaticValue}>自动分配空闲实例</option>
							{value.instanceProfileId && !selectedInstanceAvailable ? (
								<option value={value.instanceProfileId} disabled>
									{value.instanceProfileId}（当前不可用）
								</option>
							) : null}
							{options.readyInstances.map((instance) => (
								<option key={instance.id} value={instance.id}>
									{instance.name}
								</option>
							))}
						</select>
					</label>
					{!value.workflowProfileId ? (
						<p className="text-xs text-muted-foreground">手动指定实例前，请先明确选择工作流。</p>
					) : null}
					{value.instanceProfileId && !selectedInstanceAvailable ? (
						<p className="text-xs text-error-foreground">
							所选实例当前不可用；提交会保持等待或报错，不会改用其他实例。
						</p>
					) : null}
				</div>
			</details>
		</section>
	);
};

const parseScalarInput = (value: string): string | number | undefined => {
	const trimmed = value.trim();
	if (!trimmed) return undefined;
	if (/^-?(?:\d+|\d*\.\d+)$/.test(trimmed)) {
		const numeric = Number(trimmed);
		if (Number.isFinite(numeric)) return numeric;
	}
	return value;
};

const isAutoDLRoute = (routeId: string) =>
	routeId === "autodl.image" || routeId === "autodl.minimax-h3";
