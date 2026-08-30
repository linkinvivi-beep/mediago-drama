import { CheckCircle2, CircleAlert, Image, Loader2, LogOut, RefreshCw } from "lucide-react";
import type React from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import useSWR, { useSWRConfig } from "swr";
import { isAgentRuntimeConfigKey } from "@/domains/agent/api/agent";
import {
	type CodexLoginAttempt,
	beginCodexAccountLogin,
	cancelCodexAccountLogin,
	codexAccountKey,
	codexImagePreflightKey,
	getCodexAccount,
	getCodexAccountLogin,
	getCodexImagePreflight,
	logoutCodexAccount,
} from "@/domains/settings/api/settings";
import { SettingsPanelLayout } from "@/domains/settings/components/SettingsPanelLayout";
import { useToast } from "@/hooks/useToast";
import { confirmDialog } from "@/shared/components/callable/ConfirmDialog";
import { Button } from "@/shared/components/ui/button";
import { openExternalUrl } from "@/shared/desktop/actions";

export const CodexAccessPanel: React.FC = () => {
	const toast = useToast();
	const { mutate: mutateGlobal } = useSWRConfig();
	const {
		data: account,
		error: accountError,
		isLoading,
		mutate,
	} = useSWR(codexAccountKey, getCodexAccount);
	const {
		data: preflight,
		error: preflightError,
		isLoading: preflightLoading,
		isValidating: preflightValidating,
		mutate: mutatePreflight,
	} = useSWR(codexImagePreflightKey, getCodexImagePreflight);
	const [attempt, setAttempt] = useState<CodexLoginAttempt>();
	const [busy, setBusy] = useState("");
	const [refreshError, setRefreshError] = useState("");
	const mountedRef = useRef(true);
	const manualRefreshRequestRef = useRef(0);
	const accountChangeRequestRef = useRef(0);

	useEffect(() => {
		mountedRef.current = true;
		return () => {
			mountedRef.current = false;
			manualRefreshRequestRef.current += 1;
			accountChangeRequestRef.current += 1;
		};
	}, []);

	const refreshRuntimeCache = useCallback(() => {
		void mutateGlobal(isAgentRuntimeConfigKey, undefined, { revalidate: true });
	}, [mutateGlobal]);

	const warnPreflightRefresh = useCallback(
		(error: unknown) => {
			if (!mountedRef.current) return;
			toast.warning("Codex 生图状态刷新失败", { description: errorMessage(error) });
		},
		[toast],
	);

	const refreshReadiness = async () => {
		const requestID = ++manualRefreshRequestRef.current;
		setBusy("refresh");
		setRefreshError("");
		try {
			const [nextAccount, nextPreflight] = await Promise.all([
				getCodexAccount(),
				getCodexImagePreflight(),
			]);
			if (!currentRequest(mountedRef, manualRefreshRequestRef, requestID)) return;
			await Promise.all([mutate(nextAccount, false), mutatePreflight(nextPreflight, false)]);
			refreshRuntimeCache();
			toast.success("Codex 生图检查完成");
		} catch (error) {
			if (!currentRequest(mountedRef, manualRefreshRequestRef, requestID)) return;
			const message = errorMessage(error);
			setRefreshError(message);
			toast.error("Codex 生图检查失败", { description: message });
		} finally {
			if (currentRequest(mountedRef, manualRefreshRequestRef, requestID)) setBusy("");
		}
	};

	useEffect(() => {
		if (!attempt || attempt.status !== "pending") return;
		let disposed = false;
		const check = async () => {
			try {
				const next = await getCodexAccountLogin(attempt.loginId);
				if (disposed || !mountedRef.current) return;
				setAttempt(next);
				if (next.status === "completed") {
					manualRefreshRequestRef.current += 1;
					const requestID = ++accountChangeRequestRef.current;
					toast.success("ChatGPT 登录成功", { description: "已复用全局 Codex 登录态。" });
					const [accountResult, preflightResult] = await Promise.allSettled([
						getCodexAccount(),
						getCodexImagePreflight(),
					]);
					if (!currentRequest(mountedRef, accountChangeRequestRef, requestID)) return;
					refreshRuntimeCache();
					let accountRefreshError =
						accountResult.status === "rejected" ? accountResult.reason : undefined;
					if (accountResult.status === "fulfilled") {
						try {
							await mutate(accountResult.value, false);
						} catch (error) {
							accountRefreshError = error;
						}
					}
					let preflightRefreshError =
						preflightResult.status === "rejected" ? preflightResult.reason : undefined;
					if (preflightResult.status === "fulfilled") {
						try {
							await mutatePreflight(preflightResult.value, false);
						} catch (error) {
							preflightRefreshError = error;
						}
					}
					if (!currentRequest(mountedRef, accountChangeRequestRef, requestID)) return;
					if (accountRefreshError) {
						toast.warning("ChatGPT 账号状态刷新失败", {
							description: errorMessage(accountRefreshError),
						});
					}
					if (preflightRefreshError) {
						warnPreflightRefresh(preflightRefreshError);
					}
				} else if (next.status !== "pending" && next.status !== "canceled") {
					toast.error("ChatGPT 登录失败", { description: next.error || "请重新发起登录。" });
				}
			} catch (error) {
				if (!disposed && mountedRef.current) {
					toast.error("检查登录状态失败", { description: errorMessage(error) });
					setAttempt(undefined);
				}
			}
		};
		const interval = window.setInterval(() => void check(), 1500);
		void check();
		return () => {
			disposed = true;
			window.clearInterval(interval);
		};
	}, [
		attempt?.loginId,
		attempt?.status,
		mutate,
		mutatePreflight,
		refreshRuntimeCache,
		toast,
		warnPreflightRefresh,
	]);

	const startLogin = async () => {
		setBusy("login");
		try {
			const next = await beginCodexAccountLogin();
			if (!mountedRef.current) return;
			setAttempt(next);
			if (next.authUrl) await openExternalUrl(next.authUrl);
			if (!mountedRef.current) return;
			toast.info("ChatGPT 登录页已打开", { description: "请在浏览器中完成授权。" });
		} catch (error) {
			if (mountedRef.current) {
				toast.error("无法开始登录", { description: errorMessage(error) });
			}
		} finally {
			if (mountedRef.current) setBusy("");
		}
	};

	const reopenLogin = async () => {
		if (!attempt?.authUrl) return;
		try {
			await openExternalUrl(attempt.authUrl);
		} catch (error) {
			if (mountedRef.current) {
				toast.error("打开登录页失败", { description: errorMessage(error) });
			}
		}
	};

	const cancelLogin = async () => {
		if (!attempt) return;
		setBusy("cancel");
		try {
			const next = await cancelCodexAccountLogin(attempt.loginId);
			if (!mountedRef.current) return;
			setAttempt(next);
			toast.info("登录已取消");
		} catch (error) {
			if (mountedRef.current) {
				toast.error("取消失败", { description: errorMessage(error) });
			}
		} finally {
			if (mountedRef.current) setBusy("");
		}
	};

	const logout = async () => {
		setBusy("logout");
		let next;
		try {
			next = await logoutCodexAccount();
		} catch (error) {
			if (mountedRef.current) {
				toast.error("退出失败", { description: errorMessage(error) });
				setBusy("");
			}
			return false;
		}

		manualRefreshRequestRef.current += 1;
		const requestID = ++accountChangeRequestRef.current;
		try {
			await mutate(next, false);
		} catch (error) {
			if (currentRequest(mountedRef, accountChangeRequestRef, requestID)) {
				toast.warning("ChatGPT 账号状态刷新失败", { description: errorMessage(error) });
			}
		}
		if (currentRequest(mountedRef, accountChangeRequestRef, requestID)) {
			toast.success("已退出全局 Codex 账号");
		}

		try {
			await mutatePreflight(loggedOutPreflight, false);
			const nextPreflight = await getCodexImagePreflight();
			if (currentRequest(mountedRef, accountChangeRequestRef, requestID)) {
				await mutatePreflight(nextPreflight, false);
			}
		} catch (error) {
			if (currentRequest(mountedRef, accountChangeRequestRef, requestID)) {
				warnPreflightRefresh(error);
			}
		}
		if (currentRequest(mountedRef, accountChangeRequestRef, requestID)) setBusy("");
		return true;
	};

	const confirmLogout = () => {
		void confirmDialog({
			title: "退出全局 Codex 账号？",
			description: "退出后，共享同一 Codex 目录的 CLI、IDE 和其他客户端也需要重新登录。",
			confirmLabel: "退出全局账号",
			confirmIcon: <LogOut />,
			onConfirm: logout,
		});
	};

	const loggedIn = account?.status === "loggedIn";
	const pending = attempt?.status === "pending";
	const readiness = codexImageReadiness(preflight?.reason, preflight?.ready);
	const readinessError = refreshError || (preflightError ? errorMessage(preflightError) : "");

	return (
		<SettingsPanelLayout
			title="Codex 接入"
			description="复用本机 Codex 的全局 ChatGPT 登录，检查内置生图能力。"
			icon={<Image className="size-4" />}
			actions={
				<Button
					type="button"
					variant="outline"
					size="sm"
					disabled={(busy !== "" && busy !== "refresh") || preflightLoading || preflightValidating}
					onClick={() => void refreshReadiness()}
				>
					{busy === "refresh" || preflightValidating ? (
						<Loader2 className="animate-spin" />
					) : (
						<RefreshCw />
					)}
					刷新并测试
				</Button>
			}
		>
			<div className="mx-auto flex w-full max-w-3xl flex-col gap-4">
				<section className="rounded-lg border border-border bg-card p-4">
					<div className="flex flex-wrap items-start justify-between gap-3">
						<div>
							<h3 className="text-sm font-semibold text-foreground">共享 ChatGPT 登录</h3>
							{isLoading ? (
								<p className="mt-1 text-sm text-muted-foreground">正在读取全局账号…</p>
							) : accountError ? (
								<p className="mt-1 text-sm text-destructive">无法读取全局账号</p>
							) : loggedIn ? (
								<>
									<p className="mt-1 text-sm text-foreground">
										{account?.email || "已登录 ChatGPT"}
									</p>
									<p className="mt-1 text-xs text-muted-foreground">
										{planLabel(account?.planType)} · {account?.codexHome}
									</p>
								</>
							) : (
								<p className="mt-1 text-sm text-muted-foreground">尚未登录 ChatGPT</p>
							)}
						</div>
						<div className="flex flex-wrap gap-2">
							{loggedIn ? (
								<Button
									type="button"
									variant="outline"
									size="sm"
									onClick={confirmLogout}
									disabled={busy !== ""}
								>
									<LogOut />
									退出全局账号
								</Button>
							) : pending ? (
								<>
									<Button
										type="button"
										variant="outline"
										size="sm"
										onClick={() => void reopenLogin()}
									>
										重新打开浏览器
									</Button>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										onClick={() => void cancelLogin()}
										disabled={busy !== ""}
									>
										取消登录
									</Button>
								</>
							) : (
								<Button
									type="button"
									size="sm"
									onClick={() => void startLogin()}
									disabled={busy !== ""}
								>
									使用 ChatGPT 登录
								</Button>
							)}
						</div>
					</div>
				</section>

				<section className="rounded-lg border border-border bg-card p-4">
					<div className="flex items-start gap-3" role="status" aria-live="polite">
						{preflightLoading ? (
							<Loader2 className="mt-0.5 size-5 animate-spin text-muted-foreground" />
						) : readiness.ready ? (
							<CheckCircle2 className="mt-0.5 size-5 text-emerald-500" />
						) : (
							<CircleAlert className="mt-0.5 size-5 text-amber-500" />
						)}
						<div>
							<h3 className="text-sm font-semibold text-foreground">
								{preflightLoading ? "正在检查 Codex 生图能力…" : readiness.label}
							</h3>
							<p className="mt-1 text-xs text-muted-foreground">
								生图会使用当前 ChatGPT 账号的 Codex 配额。
							</p>
						</div>
					</div>
					{readinessError ? (
						<p className="mt-2 text-xs text-destructive" role="alert">
							检查失败：{readinessError}
						</p>
					) : null}
				</section>
			</div>
		</SettingsPanelLayout>
	);
};

