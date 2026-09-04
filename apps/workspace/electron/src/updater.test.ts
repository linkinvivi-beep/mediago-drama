import { beforeEach, describe, expect, it, vi } from "vitest";

const handlers = new Map<string, (...args: unknown[]) => unknown>();

vi.mock("electron", () => ({
	app: {
		isPackaged: true,
		getVersion: () => "1.0.0",
	},
	ipcMain: {
		handle: vi.fn((channel: string, handler: (...args: unknown[]) => unknown) => {
			handlers.set(channel, handler);
		}),
	},
}));

vi.mock("electron-updater", () => ({
	default: {
		autoUpdater: {
			on: vi.fn(),
			checkForUpdates: vi.fn(),
			downloadUpdate: vi.fn(),
			quitAndInstall: vi.fn(),
		},
	},
}));

describe("MediaLink desktop updater", () => {
	beforeEach(() => {
		handlers.clear();
		vi.clearAllMocks();
	});

	it("reports that no release feed is configured", async () => {
		const { registerDesktopUpdater } = await import("./updater");
		registerDesktopUpdater({ authorizeIpcSender: vi.fn(), getWindow: () => null });

		const getCapability = handlers.get("desktop:get-update-capability");
		expect(getCapability).toBeDefined();
		expect(getCapability?.({})).toEqual({
			supportsAutoUpdate: false,
			releasePageUrl: "",
			reason: "releaseFeedNotConfigured",
		});
	});
});
