import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ThemeProvider, useTheme } from "./useTheme";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  localStorage.clear();
  document.documentElement.classList.remove("dark");
});

test.each(["light", "dark"])("restores %s and persists toggles", (theme) => {
  localStorage.setItem("nerocd.theme.v2", theme);
  const { result } = renderHook(() => useTheme(), { wrapper: ThemeProvider });
  expect(result.current.theme).toBe(theme);
  expect(document.documentElement.classList.contains("dark")).toBe(theme === "dark");
  act(() => result.current.toggleTheme());
  expect(localStorage.getItem("nerocd.theme.v2")).toBe(theme === "dark" ? "light" : "dark");
});

test("invalid preferences use the default dark theme", () => {
  localStorage.setItem("nerocd.theme.v2", "invalid");
  const { result } = renderHook(() => useTheme(), { wrapper: ThemeProvider });
  expect(result.current.theme).toBe("dark");
});

test("blocked storage does not prevent rendering or toggling", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new DOMException("Blocked", "SecurityError"); });
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new DOMException("Blocked", "SecurityError"); });
  const { result } = renderHook(() => useTheme(), { wrapper: ThemeProvider });
  expect(result.current.theme).toBe("dark");
  act(() => result.current.toggleTheme());
  expect(result.current.theme).toBe("light");
  expect(document.documentElement.classList.contains("dark")).toBe(false);
});
