import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
	beginCodexAccountLogin,
	getCodexAccount,
	getCodexAccountLogin,
	getCodexImagePreflight,
} from "@/domains/settings/api/settings";
import { openExternalUrl } from "@/shared/desktop/actions";
import { CodexAccessPanel } from "./CodexAccessPanel";

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
	useToast: () => ({ error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() }),
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

		expect(await screen.findByText("检查失败：connection closed")).toBeInTheDocument();
		expect(screen.getByText("Codex 生图已就绪")).toBeInTheDocument();
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
