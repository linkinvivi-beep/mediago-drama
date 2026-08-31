import { Check, FileJson, Loader2, Workflow } from "lucide-react";
import type React from "react";
import { useEffect, useMemo, useState } from "react";
import {
	createAutoDLWorkflow,
	previewAutoDLWorkflow,
	replaceAutoDLWorkflow,
	type AutoDLInstance,
	type AutoDLWorkflowProfile,
	type AutoDLWorkflowPreview,
	type WorkflowBindings,
} from "@/domains/settings/api/autodl";
import {
	AlertDialog,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/shared/components/ui/alert-dialog";
import { Button } from "@/shared/components/ui/button";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/shared/components/ui/select";
import { Textarea } from "@/shared/components/ui/textarea";
import { useToast } from "@/hooks/useToast";

interface AutoDLWorkflowDialogProps {
	instances: AutoDLInstance[];
	mode: "create" | "replace";
	onOpenChange: (open: boolean) => void;
	onSaved: () => void | Promise<void>;
	open: boolean;
	profile?: AutoDLWorkflowProfile;
}

export const AutoDLWorkflowDialog: React.FC<AutoDLWorkflowDialogProps> = ({
	instances,
	mode,
	onOpenChange,
	onSaved,
	open,
	profile,
}) => {
	const toast = useToast();
	const usableInstances = instances.filter((instance) => instance.enabled && instance.hasPassword);
	const [instanceId, setInstanceId] = useState("");
	const [profileId, setProfileId] = useState("");
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [promptGuide, setPromptGuide] = useState("");
	const [uiWorkflow, setUIWorkflow] = useState<unknown>();
	const [fileName, setFileName] = useState("");
	const [preview, setPreview] = useState<AutoDLWorkflowPreview>();
	const [confirmed, setConfirmed] = useState<Set<string>>(new Set());
	const [minReferences, setMinReferences] = useState(0);
	const [maxReferences, setMaxReferences] = useState(0);
	const [analyzing, setAnalyzing] = useState(false);
	const [saving, setSaving] = useState(false);

	useEffect(() => {
		if (!open) return;
		const current = profile?.versions.find(
			(version) => version.versionId === profile.currentVersionId,
		);
		setInstanceId(usableInstances[0]?.id ?? "");
		setProfileId(mode === "create" ? "" : (profile?.id ?? ""));
		setName(mode === "create" ? "" : (profile?.name ?? ""));
		setDescription(mode === "create" ? "" : (profile?.description ?? ""));
		setPromptGuide(current?.promptGuide ?? "");
		setMinReferences(current?.references.min ?? 0);
		setMaxReferences(current?.references.max ?? 0);
		setUIWorkflow(undefined);
		setFileName("");
		setPreview(undefined);
		setConfirmed(new Set());
	}, [mode, open, profile, usableInstances[0]?.id]);

	const mappings = useMemo(() => (preview ? previewMappings(preview) : []), [preview]);
	const allConfirmed =
		mappings.length > 0 && mappings.every((mapping) => confirmed.has(mapping.key));
	const canSave =
		Boolean(instanceId && uiWorkflow && allConfirmed) &&
		(mode === "replace" || Boolean(profileId.trim() && name.trim())) &&
		minReferences >= 0 &&
		maxReferences >= minReferences &&
		maxReferences <= 8;

	const loadWorkflow = async (event: React.ChangeEvent<HTMLInputElement>) => {
		const file = event.target.files?.[0];
		if (!file) return;
		try {
			const parsed = JSON.parse(await file.text()) as unknown;
			setUIWorkflow(parsed);
			setFileName(file.name);
			setPreview(undefined);
			setConfirmed(new Set());
		} catch {
			setUIWorkflow(undefined);
			setFileName("");
			toast.error("工作流文件无效", { description: "请选择 ComfyUI 导出的 JSON 工作流。" });
		}
	};

	const analyze = async () => {
		if (!instanceId || !uiWorkflow) return;
		setAnalyzing(true);
		try {
			const result = await previewAutoDLWorkflow(instanceId, uiWorkflow);
			setPreview(result);
			setConfirmed(new Set());
			const suggestedReferences = result.inspection.suggestions.references?.length ?? 0;
			setMaxReferences(suggestedReferences);
			setMinReferences(suggestedReferences > 0 ? 1 : 0);
		} catch (err) {
			toast.error("分析工作流失败", { description: errorMessage(err) });
		} finally {
			setAnalyzing(false);
		}
	};

	const save = async () => {
		if (!canSave || !preview || !uiWorkflow) return;
		setSaving(true);
		try {
			const bindings = bindingsFromPreview(preview);
			const references = {
				min: minReferences,
				max: maxReferences,
				slots: preview.inspection.suggestions.references ?? [],
			};
			if (mode === "create") {
				await createAutoDLWorkflow({
					instanceProfileId: instanceId,
					id: profileId.trim(),
					name: name.trim(),
					description: description.trim(),
					uiWorkflow,
					bindings,
					references,
					promptGuide: promptGuide.trim(),
				});
			} else if (profile) {
				await replaceAutoDLWorkflow(profile.id, {
					instanceProfileId: instanceId,
					expectedCurrentVersionId: profile.currentVersionId,
					uiWorkflow,
					bindings,
					references,
					promptGuide: promptGuide.trim(),
				});
			}
			await onSaved();
			onOpenChange(false);
			toast.success(mode === "create" ? "工作流已添加" : "工作流版本已替换", {
				description: "新版本默认保持停用，完成实例验证后再启用。",
			});
		} catch (err) {
			toast.error("保存工作流失败", { description: errorMessage(err) });
		} finally {
			setSaving(false);
		}
	};

	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent className="max-h-[86vh] max-w-3xl overflow-y-auto">
				<AlertDialogHeader>
					<AlertDialogTitle>
						{mode === "create" ? "添加 ComfyUI 工作流" : `替换版本 · ${profile?.name ?? ""}`}
					</AlertDialogTitle>
					<AlertDialogDescription>
						MediaLink 只读取所选实例的节点清单并在本机编译模板；分析和保存都不会执行工作流。
					</AlertDialogDescription>
				</AlertDialogHeader>

				<div className="grid gap-4 py-1 md:grid-cols-2">
					{mode === "create" ? (
						<>
							<Field label="工作流 ID">
								<Input
									value={profileId}
									onChange={(event) => setProfileId(event.target.value)}
									placeholder="例如 portrait-v1"
								/>
							</Field>
							<Field label="显示名称">
								<Input
									value={name}
									onChange={(event) => setName(event.target.value)}
									placeholder="例如 人物多图参考"
								/>
							</Field>
						</>
					) : null}
					<Field label="用于分析的 AutoDL 实例">
						<Select value={instanceId} onValueChange={setInstanceId}>
							<SelectTrigger aria-label="用于分析的 AutoDL 实例">
								<SelectValue placeholder="选择已配置实例" />
							</SelectTrigger>
							<SelectContent>
								{usableInstances.map((instance) => (
									<SelectItem key={instance.id} value={instance.id}>
										{instance.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Field>
					<Field label="ComfyUI 工作流 JSON">
						<label className="flex h-8 cursor-pointer items-center gap-2 rounded-control border border-input bg-ide-editor px-2 text-xs hover:bg-ide-list-hover">
							<FileJson className="size-4 text-muted-foreground" />
							<span className="min-w-0 flex-1 truncate">{fileName || "选择 JSON 文件"}</span>
							<input
								className="sr-only"
								type="file"
								accept="application/json,.json"
								onChange={(event) => void loadWorkflow(event)}
							/>
						</label>
					</Field>
					<div className="md:col-span-2">
						<Field label="说明">
							<Input
								value={description}
								onChange={(event) => setDescription(event.target.value)}
								placeholder="用途、模型要求或使用提醒"
							/>
						</Field>
					</div>
					<div className="md:col-span-2">
						<Field label="提示词优化说明（可选）">
							<Textarea
								value={promptGuide}
								onChange={(event) => setPromptGuide(event.target.value)}
								placeholder="描述这个工作流偏好的提示词结构，不绑定具体模型名称。"
							/>
						</Field>
					</div>
				</div>

				<div className="flex justify-end">
					<Button
						type="button"
						variant="secondary"
						disabled={!instanceId || !uiWorkflow || analyzing}
						onClick={() => void analyze()}
					>
						{analyzing ? <Loader2 className="animate-spin" /> : <Workflow />}
						分析工作流
					</Button>
				</div>

				{preview ? (
					<div className="space-y-3 rounded-sm border border-border/70 bg-ide-editor p-3">
						<div className="flex flex-wrap items-center justify-between gap-2">
							<div>
								<p className="text-xs font-semibold">确认字段映射</p>
								<p className="text-2xs text-muted-foreground">
									{preview.inspection.nodeCount} 个节点 · {preview.inspection.linkCount} 条连接
								</p>
							</div>
							<p className="font-mono text-2xs text-muted-foreground">
								节点清单 {preview.objectInfoDigest.slice(0, 12)}
							</p>
						</div>
						<div className="grid gap-2 md:grid-cols-2">
							{mappings.map((mapping) => (
								<label
									key={mapping.key}
									className="flex cursor-pointer items-start gap-2 rounded-sm border border-border/60 bg-ide-panel px-2.5 py-2"
								>
									<input
										type="checkbox"
										className="mt-0.5"
										checked={confirmed.has(mapping.key)}
										onChange={(event) =>
											setConfirmed((current) =>
												toggleSet(current, mapping.key, event.target.checked),
											)
										}
										aria-label={`确认映射：${mapping.label}`}
									/>
									<span className="min-w-0">
										<span className="block text-xs font-medium">{mapping.label}</span>
										<span className="block truncate font-mono text-2xs text-muted-foreground">
											{mapping.target}
										</span>
									</span>
								</label>
							))}
						</div>
						<div className="grid gap-3 border-t border-border/60 pt-3 sm:grid-cols-2">
							<Field label="最少参考图">
								<Input
									type="number"
									min={0}
									max={8}
									value={minReferences}
									onChange={(event) => setMinReferences(Number(event.target.value))}
								/>
							</Field>
							<Field label="最多参考图">
								<Input
									type="number"
									min={0}
									max={8}
									value={maxReferences}
									onChange={(event) => setMaxReferences(Number(event.target.value))}
								/>
							</Field>
						</div>
					</div>
				) : null}

				<AlertDialogFooter>
					<AlertDialogCancel disabled={saving}>取消</AlertDialogCancel>
					<Button type="button" disabled={!canSave || saving} onClick={() => void save()}>
						{saving ? <Loader2 className="animate-spin" /> : <Check />}
						保存工作流
					</Button>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
};

const Field: React.FC<{ children: React.ReactNode; label: string }> = ({ children, label }) => (
	<div className="space-y-1.5">
		<Label className="text-xs text-muted-foreground">{label}</Label>
		{children}
	</div>
);

const previewMappings = (preview: AutoDLWorkflowPreview) => {
	const suggestions = preview.inspection.suggestions;
	return [
		...suggestions.prompts.map((target, index) => ({
			key: `prompt:${index}`,
			label: `提示词 ${index + 1}`,
			target: `${target.nodeId}.${target.inputName}`,
		})),
		...(suggestions.references ?? []).map((binding) => ({
			key: `reference:${binding.index}`,
			label: `参考图 ${binding.index + 1}`,
			target: `${binding.target.nodeId}.${binding.target.inputName}`,
		})),
		...(suggestions.parameters ?? []).map((binding, index) => ({
			key: `parameter:${index}`,
			label: `参数 · ${binding.name}`,
			target: `${binding.target.nodeId}.${binding.target.inputName}`,
		})),
		...suggestions.outputs.map((binding, index) => ({
			key: `output:${index}`,
			label: `输出 ${index + 1}`,
			target: `${binding.nodeId}[${binding.outputIndex ?? 0}]`,
		})),
	];
};

const bindingsFromPreview = (preview: AutoDLWorkflowPreview): WorkflowBindings => ({
	confirmed: true,
	prompts: preview.inspection.suggestions.prompts,
	references: preview.inspection.suggestions.references ?? [],
	outputs: preview.inspection.suggestions.outputs,
	parameters: preview.inspection.suggestions.parameters ?? [],
});

const toggleSet = (current: Set<string>, value: string, checked: boolean) => {
	const next = new Set(current);
	if (checked) next.add(value);
	else next.delete(value);
	return next;
};

const errorMessage = (error: unknown) =>
	typeof error === "object" && error !== null && "message" in error
		? String(error.message)
		: "操作失败，请稍后重试。";
