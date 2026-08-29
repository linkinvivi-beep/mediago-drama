import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

type WorkspacePackage = {
	name?: string;
	version?: string;
	license?: string;
	dependencies?: {
		"electron-updater"?: string;
	};
	devDependencies?: {
		electron?: string;
		"electron-updater"?: string;
	};
};

const scriptDir = dirname(fileURLToPath(import.meta.url));
const workspaceDir = resolve(scriptDir, "..");
const workspacePackagePath = join(workspaceDir, "package.json");
const rendererDistDir = join(workspaceDir, "dist");
const electronDistDir = join(workspaceDir, "electron", "dist");
const electronAppDir = join(workspaceDir, "electron", "app");
function main(): void {
	const electronTargetPlatform = process.env.MEDIAGO_ELECTRON_TARGET_PLATFORM?.trim();
	assertDarwinArm64Target(electronTargetPlatform);
	ensureDirectory(rendererDistDir, "missing renderer build output");
	ensureDirectory(electronDistDir, "missing Electron main process build output");
	ensureStagedServerBinary();

	const workspacePackage = readWorkspacePackage();
	const electronVersion = normalizeVersion(workspacePackage.devDependencies?.electron);
	const requiresCodeSigning =
		process.env.MEDIAGO_CODE_SIGN === "1" || process.env.MEDIAGO_MAC_SIGN === "1";
	const appPackage = createElectronAppPackage(
		workspacePackage,
		electronVersion,
		requiresCodeSigning,
	);

	rmSync(electronAppDir, { recursive: true, force: true });
	mkdirSync(electronAppDir, { recursive: true });

	writeFileSync(join(electronAppDir, "package.json"), `${JSON.stringify(appPackage, null, 2)}\n`);
	cpSync(electronDistDir, electronAppDir, { recursive: true });
	cpSync(rendererDistDir, join(electronAppDir, "renderer"), { recursive: true });
}

export function assertDarwinArm64Target(
	target: string | undefined,
): asserts target is "darwin-arm64" {
	if (target !== "darwin-arm64") {
		throw new Error(
			`MEDIAGO_ELECTRON_TARGET_PLATFORM must be darwin-arm64 (received ${target || "unset"})`,
		);
	}
}

export function createElectronAppPackage(
	workspacePackage: WorkspacePackage,
	electronVersion: string,
	requiresCodeSigning: boolean,
) {
	const electronUpdaterVersion =
		workspacePackage.dependencies?.["electron-updater"] ??
		workspacePackage.devDependencies?.["electron-updater"];
	if (!electronUpdaterVersion) {
		throw new Error("missing electron-updater dependency in workspace package");
	}

	return {
		name: "medialink",
		productName: "MediaLink",
		version: workspacePackage.version ?? "0.0.0",
		description: "MediaLink desktop workspace",
		license: workspacePackage.license ?? "Apache-2.0",
		private: true,
		type: "module",
		main: "main.js",
		dependencies: { "electron-updater": electronUpdaterVersion },
		build: {
			appId: "app.medialink.desktop",
			productName: "MediaLink",
			asar: true,
			// Flipping Electron fuses mutates the macOS framework binary. Only do so when
			// the build will be signed afterwards; otherwise its embedded signature becomes
			// invalid and macOS terminates the app with CODESIGNING/Invalid Page at launch.
			...(requiresCodeSigning
				? {
						electronFuses: {
							runAsNode: false,
							enableCookieEncryption: true,
							enableNodeOptionsEnvironmentVariable: false,
							enableNodeCliInspectArguments: false,
							enableEmbeddedAsarIntegrityValidation: true,
							onlyLoadAppFromAsar: true,
							loadBrowserProcessSpecificV8Snapshot: false,
							grantFileProtocolExtraPrivileges: false,
						},
					}
				: {}),
			...(requiresCodeSigning ? { forceCodeSigning: true } : {}),
			artifactName: "MediaLink-${version}-macos-arm64.${ext}",
			electronVersion,
			npmRebuild: false,
			directories: {
				output: "../../release",
			},
			files: ["package.json", "*.js", "*.cjs", "renderer/**/*", "!**/*.map"],
			extraResources: [
				{
					from: "../resources",
					to: ".",
				},
			],
			mac: {
				category: "public.app-category.productivity",
				target: ["dmg", "zip"],
				icon: "../../build/icons/icon.icns",
				...(requiresCodeSigning
					? { hardenedRuntime: true, notarize: process.env.MEDIAGO_MAC_NOTARIZE === "1" }
					: { identity: null, hardenedRuntime: false }),
			},
		},
	};
}

function readWorkspacePackage(): WorkspacePackage {
	return JSON.parse(readFileSync(workspacePackagePath, "utf8")) as WorkspacePackage;
}

function ensureDirectory(path: string, message: string): void {
	if (!existsSync(path)) {
		throw new Error(`${message}: ${path}`);
	}
}

function ensureStagedServerBinary(): void {
	const path = join(workspaceDir, "electron", "resources", "bin", "mediago-server");
	if (!existsSync(path)) {
		throw new Error(`missing staged server binary: ${path}`);
	}
}

function normalizeVersion(version: string | undefined): string {
	return version?.replace(/^[^\d]*/, "") || "42.4.1";
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
	try {
		main();
	} catch (error) {
		console.error(error instanceof Error ? error.message : String(error));
		process.exit(1);
	}
}
