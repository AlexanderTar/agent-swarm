import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

afterEach(() => {
  cleanup();
  localStorage.clear();
  sessionStorage.clear();
  window.history.replaceState(null, "", "/");
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

// React Flow in jsdom: https://reactflow.dev/learn/advanced-use/testing
class ResizeObserverStub {
  constructor(private readonly cb: ResizeObserverCallback) {}
  observe(target: Element) {
    this.cb(
      [{ target, contentRect: { width: 1000, height: 700 } } as unknown as ResizeObserverEntry],
      this as unknown as ResizeObserver,
    );
  }
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;

class DOMMatrixReadOnlyStub {
  m22: number;
  constructor(transform?: string) {
    const scale = transform?.match(/scale\(([0-9.]+)\)/)?.[1];
    this.m22 = scale !== undefined ? Number(scale) : 1;
  }
}
if (!("DOMMatrixReadOnly" in globalThis)) {
  (globalThis as Record<string, unknown>).DOMMatrixReadOnly = DOMMatrixReadOnlyStub;
}

Object.defineProperties(HTMLElement.prototype, {
  offsetHeight: { get() { return Number.parseFloat((this as HTMLElement).style.height) || 1; } },
  offsetWidth: { get() { return Number.parseFloat((this as HTMLElement).style.width) || 1; } },
});
(SVGElement.prototype as unknown as { getBBox: () => DOMRect }).getBBox = () =>
  ({ x: 0, y: 0, width: 0, height: 0 }) as DOMRect;

if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}
