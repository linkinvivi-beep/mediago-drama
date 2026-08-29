import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const workspaceDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repositoryDir = resolve(workspaceDir, "../..");

const readTask = (taskfile: string, taskName: string) => {
	const escapedName = taskName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
	const match = taskfile.match(
		new RegExp(`^  ${escapedName}:\\n([\\s\\S]*?)(?=^  [^ \\n]+:\\n|(?![\\s\\S]))`, "m"),
	);
	if (!match) throw new Error(`missing Taskfile task: ${taskName}`);
	return match[1];
};

describe("MediaLink release boundaries", () => {
	it("disables publishing in every Electron packaging command", () => {
		const workspacePackage = JSON.parse(
			readFileSync(resolve(workspaceDir, "package.json"), "utf8"),
		) as { scripts: Record<string, string> };
		expect(workspacePackage.scripts["electron:build"]).toBe("pnpm electron:build:darwin-arm64");
		const packagingCommands = Object.entries(workspacePackage.scripts).filter(([, command]) =>
			command.includes("electron-builder"),
		);

		expect(packagingCommands.length).toBeGreaterThan(0);
		for (const [name, command] of packagingCommands) {
			expect(command, `${name} must never publish`).toContain("--publish never");
		}
	});

	it("routes generic Electron builds through the darwin-arm64 target pipeline", () => {
		const taskfile = readFileSync(resolve(repositoryDir, "Taskfile.yml"), "utf8");

		for (const taskName of ["build", "build:desktop", "build:electron"]) {
			const task = readTask(taskfile, taskName);
			expect(task, `${taskName} must use the target pipeline`).toContain(
				"task: build:electron:target",
			);
			expect(task, `${taskName} must pass the Apple Silicon target`).toContain(
				'PLATFORM: "darwin-arm64"',
			);
		}

		const targetTask = readTask(taskfile, "build:electron:target");
		expect(targetTask).toContain("pnpm -C apps/workspace electron:build:darwin-arm64");
		expect(targetTask).not.toContain("ELECTRON_SCRIPT");
	});
});
