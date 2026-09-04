import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
	assertDarwinArm64Target,
	assertStagedServiceBinariesMatchTarget,
	createElectronAppPackage,
} from "./stage-electron-app";

describe("MediaLink Electron staging", () => {
	it("builds an unpublished macOS arm64-only package configuration", () => {
		const stagedPackage = createElectronAppPackage(
			{
				version: "1.2.3",
				license: "Apache-2.0",
				dependencies: { "electron-updater": "^6.6.1" },
			},
			"42.4.1",
			false,
		);

		expect(stagedPackage.productName).toBe("MediaLink");
		expect(stagedPackage.build.productName).toBe("MediaLink");
		expect(stagedPackage.build.appId).toBe("app.medialink.desktop");
		expect(stagedPackage.build.artifactName).toContain("MediaLink");
		expect(stagedPackage.build.artifactName).toBe("MediaLink-${version}-macos-arm64.${ext}");
		expect(stagedPackage.build.mac.icon).toBe("../../build/icons/icon.icns");
		expect(stagedPackage).not.toHaveProperty("dependencies");
		const config = stagedPackage.build as Record<string, unknown>;
		expect(config.win).toBeUndefined();
		expect(config.publish).toBeUndefined();
	});

	it("rejects every staging target except darwin-arm64", () => {
		expect(() => assertDarwinArm64Target(undefined)).toThrow(
			"MEDIAGO_ELECTRON_TARGET_PLATFORM must be darwin-arm64",
		);
		expect(() => assertDarwinArm64Target("windows-x64")).toThrow(
			"MEDIAGO_ELECTRON_TARGET_PLATFORM must be darwin-arm64",
		);
		expect(() => assertDarwinArm64Target("darwin-arm64")).not.toThrow();
	});

	it("rejects a stale staged service binary before packaging", () => {
		const root = mkdtempSync(join(tmpdir(), "medialink-staging-"));
		const targetBinDir = join(root, "target");
		const stagedBinDir = join(root, "staged");
		mkdirSync(targetBinDir);
		mkdirSync(stagedBinDir);
		writeFileSync(join(targetBinDir, "mediago-server"), "current-server");
		writeFileSync(join(stagedBinDir, "mediago-server"), "stale-server");

		try {
			expect(() =>
				assertStagedServiceBinariesMatchTarget(stagedBinDir, targetBinDir, ["mediago-server"]),
			).toThrow("stale staged service binary: mediago-server");
		} finally {
			rmSync(root, { recursive: true, force: true });
		}
	});
});
