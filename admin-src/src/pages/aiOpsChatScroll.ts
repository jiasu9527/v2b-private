import { aiNearBottom } from './aiOpsModel';

// Tabs retain hidden panes. Their zero-size geometry must never overwrite the
// visible chat position or turn off following while a menu is being changed.
export function watchAIChatScroll(getPane: () => HTMLDivElement | null, follow: { current: boolean }) {
  let disposed = false;
  let restoring = true;
  let frame: number | null = null;
  let observed: HTMLDivElement | null = null;
  const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => refresh());
  const visiblePane = () => {
    const pane = getPane();
    return pane && pane.clientHeight > 0 ? pane : null;
  };
  function refresh() {
    if (disposed || frame !== null) return;
    frame = window.requestAnimationFrame(() => {
      frame = null;
      if (disposed) return;
      const node = getPane();
      if (node !== observed) {
        observer?.disconnect();
        if (node) observer?.observe(node);
        observed = node;
      }
      const pane = visiblePane();
      if (!pane || (!restoring && !follow.current)) return;
      // Only scroll this viewport, never the outer page (scrollIntoView).
      pane.scrollTop = pane.scrollHeight;
      follow.current = true;
      restoring = false;
    });
  }
  follow.current = true;
  refresh();
  return {
    refresh,
    onScroll() {
      if (disposed || restoring) return;
      const pane = visiblePane();
      if (pane) follow.current = aiNearBottom(pane.scrollTop, pane.clientHeight, pane.scrollHeight);
    },
    dispose() {
      disposed = true;
      if (frame !== null) window.cancelAnimationFrame(frame);
      frame = null;
      observer?.disconnect();
    },
  };
}
