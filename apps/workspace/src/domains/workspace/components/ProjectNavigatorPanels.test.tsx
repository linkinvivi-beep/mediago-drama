import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { SWRConfig } from "swr";
import { afterEach, describe, expect, it, vi } from "vitest";
import { capabilitiesKey } from "@/domains/capabilities/api/capabilities";
import { projectSettingsGeneralTab } from "@/lib/stores/settings";
import { SettingsSidebarPanel, StudioTypesScreen } from "./ProjectNavigatorPanels";

describe("SettingsSidebarPanel", () => {
	afterEach(() => {
		cleanup();
	});

	it("selects the shortcuts settings tab", () => {
		const onSelectTab = vi.fn();

		render(
			<SettingsSidebarPanel
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={onSelectTab}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "快捷键" }));

		expect(onSelectTab).toHaveBeenCalledWith("shortcuts");
	});

	it("labels the settings shell for MediaLink", () => {
		render(
			<SettingsSidebarPanel
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={vi.fn()}
			/>,
		);

		expect(screen.getByRole("heading", { level: 2, name: "MediaLink 设置" })).toBeInTheDocument();
		expect(screen.queryByText(/MediaGo Drama/)).not.toBeInTheDocument();
	});

	it("does not expose the upstream GitHub help button", () => {
		render(
			<SWRConfig
				value={{
					fallback: { [capabilitiesKey]: { capabilities: [] } },
					revalidateOnMount: false,
				}}
			>
				<StudioTypesScreen
					activeCapabilityId={null}
					activeTab={null}
					onOpenSettings={vi.fn()}
					onSelectTab={vi.fn()}
				/>
			</SWRConfig>,
		);

		expect(screen.queryByRole("button", { name: "打开 GitHub 页面" })).not.toBeInTheDocument();
	});

	it("hides legacy API key and agent model entries", () => {
		render(
			<SettingsSidebarPanel
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={vi.fn()}
			/>,
		);

		expect(screen.queryByRole("button", { name: "模型接入" })).toBeNull();
		expect(screen.queryByRole("button", { name: "API 密钥" })).toBeNull();
	});

	it("shows the Codex access settings entry for Codex", () => {
		const onSelectTab = vi.fn();

		render(
			<SettingsSidebarPanel
				activeAgentBackendId="codex"
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={onSelectTab}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Codex 接入" }));

		expect(onSelectTab).toHaveBeenCalledWith("codex-access");
	});

	it("shows Codex skills for every agent backend and selects its own tab", () => {
		const onSelectTab = vi.fn();

		const { rerender } = render(
			<SettingsSidebarPanel
				activeAgentBackendId="codex"
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={onSelectTab}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Codex 技能" }));
		expect(onSelectTab).toHaveBeenLastCalledWith("codex-skills");

		rerender(
			<SettingsSidebarPanel
				activeAgentBackendId="opencode"
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={onSelectTab}
			/>,
		);
		expect(screen.getByRole("button", { name: "Codex 技能" })).toBeInTheDocument();
	});

	it("orders Codex skills after relay when present and before agent instructions", () => {
		const { rerender } = render(
			<SettingsSidebarPanel
				activeAgentBackendId="codex"
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={vi.fn()}
			/>,
		);
		const codexGroup = screen.getByText("生成配置").closest("section");
		expect(
			within(codexGroup as HTMLElement)
				.getAllByRole("button")
				.map((button) => button.textContent)
				.slice(0, 3),
		).toEqual(["MediaLink 配置", "Codex 接入", "Codex 技能"]);

		rerender(
			<SettingsSidebarPanel
				activeAgentBackendId="opencode"
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={vi.fn()}
			/>,
		);
		const otherGroup = screen.getByText("生成配置").closest("section");
		expect(
			within(otherGroup as HTMLElement)
				.getAllByRole("button")
				.map((button) => button.textContent)
				.slice(0, 3),
		).toEqual(["MediaLink 配置", "Codex 技能", "智能体指令"]);
	});

	it("shows the app updates settings entry", () => {
		const onSelectTab = vi.fn();

		render(
			<SettingsSidebarPanel
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={onSelectTab}
			/>,
		);

		fireEvent.click(screen.getByRole("button", { name: "应用更新" }));
		expect(onSelectTab).toHaveBeenCalledWith("updates");
	});

	it("shows app updates at the bottom of the workspace settings group", () => {
		render(
			<SettingsSidebarPanel
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={vi.fn()}
			/>,
		);

		const workspaceGroup = screen.getByText("工作区").closest("section");
		expect(workspaceGroup).not.toBeNull();
		expect(
			within(workspaceGroup as HTMLElement)
				.getAllByRole("button")
				.map((button) => button.textContent),
		).toEqual(["基础设置", "快捷键", "用量与账单", "应用更新"]);
	});

	it("hides the Codex access settings entry for non-Codex agents", () => {
		render(
			<SettingsSidebarPanel
				activeAgentBackendId="opencode"
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={vi.fn()}
			/>,
		);

		expect(screen.queryByRole("button", { name: "Codex 接入" })).toBeNull();
	});

	it("hides the Jianying draft settings entry while it is disabled", () => {
		render(
			<SettingsSidebarPanel
				activeTab="appearance"
				isProjectSettings={false}
				onBack={vi.fn()}
				onSelectTab={vi.fn()}
			/>,
		);

		expect(screen.queryByRole("button", { name: "剪映草稿" })).toBeNull();
	});

	it("adds project settings above global settings in project mode", () => {
		const onSelectTab = vi.fn();

		render(
			<SettingsSidebarPanel
				activeTab={projectSettingsGeneralTab}
				isProjectSettings
				onBack={vi.fn()}
				onSelectTab={onSelectTab}
			/>,
		);

		expect(screen.getByText("项目设置")).toBeTruthy();
		expect(screen.getByRole("button", { name: "常规" }).className).toContain("bg-ide-list-active");
		expect(screen.getByRole("button", { name: "基础设置" })).toBeTruthy();
		expect(screen.queryByRole("button", { name: "API 密钥" })).toBeNull();

		fireEvent.click(screen.getByRole("button", { name: "基础设置" }));
		expect(onSelectTab).toHaveBeenCalledWith("appearance");
	});
});