const loggedOutPreflight = {
	accountStatus: "notLoggedIn",
	imageGeneration: false,
	ready: false,
	reason: "not_logged_in",
};

const currentRequest = (
	mountedRef: React.RefObject<boolean>,
	requestRef: React.RefObject<number>,
	requestID: number,
) => mountedRef.current && requestRef.current === requestID;

const codexImageReadiness = (reason?: string, ready?: boolean) => {
	if (ready || reason === "ready") return { ready: true, label: "Codex 生图已就绪" };
	switch (reason) {
		case "not_logged_in":
			return { ready: false, label: "登录 ChatGPT 后可检查生图能力" };
		case "cli_unavailable":
			return { ready: false, label: "内置 Codex CLI 不可用" };
		case "capability_disabled":
			return { ready: false, label: "当前账号未启用 Codex 生图" };
		case "capability_unavailable":
			return { ready: false, label: "无法读取 Codex 生图能力" };
		default:
			return { ready: false, label: "Codex 生图尚未就绪" };
	}
};

const planLabel = (value?: string) => {
	if (!value) return "ChatGPT 订阅";
	return `ChatGPT ${value.charAt(0).toUpperCase()}${value.slice(1)}`;
};

const errorMessage = (error: unknown) =>
	error instanceof Error && error.message ? error.message : "操作失败，请重试。";
