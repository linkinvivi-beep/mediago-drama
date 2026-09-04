import { cleanup, createEvent, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useReferenceFileDropTarget } from "./useReferenceFileDropTarget";

const dataTransfer = (types: string[], files: File[] = []) => ({ files, types });

const DropTarget = ({
	disabled = false,
	isImporting = false,
	onImportFiles,
}: {
	disabled?: boolean;
	isImporting?: boolean;
	onImportFiles: (files: File[]) => void | Promise<void>;
}) => {
	const { dropTargetProps, isDraggingFiles } = useReferenceFileDropTarget({
		disabled,
		isImporting,
		onImportFiles,
	});
	return (
		<div data-testid="drop-target" data-dragging={isDraggingFiles} {...dropTargetProps}>
			拖入素材
		</div>
	);
};

describe("useReferenceFileDropTarget", () => {
	afterEach(cleanup);

	it("imports external files in order and clears the drag state after drop", () => {
		const first = new File(["a"], "a.png", { type: "image/png" });
		const second = new File(["b"], "b.png", { type: "image/png" });
		const onImportFiles = vi.fn();
		render(<DropTarget onImportFiles={onImportFiles} />);
		const target = screen.getByTestId("drop-target");
		const transfer = dataTransfer(["Files"], [first, second]);

		fireEvent.dragEnter(target, { dataTransfer: transfer });
		expect(target).toHaveAttribute("data-dragging", "true");

		const overEvent = createEvent.dragOver(target, { dataTransfer: transfer });
		fireEvent(target, overEvent);
		expect(overEvent.defaultPrevented).toBe(true);

		const dropEvent = createEvent.drop(target, { dataTransfer: transfer });
		fireEvent(target, dropEvent);
		expect(dropEvent.defaultPrevented).toBe(true);
		expect(onImportFiles).toHaveBeenCalledWith([first, second]);
		expect(target).toHaveAttribute("data-dragging", "false");
	});

	it("ignores non-file drags without preventing their default behavior", () => {
		const onImportFiles = vi.fn();
		render(<DropTarget onImportFiles={onImportFiles} />);
		const target = screen.getByTestId("drop-target");
		const transfer = dataTransfer(["text/plain"]);

		const enterEvent = createEvent.dragEnter(target, { dataTransfer: transfer });
		fireEvent(target, enterEvent);
		const overEvent = createEvent.dragOver(target, { dataTransfer: transfer });
		fireEvent(target, overEvent);
		const dropEvent = createEvent.drop(target, { dataTransfer: transfer });
		fireEvent(target, dropEvent);

		expect(enterEvent.defaultPrevented).toBe(false);
		expect(overEvent.defaultPrevented).toBe(false);
		expect(dropEvent.defaultPrevented).toBe(false);
		expect(onImportFiles).not.toHaveBeenCalled();
		expect(target).toHaveAttribute("data-dragging", "false");
	});

	it("keeps the highlight until nested drag enters have fully left", () => {
		const onImportFiles = vi.fn();
		render(<DropTarget onImportFiles={onImportFiles} />);
		const target = screen.getByTestId("drop-target");
		const transfer = dataTransfer(["Files"]);

		fireEvent.dragEnter(target, { dataTransfer: transfer });
		fireEvent.dragEnter(target, { dataTransfer: transfer });
		fireEvent.dragLeave(target, { dataTransfer: transfer });
		expect(target).toHaveAttribute("data-dragging", "true");

		fireEvent.dragLeave(target, { dataTransfer: transfer });
		expect(target).toHaveAttribute("data-dragging", "false");
	});

	it("does not claim file drops while disabled or importing", () => {
		const onImportFiles = vi.fn();
		const { rerender } = render(<DropTarget disabled onImportFiles={onImportFiles} />);
		const target = screen.getByTestId("drop-target");
		const transfer = dataTransfer(["Files"], [new File(["a"], "a.png", { type: "image/png" })]);

		const disabledDrop = createEvent.drop(target, { dataTransfer: transfer });
		fireEvent(target, disabledDrop);
		expect(disabledDrop.defaultPrevented).toBe(false);

		rerender(<DropTarget isImporting onImportFiles={onImportFiles} />);
		const importingDrop = createEvent.drop(target, { dataTransfer: transfer });
		fireEvent(target, importingDrop);
		expect(importingDrop.defaultPrevented).toBe(false);
		expect(onImportFiles).not.toHaveBeenCalled();
	});
});
