import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { StrictMode } from "react";
import {
	beginCodexAccountLogin,
	getCodexAccount,
	getCodexAccountLogin,
	getCodexImagePreflight,
	logoutCodexAccount,
} from "@/domains/settings/api/settings";
import { openExternalUrl } from "@/shared/desktop/actions";
import { CodexAccessPanel } from "./CodexAccessPanel";

const testSpies = vi.hoisted(() => ({
	confirmDialog: vi.fn(),
	toast: {
		error: vi.fn(),
		info: vi.fn(),
		success: vi.fn(),
		warning: vi.fn(),
	},
}));

vi.mock("@/domains/settings/api/settings", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@/domains/settings/api/settings")>();
	return {
		...actual,
		beginCodexAccountLogin: vi.fn(),
		cancelCodexAccountLogin: vi.fn(),
		getCodexAccount: vi.fn(),
		getCodexAccountLogin: vi.fn(),
		getCodexImagePreflight: vi.fn(),
		logoutCodexAccount: vi.fn(),
	};
});

vi.mock("@/shared/desktop/actions", () => ({ openExternalUrl: vi.fn() }));

vi.mock("@/hooks/useToast", () => ({
	useToast: () => testSpies.toast,
}));

vi.mock("@/shared/components/callable/ConfirmDialog", () => ({
	confirmDialog: testSpies.confirmDialog,
}));

