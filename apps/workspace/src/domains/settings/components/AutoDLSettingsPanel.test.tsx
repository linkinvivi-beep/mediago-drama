import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { SWRConfig } from "swr";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAutoDLInstance, getAutoDLSettings } from "@/domains/settings/api/autodl";
import { AutoDLSettingsPanel } from "./AutoDLSettingsPanel";

const testSpies = vi.hoisted(() => ({
	toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));

vi.mock("@/domains/settings/api/autodl", async (importOriginal) => {
	const actual = await importOriginal<typeof import("@/domains/settings/api/autodl")>();
	return {
		...actual,
		checkAutoDLInstance: vi.fn(),
		getAutoDLInstanceReadiness: vi.fn().mockResolvedValue({ connected: false, stage: "probing" }),
		getAutoDLSettings: vi.fn(),
	};
});

vi.mock("@/hooks/useToast", () => ({ useToast: () => testSpies.toast }));
vi.mock("@/shared/components/callable/ConfirmDialog", () => ({ confirmDialog: vi.fn() }));

const instance = {
	id: "instance-a",
	name: "5090",
	host: "connect.example.com",
	sshPort: 16109,
	sshUser: "root",
	comfyPort: 6006,
	startupCommand: "/root/start_comfyui.sh",
	localPort: 0,
	hostFingerprint: "SHA256:confirmed",
	credentialRef: "instance-a",
	enabled: true,
	hasPassword: true,
};

describe("AutoDLSettingsPanel", () => {
	beforeEach(() => {
		vi.clearAllMocks();
		vi.mocked(getAutoDLSettings).mockResolvedValue({
			instances: [instance],
			workflowProfiles: [],
			workflowDefaults: [],
		});
	});
	afterEach(cleanup);

	it("preserves startup command and automatic local port in advanced settings", async () => {
		renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
		expect(screen.getByText("高级设置")).toBeInTheDocument();
		expect(screen.getByRole("textbox", { name: "远程启动命令" })).toHaveValue(
			"/root/start_comfyui.sh",
		);
		expect(screen.getByRole("spinbutton", { name: "本地端口" })).toHaveValue(0);
	});

	it("shows ready checks as directly usable", async () => {
		vi.mocked(checkAutoDLInstance).mockResolvedValue({
			connected: true,
			stage: "ready",
			localPort: 43123,
			comfyuiVersion: "0.3.60",
			devices: ["RTX 5090"],
		});
		renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "检查连接" }));
		expect(await screen.findByText(/可以生成/)).toBeInTheDocument();
		expect(screen.getByText(/RTX 5090/)).toBeInTheDocument();
	});

	it("shows actionable server errors instead of internal error", async () => {
		vi.mocked(checkAutoDLInstance).mockRejectedValue(new Error("本地端口已被占用，请改用自动分配"));
		renderPanel();
		fireEvent.click(await screen.findByRole("button", { name: "检查连接" }));
		await waitFor(() =>
			expect(testSpies.toast.error).toHaveBeenCalledWith("检查实例失败", {
				description: "本地端口已被占用，请改用自动分配",
			}),
		);
	});
});

const renderPanel = () =>
	render(
		<SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}>
			<AutoDLSettingsPanel />
		</SWRConfig>,
	);
