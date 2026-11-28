import { useEffect } from 'react';

export interface UndoAction {
  label: string;
  undo: () => void;
}
export function UndoToast({
  action,
  onClose,
}: {
  action: UndoAction | null;
  onClose: () => void;
}) {
  useEffect(() => {
    if (!action) return;
    const timer = window.setTimeout(onClose, 5000);
    const key = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement;
      if (
        (e.metaKey || e.ctrlKey) &&
        e.key.toLowerCase() === 'z' &&
        !(
          el instanceof HTMLElement &&
          el.closest('input, textarea, [contenteditable="true"]')
        )
      ) {
        e.preventDefault();
        action.undo();
        onClose();
      }
    };
    window.addEventListener('keydown', key);
    return () => {
      clearTimeout(timer);
      window.removeEventListener('keydown', key);
    };
  }, [action, onClose]);
  if (!action) return null;
  return (
    <div
      role="status"
      className="fixed bottom-6 left-6 z-50 flex items-center gap-4 rounded-xl border bg-background p-4 shadow-lg"
    >
      {action.label}
      <button
        className="font-medium underline"
        onClick={() => {
          action.undo();
          onClose();
        }}
      >
        Undo
      </button>
    </div>
  );
}
