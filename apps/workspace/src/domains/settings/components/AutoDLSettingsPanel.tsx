import {
	Archive,
	CheckCircle2,
	CloudCog,
	Copy,
	Fingerprint,
	KeyRound,
	Loader2,
	Pencil,
	Plus,
	RefreshCw,
	Server,
	ShieldCheck,
	Unplug,
} from "lucide-react";
import type React from "react";
import { useEffect, useState } from "react";
import useSWR from "swr";
import {
	autoDLSettingsKey,
	checkAutoDLInstance,
	clearAutoDLPassword,
	deleteAutoDLInstance,
	duplicateAutoDLWorkflow,
	getAutoDLInstanceReadiness,
	getAutoDLSettings,
	saveAutoDLInstance,
	scanAutoDLFingerprint,
	updateAutoDLWorkflowState,
	validateAutoDLWorkflow,
	type AutoDLInstance,
	type AutoDLInstanceCheck,
	type AutoDLWorkflowProfile,
} from "@/domains/settings/api/autodl";
import { AutoDLWorkflowDialog } from "@/domains/settings/components/AutoDLWorkflowDialog";
import { SettingsPanelLayout } from "@/domains/settings/components/SettingsPanelLayout";
import { confirmDialog } from "@/shared/components/callable/ConfirmDialog";
import {
	AlertDialog,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/shared/components/ui/alert-dialog";
import { Badge } from "@/shared/components/ui/badge";
import { Button } from "@/shared/components/ui/button";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/shared/components/ui/card";
import { Input } from "@/shared/components/ui/input";
import { Label } from "@/shared/components/ui/label";
import { Switch } from "@/shared/components/ui/switch";
import { Textarea } from "@/shared/components/ui/textarea";
import { useToast } from "@/hooks/useToast";

type WorkflowDialogState =
	| { mode: "create"; profile?: undefined }
	| { mode: "replace"; profile: AutoDLWorkflowProfile };

export const AutoDLSettingsPanel: React.FC = () => {
	const toast = useToast();
	const { data, error, isLoading, mutate } = useSWR(autoDLSettingsKey, getAutoDLSettings);
	const instances = data?.instances ?? [];
	const profiles = data?.workflowProfiles ?? [];
	const [instanceDialog, setInstanceDialog] = useState<AutoDLInstance | null | undefined>();
	const [workflowDialog, setWorkflowDialog] = useState<WorkflowDialogState>();
	const [busyKey, setBusyKey] = useState("");
	const [checks, setChecks] = useState<Record<string, AutoDLInstanceCheck>>({});

	const refresh = async () => {
		await mutate();
	};

	const run = async (key: string, action: () => Promise<void>, failure: string) => {
		setBusyKey(key);
		try {
			await action();
		} catch (err) {
			toast.error(failure, { description: errorMessage(err) });
		} finally {
			setBusyKey("");
		}
	};

	const checkInstance = (instance: AutoDLInstance) =>
		run(
			`check:${instance.id}`,
			async () => {
				setChecks((current) => ({
					...current,
					[instance.id]: { connected: false, stage: "connecting" },
				}));
				const poll = async () => {
					try {
						const status = await getAutoDLInstanceReadiness(instance.id);
						if (status.stage) {
							setChecks((current) => ({ ...current, [instance.id]: status }));
						}
					} catch {
						// The POST result remains authoritative; polling is display-only.
					}
				};
				const timer = window.setInterval(() => void poll(), 1000);
				try {
					const result = await checkAutoDLInstance(instance.id);
					setChecks((current) => ({ ...current, [instance.id]: result }));
					toast.success("实例可以生成", {
						description: `${instance.name} · 本地端口 ${result.localPort ?? "自动"}`,
					});
				} finally {
					window.clearInterval(timer);
				}
			},
			"检查实例失败",
		);

	const scanFingerprint = (instance: AutoDLInstance) =>
		run(
			`scan:${instance.id}`,
			async () => {
				const observed = await scanAutoDLFingerprint(instance.id);
				await confirmDialog({
					title: "确认 SSH 主机指纹",
					description: `请核对云 GPU 控制台显示的指纹：${observed.fingerprint}。确认后才会写入实例配置。`,
					confirmLabel: "确认并保存",
					confirmIcon: <ShieldCheck />,
					onConfirm: async () => {
						await saveAutoDLInstance(
							{
								name: instance.name,
								sshCommand: sshCommand(instance),
								comfyPort: instance.comfyPort,
								startupCommand: instance.startupCommand,
								localPort: instance.localPort ?? 0,
								hostFingerprint: observed.fingerprint,
								enabled: instance.enabled,
							},
							instance.id,
						);
						await refresh();
					},
				});
			},
			"扫描指纹失败",
		);

	const removeInstance = (instance: AutoDLInstance) => {
		void confirmDialog({
			title: `删除实例“${instance.name}”？`,
			description: "这会删除该实例配置及其对应的 Keychain 密码，不会删除云端文件。",
			confirmLabel: "删除实例",
			confirmIcon: <Unplug />,
			onConfirm: () =>
				run(
					`delete:${instance.id}`,
					async () => {
						await deleteAutoDLInstance(instance.id);
						await refresh();
					},
					"删除实例失败",
				),
		});
	};

	const setWorkflowState = (
		profile: AutoDLWorkflowProfile,
		input: { enabled?: boolean; autoSelectable?: boolean; archived?: boolean },
	) =>
		run(
			`workflow:${profile.id}`,
			async () => {
				await updateAutoDLWorkflowState(profile.id, input);
				await refresh();
			},
			"更新工作流失败",
		);

	const duplicateWorkflow = (profile: AutoDLWorkflowProfile) => {
		void confirmDialog({
			title: `复制“${profile.name}”？`,
			description: `将创建停用副本 ${profile.id}-copy；如 ID 已存在，可在后续添加工作流时换一个 ID。`,
			confirmLabel: "复制",
			confirmIcon: <Copy />,
			onConfirm: () =>
				run(
					`duplicate:${profile.id}`,
					async () => {
						await duplicateAutoDLWorkflow(profile.id, {
							id: `${profile.id}-copy`,
							name: `${profile.name} 副本`,
							description: profile.description,
						});
						await refresh();
					},
					"复制工作流失败",
				),
		});
	};

	const archiveWorkflow = (profile: AutoDLWorkflowProfile) => {
		void confirmDialog({
			title: `归档“${profile.name}”？`,
			description: "归档会停用自动选择，但保留全部历史版本和既有任务快照。",
			confirmLabel: "归档",
			confirmIcon: <Archive />,
			onConfirm: () => setWorkflowState(profile, { archived: true }),
		});
	};

	const validate = (profile: AutoDLWorkflowProfile, instance: AutoDLInstance) =>
		run(
			`validate:${profile.id}:${instance.id}`,
			async () => {
				await validateAutoDLWorkflow(profile.id, profile.currentVersionId, instance.id);
				await refresh();
				toast.success("工作流验证完成", { description: `${profile.name} · ${instance.name}` });
			},
			"验证工作流失败",
		);

	return (
		<SettingsPanelLayout
			title="MediaLink 配置"
			description="管理经常变化的云 GPU 连接和 ComfyUI 工作流版本。工作流不是写死的模型选项。"
			icon={<CloudCog className="size-4" />}
			actions={
				<>
					<Button type="button" variant="outline" onClick={() => setInstanceDialog(null)}>
						<Plus />
						添加实例
					</Button>
					<Button
						type="button"
						onClick={() => setWorkflowDialog({ mode: "create" })}
						disabled={instances.length === 0}
					>
						<Plus />
						添加工作流
					</Button>
				</>
			}
		>
			{isLoading ? <LoadingState /> : null}
			{error ? (
				<EmptyState title="无法读取 MediaLink 配置" description={errorMessage(error)} />
			) : null}
			{!isLoading && !error ? (
				<div className="mx-auto max-w-6xl space-y-7">
					<section className="space-y-3">
						<SectionHeading
							title="AutoDL 实例"
							description="每个实例独立保存 SSH 地址、动态端口、主机指纹和 Keychain 密码。"
						/>
						{instances.length === 0 ? (
							<EmptyState
								title="还没有云 GPU 实例"
								description="先添加 SSH 登录指令；ComfyUI 端口可使用默认值或手动指定。"
							/>
						) : (
							<div className="grid gap-3 xl:grid-cols-2">
								{instances.map((instance) => (
									<InstanceCard
										key={instance.id}
										instance={instance}
										check={checks[instance.id]}
										busyKey={busyKey}
										onCheck={() => void checkInstance(instance)}
										onEdit={() => setInstanceDialog(instance)}
										onRemove={() => removeInstance(instance)}
										onScan={() => void scanFingerprint(instance)}
										onClearPassword={() =>
											void run(
												`password:${instance.id}`,
												async () => {
													await clearAutoDLPassword(instance.id);
													await refresh();
												},
												"清除密码失败",
											)
										}
									/>
								))}
							</div>
						)}
					</section>

					<section className="space-y-3">
						<SectionHeading
							title="工作流注册表"
							description="添加、替换版本、复制、停用或归档；不会物理删除历史工作流。"
						/>
						{profiles.length === 0 ? (
							<EmptyState
								title="还没有工作流"
								description="导入任意 ComfyUI UI JSON，并逐项确认 MediaLink 字段映射。"
							/>
						) : (
							<div className="space-y-3">
								{profiles.map((profile) => (
									<WorkflowCard
										key={profile.id}
										profile={profile}
										instances={instances}
										busyKey={busyKey}
										onArchive={() => archiveWorkflow(profile)}
										onDuplicate={() => duplicateWorkflow(profile)}
										onReplace={() => setWorkflowDialog({ mode: "replace", profile })}
										onState={(input) => void setWorkflowState(profile, input)}
										onValidate={(instance) => void validate(profile, instance)}
									/>
								))}
							</div>
						)}
					</section>
				</div>
			) : null}

			<InstanceDialog
				instance={instanceDialog}
				open={instanceDialog !== undefined}
				onOpenChange={(open) => {
					if (!open) setInstanceDialog(undefined);
				}}
				onSaved={refresh}
			/>
			<AutoDLWorkflowDialog
				instances={instances}
				mode={workflowDialog?.mode ?? "create"}
				open={Boolean(workflowDialog)}
				profile={workflowDialog?.profile}
				onOpenChange={(open) => {
					if (!open) setWorkflowDialog(undefined);
				}}
				onSaved={refresh}
			/>
		</SettingsPanelLayout>
	);
};

const InstanceCard: React.FC<{
	busyKey: string;
	check?: AutoDLInstanceCheck;
	instance: AutoDLInstance;
	onCheck: () => void;
	onClearPassword: () => void;
	onEdit: () => void;
	onRemove: () => void;
	onScan: () => void;
}> = ({ busyKey, check, instance, onCheck, onClearPassword, onEdit, onRemove, onScan }) => (
	<Card>
		<CardHeader className="pb-2">
			<div className="flex items-start justify-between gap-3">
				<div className="min-w-0">
					<CardTitle className="truncate">{instance.name}</CardTitle>
					<CardDescription className="font-mono">
						{instance.sshUser}@{instance.host}:{instance.sshPort}
					</CardDescription>
				</div>
				<div className="flex gap-1">
					<Badge variant={instance.enabled ? "default" : "outline"}>
						{instance.enabled ? "已启用" : "已停用"}
					</Badge>
					<Badge variant={instance.hasPassword ? "secondary" : "destructive"}>
						{instance.hasPassword ? "密码已保存" : "缺少密码"}
					</Badge>
				</div>
			</div>
		</CardHeader>
		<CardContent className="space-y-3">
			<div className="grid gap-2 text-xs text-muted-foreground sm:grid-cols-2">
				<p>
					ComfyUI 端口 <span className="font-mono text-foreground">{instance.comfyPort}</span>
				</p>
				<p>
					主机指纹{" "}
					<span className="font-mono text-foreground">
						{instance.hostFingerprint ? `${instance.hostFingerprint.slice(0, 18)}…` : "未确认"}
					</span>
				</p>
			</div>
			{check ? (
				<div
					className={`rounded-sm border px-2.5 py-2 text-xs ${check.stage === "ready" ? "border-emerald-500/30 bg-emerald-500/5" : "border-border bg-ide-panel/50"}`}
				>
					<p
						className={
							check.stage === "ready"
								? "font-medium text-emerald-600 dark:text-emerald-400"
								: "font-medium text-foreground"
						}
					>
						{check.stage === "ready"
							? `可以生成 · ComfyUI ${check.comfyuiVersion || "已连接"} · 本地端口 ${check.localPort}`
							: readinessStageLabel(check.stage)}
					</p>
					<p className="mt-0.5 text-muted-foreground">
						{check.stage === "ready"
							? check.devices?.join(" · ") || "设备信息不可用"
							: check.reason || "正在准备实例"}
					</p>
				</div>
			) : null}
			<div className="flex flex-wrap gap-2">
				<ActionButton
					icon={<RefreshCw />}
					label="检查连接"
					busy={busyKey === `check:${instance.id}`}
					onClick={onCheck}
				/>
				<ActionButton
					icon={<Fingerprint />}
					label="扫描指纹"
					busy={busyKey === `scan:${instance.id}`}
					onClick={onScan}
				/>
				<ActionButton icon={<Pencil />} label="编辑" onClick={onEdit} />
				{instance.hasPassword ? (
					<ActionButton
						icon={<KeyRound />}
						label="清除密码"
						busy={busyKey === `password:${instance.id}`}
						onClick={onClearPassword}
					/>
				) : null}
				<Button type="button" size="sm" variant="ghost" onClick={onRemove}>
					移除
				</Button>
			</div>
		</CardContent>
	</Card>
);

const WorkflowCard: React.FC<{
	busyKey: string;
	instances: AutoDLInstance[];
	onArchive: () => void;
	onDuplicate: () => void;
	onReplace: () => void;
	onState: (input: { enabled?: boolean; autoSelectable?: boolean; archived?: boolean }) => void;
	onValidate: (instance: AutoDLInstance) => void;
	profile: AutoDLWorkflowProfile;
}> = ({ busyKey, instances, onArchive, onDuplicate, onReplace, onState, onValidate, profile }) => {
	const current = profile.versions.find(
		(version) => version.versionId === profile.currentVersionId,
	);
	return (
		<Card className={profile.archived ? "opacity-70" : undefined}>
			<CardHeader className="pb-2">
				<div className="flex flex-wrap items-start justify-between gap-3">
					<div>
						<div className="flex items-center gap-2">
							<CardTitle>{profile.name}</CardTitle>
							<Badge variant="secondary">{workflowRouteLabel(profile.routeId)}</Badge>
							<Badge variant="outline">{profile.currentVersionId}</Badge>
							{profile.archived ? <Badge variant="secondary">已归档</Badge> : null}
						</div>
						<CardDescription>{profile.description || "通用 AutoDL 云端工作流"}</CardDescription>
					</div>
					<div className="flex flex-wrap gap-2">
						<Button type="button" size="sm" variant="outline" onClick={onReplace}>
							<RefreshCw />
							替换版本
						</Button>
						<Button type="button" size="sm" variant="outline" onClick={onDuplicate}>
							<Copy />
							复制
						</Button>
						{!profile.archived ? (
							<Button type="button" size="sm" variant="ghost" onClick={onArchive}>
								<Archive />
								归档
							</Button>
						) : null}
					</div>
				</div>
			</CardHeader>
			<CardContent className="space-y-3">
				<div className="grid gap-2 rounded-sm border border-border/60 bg-ide-editor px-3 py-2 text-xs sm:grid-cols-3">
					<p>
						参考图{" "}
						<strong>
							{current?.references.min ?? 0}–{current?.references.max ?? 0}
						</strong>
					</p>
					<p>
						工作流摘要 <span className="font-mono">{current?.workflowDigest.slice(0, 10)}</span>
					</p>
					<p>
						API 摘要 <span className="font-mono">{current?.apiTemplateDigest.slice(0, 10)}</span>
					</p>
				</div>
				<div className="flex flex-wrap items-center gap-5 text-xs">
					<label className="flex items-center gap-2">
						<Switch
							size="sm"
							checked={profile.enabled}
							disabled={profile.archived || busyKey === `workflow:${profile.id}`}
							onCheckedChange={(checked) => onState({ enabled: checked })}
						/>
						启用
					</label>
					<label className="flex items-center gap-2">
						<Switch
							size="sm"
							checked={profile.autoSelectable}
							disabled={
								!profile.enabled || profile.archived || busyKey === `workflow:${profile.id}`
							}
							onCheckedChange={(checked) => onState({ autoSelectable: checked })}
						/>
						允许自动选择
					</label>
					<span className="text-muted-foreground">
						映射：{current?.bindingStatus === "confirmed" ? "已确认" : "待确认"}
					</span>
				</div>
				<div className="space-y-1.5 border-t border-border/60 pt-3">
					<p className="text-xs font-medium">逐实例验证</p>
					{instances.length === 0 ? (
						<p className="text-xs text-muted-foreground">暂无实例</p>
					) : (
						<div className="flex flex-wrap gap-2">
							{instances.map((instance) => {
								const validation = instance.workflowValidations?.find(
									(item) =>
										item.workflowProfileId === profile.id &&
										item.versionId === profile.currentVersionId,
								);
								return (
									<Button
										key={instance.id}
										type="button"
										size="sm"
										variant="outline"
										disabled={
											!instance.enabled ||
											!instance.hasPassword ||
											busyKey === `validate:${profile.id}:${instance.id}`
										}
										onClick={() => onValidate(instance)}
									>
										{busyKey === `validate:${profile.id}:${instance.id}` ? (
											<Loader2 className="animate-spin" />
										) : validation?.status === "ready" ? (
											<CheckCircle2 className="text-emerald-500" />
										) : (
											<Server />
										)}{" "}
										{instance.name} · {validationLabel(validation?.status)}
									</Button>
								);
							})}
						</div>
					)}
				</div>
			</CardContent>
		</Card>
	);
};

const InstanceDialog: React.FC<{
	instance: AutoDLInstance | null | undefined;
	onOpenChange: (open: boolean) => void;
	onSaved: () => Promise<void>;
	open: boolean;
}> = ({ instance, onOpenChange, onSaved, open }) => {
	const toast = useToast();
	const [name, setName] = useState("");
	const [ssh, setSSH] = useState("");
	const [password, setPassword] = useState("");
	const [comfyPort, setComfyPort] = useState(6006);
	const [startupCommand, setStartupCommand] = useState("");
	const [localPort, setLocalPort] = useState(0);
	const [fingerprint, setFingerprint] = useState("");
	const [enabled, setEnabled] = useState(false);
	const [saving, setSaving] = useState(false);
	const identity = instance?.id ?? "new";
	useEffect(() => {
		if (!open) return;
		setName(instance?.name ?? "");
		setSSH(instance ? sshCommand(instance) : "");
		setPassword("");
		setComfyPort(instance?.comfyPort ?? 6006);
		setStartupCommand(instance?.startupCommand ?? "");
		setLocalPort(instance?.localPort ?? 0);
		setFingerprint(instance?.hostFingerprint ?? "");
		setEnabled(instance?.enabled ?? false);
	}, [identity, instance, open]);
	const save = async () => {
		setSaving(true);
		try {
			await saveAutoDLInstance(
				{
					name: name.trim(),
					sshCommand: ssh.trim(),
					password: password || undefined,
					comfyPort,
					startupCommand: startupCommand.trim() || undefined,
					localPort,
					hostFingerprint: fingerprint.trim() || undefined,
					enabled,
				},
				instance?.id,
			);
			setPassword("");
			await onSaved();
			onOpenChange(false);
			toast.success(instance ? "实例已更新" : "实例已添加");
		} catch (err) {
			toast.error("保存实例失败", { description: errorMessage(err) });
		} finally {
			setSaving(false);
		}
	};
	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent className="max-w-xl">
				<AlertDialogHeader>
					<AlertDialogTitle>{instance ? "编辑 AutoDL 实例" : "添加 AutoDL 实例"}</AlertDialogTitle>
					<AlertDialogDescription>
						密码只写入 macOS Keychain；SSH 指令会被解析，不会原样保存或执行。
					</AlertDialogDescription>
				</AlertDialogHeader>
				<div className="grid gap-3 py-2 sm:grid-cols-2">
					<DialogField label="实例名称">
						<Input
							value={name}
							onChange={(event) => setName(event.target.value)}
							placeholder="例如 图像 GPU 1"
						/>
					</DialogField>
					<DialogField label="ComfyUI 端口">
						<Input
							type="number"
							min={1}
							max={65535}
							value={comfyPort}
							onChange={(event) => setComfyPort(Number(event.target.value))}
						/>
					</DialogField>
					<div className="sm:col-span-2">
						<DialogField label="SSH 登录指令">
							<Input
								value={ssh}
								onChange={(event) => setSSH(event.target.value)}
								placeholder="ssh -p 16109 root@connect.example.com"
							/>
						</DialogField>
					</div>
					<DialogField label={instance?.hasPassword ? "新密码（留空保持原值）" : "SSH 密码"}>
						<Input
							type="password"
							autoComplete="new-password"
							value={password}
							onChange={(event) => setPassword(event.target.value)}
						/>
					</DialogField>
					<DialogField label="已确认主机指纹">
						<Input
							value={fingerprint}
							onChange={(event) => setFingerprint(event.target.value)}
							placeholder="建议保存后使用扫描指纹"
						/>
					</DialogField>
					<label className="flex items-center gap-2 text-xs">
						<Switch checked={enabled} onCheckedChange={setEnabled} />
						启用此实例
					</label>
					<details className="sm:col-span-2 rounded-sm border border-border/70 bg-ide-panel/40 px-3 py-2">
						<summary className="cursor-pointer text-xs font-medium">高级设置</summary>
						<div className="mt-3 grid gap-3 sm:grid-cols-2">
							<div className="sm:col-span-2">
								<DialogField label="远程启动命令">
									<Textarea
										aria-label="远程启动命令"
										value={startupCommand}
										onChange={(event) => setStartupCommand(event.target.value)}
										placeholder="例如 /root/start_comfyui.sh"
									/>
								</DialogField>
							</div>
							<DialogField label="本地端口（0 为自动分配）">
								<Input
									aria-label="本地端口"
									type="number"
									min={0}
									max={65535}
									value={localPort}
									onChange={(event) => setLocalPort(Number(event.target.value))}
								/>
							</DialogField>
							<p className="self-end pb-2 text-xs text-muted-foreground">
								保留 0 可同时运行多个实例；只有固定端口需求时才手动指定。
							</p>
						</div>
					</details>
				</div>
				<AlertDialogFooter>
					<AlertDialogCancel disabled={saving}>取消</AlertDialogCancel>
					<Button
						type="button"
						disabled={!name.trim() || !ssh.trim() || saving}
						onClick={() => void save()}
					>
						{saving ? <Loader2 className="animate-spin" /> : <CloudCog />}保存实例
					</Button>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
};

const DialogField: React.FC<{ children: React.ReactNode; label: string }> = ({
	children,
	label,
}) => (
	<div className="space-y-1.5">
		<Label className="text-xs text-muted-foreground">{label}</Label>
		{children}
	</div>
);
const ActionButton: React.FC<{
	busy?: boolean;
	icon: React.ReactNode;
	label: string;
	onClick: () => void;
}> = ({ busy, icon, label, onClick }) => (
	<Button type="button" size="sm" variant="outline" disabled={busy} onClick={onClick}>
		{busy ? <Loader2 className="animate-spin" /> : icon}
		{label}
	</Button>
);
const SectionHeading: React.FC<{ description: string; title: string }> = ({
	description,
	title,
}) => (
	<div>
		<h3 className="text-sm font-semibold">{title}</h3>
		<p className="mt-0.5 text-xs text-muted-foreground">{description}</p>
	</div>
);
const EmptyState: React.FC<{ description: string; title: string }> = ({ description, title }) => (
	<div className="rounded-sm border border-dashed border-border bg-ide-panel/40 px-4 py-8 text-center">
		<p className="text-sm font-medium">{title}</p>
		<p className="mt-1 text-xs text-muted-foreground">{description}</p>
	</div>
);
const LoadingState = () => (
	<div className="flex h-40 items-center justify-center gap-2 text-xs text-muted-foreground">
		<Loader2 className="size-4 animate-spin" />
		读取 MediaLink 配置
	</div>
);
const validationLabel = (status?: string) =>
	status === "ready"
		? "可用"
		: status === "invalid"
			? "不兼容"
			: status === "stale"
				? "需重验"
				: "验证";
const readinessStageLabel = (stage?: AutoDLInstanceCheck["stage"]) =>
	stage === "connecting"
		? "连接 SSH"
		: stage === "probing"
			? "检查远程服务"
			: stage === "starting"
				? "启动服务"
				: stage === "waiting_health"
					? "等待健康"
					: stage === "tunneling"
						? "建立隧道"
						: stage === "validating_api"
							? "验证 API"
							: stage === "failed"
								? "准备失败"
								: "检查连接";
const workflowRouteLabel = (routeId: string) =>
	routeId === "autodl.minimax-h3" ? "H3 视频" : routeId === "autodl.image" ? "云端生图" : routeId;
const sshCommand = (instance: AutoDLInstance) =>
	`ssh -p ${instance.sshPort} ${instance.sshUser}@${instance.host}`;
const errorMessage = (error: unknown) =>
	typeof error === "object" && error !== null && "message" in error
		? String(error.message)
		: "操作失败，请稍后重试。";