describe("CodexAccessPanel", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(getCodexImagePreflight).mockResolvedValue({
			accountStatus: "loggedIn",
			imageGeneration: true,
			ready: true,
			reason: "ready",
		});
	});

	afterEach(cleanup);

	it("reuses and displays the shared global Codex account", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "loggedIn",
			email: "user@example.com",
			planType: "plus",
			codexHome: "/Users/test/.codex",
			shared: true,
		});

		renderPanel();

		expect(await screen.findByText("user@example.com")).toBeInTheDocument();
		expect(screen.getByText("ChatGPT Plus · /Users/test/.codex")).toBeInTheDocument();
		expect(screen.getByText("Codex 生图已就绪")).toBeInTheDocument();
		expect(screen.getByRole("status")).toHaveTextContent("Codex 生图已就绪");
		expect(screen.getByText("生图会使用当前 ChatGPT 账号的 Codex 配额。")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "刷新并测试" })).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "退出全局账号" })).toBeInTheDocument();
		expect(beginCodexAccountLogin).not.toHaveBeenCalled();
	});

	it("shows the not logged in preflight state", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "notLoggedIn",
			codexHome: "/Users/test/.codex",
			shared: true,
		});
		vi.mocked(getCodexImagePreflight).mockResolvedValue({
			accountStatus: "notLoggedIn",
			imageGeneration: false,
			ready: false,
			reason: "not_logged_in",
		});

		renderPanel();

		expect(await screen.findByText("尚未登录 ChatGPT")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "使用 ChatGPT 登录" })).toBeInTheDocument();
	});

	it("shows capability unavailable without offering API credentials", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "loggedIn",
			email: "user@example.com",
			planType: "plus",
			codexHome: "/Users/test/.codex",
			shared: true,
		});
		vi.mocked(getCodexImagePreflight).mockResolvedValue({
			accountStatus: "loggedIn",
			imageGeneration: false,
			ready: false,
			reason: "capability_unavailable",
		});

		renderPanel();

		expect(await screen.findByText("无法读取 Codex 生图能力")).toBeInTheDocument();
		expect(screen.queryByText(/API Key/i)).not.toBeInTheDocument();
		expect(screen.queryByText(/Base URL/i)).not.toBeInTheDocument();
		expect(screen.queryByText(/Image API/i)).not.toBeInTheDocument();
	});

	it("shows a refresh failure and keeps the last readiness result", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "loggedIn",
			email: "user@example.com",
			planType: "plus",
			codexHome: "/Users/test/.codex",
			shared: true,
		});
		vi.mocked(getCodexImagePreflight)
			.mockResolvedValueOnce({
				accountStatus: "loggedIn",
				imageGeneration: true,
				ready: true,
				reason: "ready",
			})
			.mockRejectedValueOnce(new Error("connection closed"));

		renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "刷新并测试" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("检查失败：connection closed");
		expect(screen.getByText("Codex 生图已就绪")).toBeInTheDocument();
	});

	it("keeps a completed login successful when preflight refresh fails", async () => {
		vi.mocked(getCodexAccount)
			.mockResolvedValueOnce({
				status: "notLoggedIn",
				codexHome: "/Users/test/.codex",
				shared: true,
			})
			.mockResolvedValue({
				status: "loggedIn",
				email: "user@example.com",
				planType: "plus",
				codexHome: "/Users/test/.codex",
				shared: true,
			});
		vi.mocked(getCodexImagePreflight)
			.mockResolvedValueOnce({
				accountStatus: "notLoggedIn",
				imageGeneration: false,
				ready: false,
				reason: "not_logged_in",
			})
			.mockRejectedValue(new Error("preflight offline"));
		vi.mocked(beginCodexAccountLogin).mockResolvedValue({
			loginId: "login-completed",
			status: "pending",
		});
		vi.mocked(getCodexAccountLogin).mockResolvedValue({
			loginId: "login-completed",
			status: "completed",
		});

		renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "使用 ChatGPT 登录" }));

		await waitFor(() =>
			expect(testSpies.toast.success).toHaveBeenCalledWith("ChatGPT 登录成功", {
				description: "已复用全局 Codex 登录态。",
			}),
		);
		expect(testSpies.toast.error).not.toHaveBeenCalledWith("检查登录状态失败", expect.anything());
		await waitFor(() =>
			expect(testSpies.toast.warning).toHaveBeenCalledWith("Codex 生图状态刷新失败", {
				description: "preflight offline",
			}),
		);
	});

	it("returns logout success when preflight refresh fails", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "loggedIn",
			email: "user@example.com",
			planType: "plus",
			codexHome: "/Users/test/.codex",
			shared: true,
		});
		vi.mocked(getCodexImagePreflight)
			.mockResolvedValueOnce({
				accountStatus: "loggedIn",
				imageGeneration: true,
				ready: true,
				reason: "ready",
			})
			.mockRejectedValue(new Error("preflight offline"));
		vi.mocked(logoutCodexAccount).mockResolvedValue({
			status: "notLoggedIn",
			codexHome: "/Users/test/.codex",
			shared: true,
		});
		let logoutResult: Promise<boolean> | undefined;
		testSpies.confirmDialog.mockImplementation(
			(input: { onConfirm: () => boolean | Promise<boolean> }) => {
				logoutResult = Promise.resolve(input.onConfirm());
			},
		);

		renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "退出全局账号" }));
		await waitFor(() => expect(logoutResult).toBeDefined());

		await expect(logoutResult).resolves.toBe(true);
		expect(testSpies.toast.success).toHaveBeenCalledWith("已退出全局 Codex 账号");
		expect(testSpies.toast.error).not.toHaveBeenCalledWith("退出失败", expect.anything());
		await waitFor(() =>
			expect(testSpies.toast.warning).toHaveBeenCalledWith("Codex 生图状态刷新失败", {
				description: "preflight offline",
			}),
		);
	});

	it("does not publish a late manual refresh failure after unmount", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "loggedIn",
			email: "user@example.com",
			planType: "plus",
			codexHome: "/Users/test/.codex",
			shared: true,
		});
		const latePreflight = deferredPromise<never>();
		vi.mocked(getCodexImagePreflight)
			.mockResolvedValueOnce({
				accountStatus: "loggedIn",
				imageGeneration: true,
				ready: true,
				reason: "ready",
			})
			.mockImplementationOnce(() => latePreflight.promise);

		const view = renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "刷新并测试" }));
		view.unmount();
		await act(async () => {
			latePreflight.reject(new Error("late failure"));
			await Promise.resolve();
		});

		expect(testSpies.toast.error).not.toHaveBeenCalledWith("Codex 生图检查失败", {
			description: "late failure",
		});
	});

	it("keeps the newest manual refresh when an older request finishes later", async () => {
		const loggedInAccount = {
			status: "loggedIn" as const,
			email: "user@example.com",
			planType: "plus",
			codexHome: "/Users/test/.codex",
			shared: true,
		};
		const oldAccount = deferredPromise<typeof loggedInAccount>();
		const oldPreflight = deferredPromise<{
			accountStatus: string;
			imageGeneration: boolean;
			ready: boolean;
			reason: string;
		}>();
		vi.mocked(getCodexAccount)
			.mockResolvedValueOnce(loggedInAccount)
			.mockImplementationOnce(() => oldAccount.promise)
			.mockResolvedValue(loggedInAccount);
		vi.mocked(getCodexImagePreflight)
			.mockResolvedValueOnce({
				accountStatus: "loggedIn",
				imageGeneration: true,
				ready: true,
				reason: "ready",
			})
			.mockImplementationOnce(() => oldPreflight.promise)
			.mockResolvedValue({
				accountStatus: "loggedIn",
				imageGeneration: false,
				ready: false,
				reason: "capability_disabled",
			});

		renderPanel();
		const refresh = await screen.findByRole("button", { name: "刷新并测试" });
		fireEvent.click(refresh);
		await waitFor(() => expect(getCodexImagePreflight).toHaveBeenCalledTimes(2));
		fireEvent.click(refresh);

		await waitFor(() =>
			expect(screen.getByRole("status")).toHaveTextContent("当前账号未启用 Codex 生图"),
		);
		await act(async () => {
			oldAccount.resolve(loggedInAccount);
			oldPreflight.resolve({
				accountStatus: "loggedIn",
				imageGeneration: true,
				ready: true,
				reason: "ready",
			});
			await Promise.resolve();
		});

		expect(screen.getByRole("status")).toHaveTextContent("当前账号未启用 Codex 生图");
	});

	it("keeps mounted guards active across a StrictMode effect replay", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "loggedIn",
			email: "user@example.com",
			planType: "plus",
			codexHome: "/Users/test/.codex",
			shared: true,
		});

		render(
			<StrictMode>
				<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}>
					<CodexAccessPanel />
				</SWRConfig>
			</StrictMode>,
		);
		fireEvent.click(await screen.findByRole("button", { name: "刷新并测试" }));

		await waitFor(() => expect(testSpies.toast.success).toHaveBeenCalledWith("Codex 生图检查完成"));
	});

	it("opens the browser URL returned by bundled Codex", async () => {
		vi.mocked(getCodexAccount).mockResolvedValue({
			status: "notLoggedIn",
			codexHome: "/Users/test/.codex",
			shared: true,
		});
		vi.mocked(beginCodexAccountLogin).mockResolvedValue({
			loginId: "login-123",
			authUrl: "https://chatgpt.com/auth/test",
			status: "pending",
		});
		vi.mocked(getCodexAccountLogin).mockResolvedValue({
			loginId: "login-123",
			authUrl: "https://chatgpt.com/auth/test",
			status: "pending",
		});

		renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "使用 ChatGPT 登录" }));

		await waitFor(() =>
			expect(openExternalUrl).toHaveBeenCalledWith("https://chatgpt.com/auth/test"),
		);
		expect(await screen.findByRole("button", { name: "重新打开浏览器" })).toBeInTheDocument();
	});
});

const renderPanel = () =>
	render(
		<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}>
			<CodexAccessPanel />
		</SWRConfig>,
	);

const deferredPromise = <T,>() => {
	let resolve!: (value: T | PromiseLike<T>) => void;
	let reject!: (reason?: unknown) => void;
	const promise = new Promise<T>((resolvePromise, rejectPromise) => {
		resolve = resolvePromise;
		reject = rejectPromise;
	});
	return { promise, reject, resolve };
};
