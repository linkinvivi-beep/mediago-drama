import type React from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

interface UseReferenceFileDropTargetOptions {
	disabled: boolean;
	isImporting: boolean;
	onImportFiles: (files: File[]) => void | Promise<void>;
}

const hasExternalFiles = (event: React.DragEvent<HTMLElement>) =>
	Array.from(event.dataTransfer.types).includes("Files");

export const useReferenceFileDropTarget = ({
	disabled,
	isImporting,
	onImportFiles,
}: UseReferenceFileDropTargetOptions) => {
	const [isDraggingFiles, setIsDraggingFiles] = useState(false);
	const dragDepthRef = useRef(0);
	const enabled = !disabled && !isImporting;

	useEffect(() => {
		if (enabled) return;
		dragDepthRef.current = 0;
		setIsDraggingFiles(false);
	}, [enabled]);

	const onDragEnter = useCallback(
		(event: React.DragEvent<HTMLElement>) => {
			if (!enabled || !hasExternalFiles(event)) return;
			event.preventDefault();
			dragDepthRef.current += 1;
			setIsDraggingFiles(true);
		},
		[enabled],
	);
	const onDragLeave = useCallback(
		(event: React.DragEvent<HTMLElement>) => {
			if (!enabled || !hasExternalFiles(event)) return;
			event.preventDefault();
			dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
			if (dragDepthRef.current === 0) setIsDraggingFiles(false);
		},
		[enabled],
	);
	const onDragOver = useCallback(
		(event: React.DragEvent<HTMLElement>) => {
			if (!enabled || !hasExternalFiles(event)) return;
			event.preventDefault();
		},
		[enabled],
	);
	const onDrop = useCallback(
		(event: React.DragEvent<HTMLElement>) => {
			if (!enabled || !hasExternalFiles(event)) return;
			event.preventDefault();
			dragDepthRef.current = 0;
			setIsDraggingFiles(false);
			const files = Array.from(event.dataTransfer.files);
			if (files.length === 0) return;
			void Promise.resolve(onImportFiles(files)).catch(() => undefined);
		},
		[enabled, onImportFiles],
	);

	const dropTargetProps = useMemo(
		() => ({ onDragEnter, onDragLeave, onDragOver, onDrop }),
		[onDragEnter, onDragLeave, onDragOver, onDrop],
	);

	return { dropTargetProps, isDraggingFiles };
};
